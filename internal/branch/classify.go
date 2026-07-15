package branch

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

func (f Flow) classify(refs []Ref, checked map[string][]string, prs map[string][]github.PR, repo github.Repo) (Result, error) {
	result := Result{}
	var failures []error
	for _, ref := range refs {
		paths := checked[ref.Name]
		if ref.Name == repo.DefaultBranch {
			reason := "default branch"
			if len(paths) > 0 {
				reason += "; checked out at " + strings.Join(paths, ", ")
			}
			result.Skipped = append(result.Skipped, Candidate{Branch: ref, Disposition: Skip, Reason: reason})
			continue
		}
		if len(paths) > 0 {
			result.Skipped = append(result.Skipped, Candidate{Branch: ref, Disposition: Skip,
				Reason: "checked out at " + strings.Join(paths, ", ")})
			continue
		}

		surviving := matchingOwnerPRs(prs[ref.Name], repo.Owner)
		open := firstState(surviving, github.Open)
		if open != nil {
			result.Skipped = append(result.Skipped, Candidate{Branch: ref, Disposition: Skip,
				Reason: fmt.Sprintf("PR #%d still open", open.Number), PR: clonePR(open)})
			continue
		}

		historical := statePRs(surviving, github.Merged, github.Closed)
		if len(historical) == 0 {
			result.Skipped = append(result.Skipped, Candidate{Branch: ref, Disposition: Skip, Reason: "no PR"})
			continue
		}

		coveredMerged := []*github.PR{}
		coveredClosed := []*github.PR{}
		localHeads := []string{}
		for i := range historical {
			pr := &historical[i]
			if pr.HeadOID == "" {
				failures = appendClassificationFailure(&result, failures, ref,
					fmt.Sprintf("PR #%d is missing its recorded head commit", pr.Number))
				break
			}
			covered, err := coversTip(f.Exec, repo, ref.OID, pr.HeadOID)
			if err != nil {
				failures = appendClassificationFailure(&result, failures, ref,
					fmt.Sprintf("compare branch %s with PR #%d: %v", ref.Name, pr.Number, err))
				break
			}
			if covered {
				switch pr.State {
				case github.Merged:
					coveredMerged = append(coveredMerged, pr)
				case github.Closed:
					coveredClosed = append(coveredClosed, pr)
				}
			}
			if localCommitExists(f.Exec, pr.HeadOID) {
				localHeads = append(localHeads, pr.HeadOID)
			}
		}
		if hasClassificationFailure(result, ref.Name) {
			continue
		}

		sort.Slice(coveredMerged, func(i, j int) bool { return coveredMerged[i].Number < coveredMerged[j].Number })
		sort.Slice(coveredClosed, func(i, j int) bool { return coveredClosed[i].Number < coveredClosed[j].Number })
		switch {
		case len(coveredMerged) > 0:
			pr := clonePR(coveredMerged[0])
			result.Merged = append(result.Merged, Candidate{Branch: ref, Disposition: Merged,
				Reason: fmt.Sprintf("PR #%d merged", pr.Number), PR: pr})
		case len(coveredClosed) > 0:
			pr := clonePR(coveredClosed[0])
			result.Closed = append(result.Closed, Candidate{Branch: ref, Disposition: ClosedUnmerged,
				Reason: fmt.Sprintf("PR #%d closed without merging", pr.Number), PR: pr})
		default:
			unique, err := localUniqueCount(f.Exec, ref.OID, localHeads)
			if err != nil {
				failures = appendClassificationFailure(&result, failures, ref,
					fmt.Sprintf("count local-only commits for branch %s: %v", ref.Name, err))
				continue
			}
			reason := historicalSummary(historical)
			if unique > 0 {
				result.LocalOnly = append(result.LocalOnly, Candidate{Branch: ref, Disposition: LocalOnly,
					Reason: fmt.Sprintf("%s; %d unpushed commits", reason, unique),
					PR:     clonePR(&historical[0]), UniqueCommits: unique})
			} else {
				result.Skipped = append(result.Skipped, Candidate{Branch: ref, Disposition: Skip,
					Reason: fmt.Sprintf("%s; current tip not covered by a PR head and newer work is pushed", reason)})
			}
		}
	}
	if len(failures) > 0 {
		return result, errors.Join(failures...)
	}
	return result, nil
}

func appendClassificationFailure(result *Result, failures []error, ref Ref, detail string) []error {
	message := fmt.Sprintf("classification failed for branch %s: %s", ref.Name, detail)
	result.Skipped = append(result.Skipped, Candidate{Branch: ref, Disposition: Skip, Reason: message})
	return append(failures, errors.New(message))
}

func hasClassificationFailure(result Result, branch string) bool {
	for _, candidate := range result.Skipped {
		if candidate.Branch.Name == branch && strings.HasPrefix(candidate.Reason, "classification failed for branch ") {
			return true
		}
	}
	return false
}

func matchingOwnerPRs(prs []github.PR, owner string) []github.PR {
	result := make([]github.PR, 0, len(prs))
	for _, pr := range prs {
		if pr.Owner == owner {
			result = append(result, pr)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result
}

func firstState(prs []github.PR, state github.PRState) *github.PR {
	for i := range prs {
		if prs[i].State == state {
			return &prs[i]
		}
	}
	return nil
}

func statePRs(prs []github.PR, states ...github.PRState) []github.PR {
	result := []github.PR{}
	for _, pr := range prs {
		for _, state := range states {
			if pr.State == state {
				result = append(result, pr)
				break
			}
		}
	}
	return result
}

func historicalSummary(prs []github.PR) string {
	parts := make([]string, 0, len(prs))
	for _, pr := range prs {
		label := "closed without merging"
		if pr.State == github.Merged {
			label = "merged"
		}
		parts = append(parts, fmt.Sprintf("PR #%d %s", pr.Number, label))
	}
	return strings.Join(parts, "; ")
}

func clonePR(pr *github.PR) *github.PR {
	if pr == nil {
		return nil
	}
	copy := *pr
	return &copy
}

func localCommitExists(r exec.Runner, oid string) bool {
	res := r.Run("git", "cat-file", "-e", oid+"^{commit}")
	return res.ExitCode == 0
}

func coversTip(r exec.Runner, repo github.Repo, tipOID, headOID string) (bool, error) {
	if tipOID == headOID {
		return true, nil
	}
	if localCommitExists(r, headOID) {
		res := r.Run("git", "merge-base", "--is-ancestor", tipOID, headOID)
		switch res.ExitCode {
		case 0:
			return true, nil
		case 1:
			return false, nil
		default:
			return false, &GitError{Stderr: res.Stderr}
		}
	}
	status, err := github.CompareCommits(r, repo, tipOID, headOID)
	if err != nil {
		return false, err
	}
	switch status {
	case "ahead", "identical":
		return true, nil
	case "behind", "diverged":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected GitHub comparison status %q", status)
	}
}
