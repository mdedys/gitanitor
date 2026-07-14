package worktree

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/mdedys/gitanitor/internal/github"
)

// Run executes the full worktree-cleanup flow: enumerate, classify locally,
// look up PRs, act, and report. It returns the exit code (0 success, 1 on a
// real failure) and, for testability, the classification result.
func (f Flow) Run(repo github.Repo) (int, Result, error) {
	fmt.Fprintf(f.Out, "gitanitor · %s\n\n", repo)

	worktrees, err := List(f.Exec)
	if err != nil {
		fmt.Fprintln(f.Out, err.Error())
		return 1, Result{}, err
	}

	prunable, detached, candidates, localSkipped, err := f.classifyLocal(worktrees)
	if err != nil {
		fmt.Fprintln(f.Out, err.Error())
		return 1, Result{}, err
	}

	branches := make([]string, 0, len(candidates))
	for _, wt := range candidates {
		branches = append(branches, wt.Branch)
	}

	prs, err := github.LookupPRs(f.Exec, repo, branches)
	if err != nil {
		fmt.Fprintln(f.Out, err.Error())
		return 1, Result{}, err
	}

	merged, closed, prSkipped := classifyByPR(candidates, prs, repo.Owner)

	skipped := append(localSkipped, prSkipped...)
	sortByPath(merged)
	sortByPath(closed)
	sortByPath(detached)
	sortByPath(skipped)

	result := Result{Skipped: skipped}

	// Tidy prunable entries (skipped under --dry-run).
	if !f.Opts.DryRun {
		for _, wt := range prunable {
			if err := Remove(f.Exec, wt.Path); err != nil {
				fmt.Fprintln(f.Out, err.Error())
				result.RemovalFailed = true
				continue
			}
			result.ClearedPrunable++
		}
	} else {
		result.ClearedPrunable = len(prunable)
	}

	f.report(repo, result, merged, closed, detached, result.ClearedPrunable)

	if f.Opts.DryRun {
		return exitFor(result), result, nil
	}

	// Act, judgment calls first: closed-PR and detached prompts before the
	// merged batch.
	f.promptEach(&result, closed,
		"closed PR, skipped (run interactively to delete)",
		func(c Candidate) string {
			return fmt.Sprintf("delete %s (branch %s, PR #%d closed without merging)? [y/N]",
				c.Worktree.Path, c.Worktree.Branch, c.PR.Number)
		})
	f.promptEach(&result, detached,
		"detached HEAD, skipped (run interactively to delete)",
		func(c Candidate) string {
			return fmt.Sprintf("delete %s (detached HEAD at %s)? [y/N]",
				c.Worktree.Path, shortSHA(c.Worktree.Head))
		})

	// Merged batch: one confirmation for all, unless --yes.
	if len(merged) > 0 {
		consent := f.Opts.Yes
		if !consent {
			consent = f.Prompt.Confirm(fmt.Sprintf("Delete these %d worktrees? [y/N]", len(merged)))
		}
		if consent {
			for _, c := range merged {
				if err := Remove(f.Exec, c.Worktree.Path); err != nil {
					fmt.Fprintln(f.Out, err.Error())
					result.RemovalFailed = true
					continue
				}
				result.Removed = append(result.Removed, c)
			}
		}
	}

	return exitFor(result), result, nil
}

// promptEach asks a per-worktree y/N question for each candidate and removes
// consented ones. Under --yes, candidates are skipped with yesReason — a
// blanket yes doesn't consent to judgment calls.
func (f Flow) promptEach(result *Result, cs []Candidate, yesReason string, question func(Candidate) string) {
	for _, c := range cs {
		if f.Opts.Yes {
			result.Skipped = append(result.Skipped, Candidate{
				Worktree: c.Worktree, Disposition: Skip, Reason: yesReason})
			continue
		}
		if f.Prompt.Confirm(question(c)) {
			if err := Remove(f.Exec, c.Worktree.Path); err != nil {
				fmt.Fprintln(f.Out, err.Error())
				result.RemovalFailed = true
				continue
			}
			result.Removed = append(result.Removed, c)
		}
	}
}

func exitFor(r Result) int {
	if r.RemovalFailed {
		return 1
	}
	return 0
}

func sortByPath(cs []Candidate) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Worktree.Path < cs[j].Worktree.Path })
}

// report prints the grouped classification report. Empty groups are omitted.
func (f Flow) report(repo github.Repo, result Result, merged, closed, detached []Candidate, prunableCount int) {
	printed := false

	if prunableCount > 0 {
		noun := "entry"
		if prunableCount > 1 {
			noun = "entries"
		}
		fmt.Fprintf(f.Out, "Cleared %d stale worktree %s.\n\n", prunableCount, noun)
		printed = true
	}

	if len(closed) > 0 {
		fmt.Fprintln(f.Out, "Closed without merging — asked individually:")
		writeGroup(f.Out, closed)
		fmt.Fprintln(f.Out)
		printed = true
	}

	if len(detached) > 0 {
		fmt.Fprintln(f.Out, "Detached HEAD — asked individually:")
		writeGroup(f.Out, detached)
		fmt.Fprintln(f.Out)
		printed = true
	}

	if len(merged) > 0 {
		fmt.Fprintln(f.Out, "Deleting (merged):")
		writeGroup(f.Out, merged)
		fmt.Fprintln(f.Out)
		printed = true
	}

	if len(result.Skipped) > 0 {
		fmt.Fprintln(f.Out, "Skipped:")
		writeGroup(f.Out, result.Skipped)
		fmt.Fprintln(f.Out)
		printed = true
	}

	if !printed {
		fmt.Fprintln(f.Out, "nothing to clean")
	}
}

func writeGroup(out interface{ Write([]byte) (int, error) }, cs []Candidate) {
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	for _, c := range cs {
		detail := c.Reason
		if c.PR != nil {
			switch c.Disposition {
			case Merged:
				detail = fmt.Sprintf("PR #%d merged", c.PR.Number)
			case ClosedUnmerged:
				detail = fmt.Sprintf("PR #%d closed", c.PR.Number)
			}
		}
		branch := c.Worktree.Branch
		if branch == "" && c.Worktree.Detached {
			branch = shortSHA(c.Worktree.Head)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", c.Worktree.Path, branch, detail)
	}
	tw.Flush()
}

// shortSHA abbreviates a commit sha for display.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
