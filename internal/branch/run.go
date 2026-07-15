package branch

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/mdedys/gitanitor/internal/github"
)

// Run performs the full scan and only begins mutation after all inputs have
// been enumerated, looked up, compared, and classified successfully.
func (f Flow) Run(repo github.Repo) (int, Result, error) {
	if repo.DefaultBranch == "" {
		err := &GitError{Stderr: "GitHub repository metadata did not include a default branch"}
		f.printError(err)
		return 1, Result{}, err
	}

	refs, err := List(f.Exec)
	if err != nil {
		f.printError(err)
		return 1, Result{}, err
	}
	checked, err := checkedOutBranches(f.Exec)
	if err != nil {
		f.printError(err)
		return 1, Result{}, err
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	prs, err := github.LookupPRs(f.Exec, repo, names)
	if err != nil {
		f.printError(err)
		return 1, Result{}, err
	}
	result, err := f.classify(refs, checked, prs, repo)
	if err != nil {
		f.printError(err)
		return 1, Result{}, err
	}
	sortResult(&result)

	f.report(repo, len(refs), result)
	if f.Opts.DryRun {
		return 0, result, nil
	}

	if f.Opts.Yes {
		for _, c := range append(append([]Candidate{}, result.Closed...), result.LocalOnly...) {
			result.Skipped = append(result.Skipped, Candidate{
				Branch: c.Branch, Disposition: Skip,
				Reason: "judgment call skipped by --yes: " + c.Reason,
			})
		}
	} else {
		for _, c := range result.Closed {
			f.promptAndDelete(&result, c)
		}
		for _, c := range result.LocalOnly {
			f.promptAndDelete(&result, c)
		}
	}

	if len(result.Merged) > 0 {
		consent := f.Opts.Yes
		if !consent {
			consent = f.confirm(fmt.Sprintf("Delete these %d branches? [y/N]", len(result.Merged)))
		}
		if consent {
			for _, c := range result.Merged {
				f.deleteCandidate(&result, c)
			}
		}
	}

	if result.MutationFail {
		return 1, result, nil
	}
	return 0, result, nil
}

func (f Flow) promptAndDelete(result *Result, c Candidate) {
	if f.confirm(promptQuestion(c)) {
		f.deleteCandidate(result, c)
	}
}

func promptQuestion(c Candidate) string {
	return fmt.Sprintf("delete %s (%s)? [y/N]", c.Branch.Name, c.Reason)
}

func (f Flow) confirm(question string) bool {
	if f.Prompt == nil {
		return false
	}
	return f.Prompt.Confirm(question)
}

func (f Flow) deleteCandidate(result *Result, candidate Candidate) {
	oid, err := refOID(f.Exec, candidate.Branch.Name)
	if err != nil {
		f.failure(result, candidate, fmt.Sprintf("%s: branch changed during cleanup", candidate.Branch.Name))
		return
	}
	checked, err := checkedOutBranches(f.Exec)
	if err != nil {
		f.failure(result, candidate, err.Error())
		return
	}
	if oid != candidate.Branch.OID || len(checked[candidate.Branch.Name]) > 0 {
		f.failure(result, candidate, fmt.Sprintf("%s: branch changed during cleanup", candidate.Branch.Name))
		return
	}
	res := f.Exec.Run("git", "branch", "-D", "--", candidate.Branch.Name)
	if res.ExitCode != 0 {
		f.failure(result, candidate, (&GitError{Stderr: res.Stderr}).Error())
		return
	}
	result.Removed = append(result.Removed, candidate)
}

func (f Flow) failure(result *Result, candidate Candidate, message string) {
	if message == "" {
		message = "branch cleanup failed"
	}
	f.printErrorString(message)
	result.Failed = append(result.Failed, candidate)
	result.MutationFail = true
}

func (f Flow) printError(err error) { f.printErrorString(err.Error()) }

func (f Flow) printErrorString(message string) {
	if f.Out != nil {
		fmt.Fprintln(f.Out, message)
	}
}

func sortResult(result *Result) {
	for _, group := range [][]Candidate{result.Merged, result.Closed, result.LocalOnly, result.Skipped} {
		sort.Slice(group, func(i, j int) bool { return group[i].Branch.Name < group[j].Branch.Name })
	}
}

func (f Flow) report(repo github.Repo, scanned int, result Result) {
	if f.Out == nil {
		return
	}
	fmt.Fprintf(f.Out, "gitanitor · %s (default branch: %s)\n", repo, repo.DefaultBranch)
	fmt.Fprintf(f.Out, "Scanned %d local branches.\n\n", scanned)

	if len(result.Merged) > 0 {
		switch {
		case f.Opts.DryRun:
			fmt.Fprintln(f.Out, "Would delete (merged):")
		case f.Opts.Yes:
			fmt.Fprintln(f.Out, "Deleting (merged):")
		default:
			fmt.Fprintln(f.Out, "Safe merged batch (pending confirmation):")
		}
		writeGroup(f.Out, result.Merged)
		fmt.Fprintln(f.Out)
	}
	if len(result.Closed) > 0 {
		label := "Closed without merging — asked individually:"
		if f.Opts.DryRun {
			label = "Would ask individually (closed without merging):"
		} else if f.Opts.Yes {
			label = "Judgment calls skipped by --yes (closed without merging):"
		}
		fmt.Fprintln(f.Out, label)
		writeGroup(f.Out, result.Closed)
		fmt.Fprintln(f.Out)
	}
	if len(result.LocalOnly) > 0 {
		label := "Newer or divergent local-only work — asked individually:"
		if f.Opts.DryRun {
			label = "Would ask individually (local-only work):"
		} else if f.Opts.Yes {
			label = "Judgment calls skipped by --yes (local-only work):"
		}
		fmt.Fprintln(f.Out, label)
		writeGroup(f.Out, result.LocalOnly)
		fmt.Fprintln(f.Out)
	}
	if len(result.Skipped) > 0 {
		label := "Skipped:"
		if f.Opts.DryRun {
			label = "Would skip:"
		}
		fmt.Fprintln(f.Out, label)
		writeGroup(f.Out, result.Skipped)
		fmt.Fprintln(f.Out)
	}
	if len(result.Merged) == 0 && len(result.Closed) == 0 && len(result.LocalOnly) == 0 && len(result.Skipped) == 0 {
		fmt.Fprintln(f.Out, "nothing to clean")
	}
}

func writeGroup(out io.Writer, candidates []Candidate) {
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	for _, c := range candidates {
		fmt.Fprintf(tw, "  %s\t%s\n", c.Branch.Name, c.Reason)
	}
	_ = tw.Flush()
}
