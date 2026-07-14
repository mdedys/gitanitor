package worktree

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

// Disposition is how the flow decided to treat a worktree.
type Disposition int

const (
	Skip Disposition = iota
	Merged
	ClosedUnmerged
	Detached
)

// Candidate is a classified worktree carrying the reason for its disposition.
type Candidate struct {
	Worktree    Worktree
	Disposition Disposition
	Reason      string     // human-readable skip reason
	PR          *github.PR // set for Merged and ClosedUnmerged
}

// Options controls a single run.
type Options struct {
	Yes    bool
	DryRun bool
}

// Prompter answers a yes/no question. Default No.
type Prompter interface {
	Confirm(question string) bool
}

// StdinPrompter reads y/N answers from a reader.
type StdinPrompter struct {
	In  io.Reader
	Out io.Writer
}

func (p StdinPrompter) Confirm(question string) bool {
	fmt.Fprintf(p.Out, "%s ", question)
	scanner := bufio.NewScanner(p.In)
	if !scanner.Scan() {
		return false
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// Runner ties together the exec seam, prompter, and output for a run.
type Flow struct {
	Exec   exec.Runner
	Prompt Prompter
	Out    io.Writer
	Opts   Options
}

// Result summarizes what a run did, for exit-code decisions and reporting.
type Result struct {
	ClearedPrunable int
	Removed         []Candidate
	Skipped         []Candidate
	RemovalFailed   bool
}

// classify partitions non-main worktrees into local dispositions and the set
// still needing a PR lookup. Prunable entries and detached-HEAD prompt
// candidates are returned separately. Clean, pushed detached worktrees have no
// branch to look a PR up for, so they are offered per-worktree like closed PRs.
func (f Flow) classifyLocal(worktrees []Worktree) (prunable []Worktree, detached []Candidate, candidates []Worktree, skipped []Candidate, err error) {
	for _, wt := range worktrees {
		if wt.IsMain {
			continue
		}
		switch {
		case wt.Prunable:
			prunable = append(prunable, wt)
		case wt.Locked:
			reason := "locked"
			if wt.LockReason != "" {
				reason = "locked: " + wt.LockReason
			}
			skipped = append(skipped, Candidate{Worktree: wt, Disposition: Skip, Reason: reason})
		default:
			dirty, derr := isDirty(f.Exec, wt.Path)
			if derr != nil {
				return nil, nil, nil, nil, derr
			}
			if dirty {
				skipped = append(skipped, Candidate{Worktree: wt, Disposition: Skip, Reason: "uncommitted changes"})
				continue
			}
			unpushed, uerr := isUnpushed(f.Exec, wt.Path)
			if uerr != nil {
				return nil, nil, nil, nil, uerr
			}
			if unpushed {
				skipped = append(skipped, Candidate{Worktree: wt, Disposition: Skip, Reason: "unpushed commits"})
				continue
			}
			if wt.Detached {
				detached = append(detached, Candidate{Worktree: wt, Disposition: Detached, Reason: "detached HEAD"})
				continue
			}
			candidates = append(candidates, wt)
		}
	}
	return prunable, detached, candidates, skipped, nil
}

// classifyByPR resolves each remaining candidate against its PRs. The fork
// guard discards PRs whose head repository owner differs from the query owner.
func classifyByPR(candidates []Worktree, prs map[string][]github.PR, owner string) (merged, closed, skipped []Candidate) {
	for _, wt := range candidates {
		surviving := make([]github.PR, 0, len(prs[wt.Branch]))
		for _, pr := range prs[wt.Branch] {
			if pr.Owner != "" && pr.Owner != owner {
				continue
			}
			surviving = append(surviving, pr)
		}

		var open, mergedPR, closedPR *github.PR
		for i := range surviving {
			switch surviving[i].State {
			case github.Open:
				open = &surviving[i]
			case github.Merged:
				if mergedPR == nil {
					mergedPR = &surviving[i]
				}
			case github.Closed:
				if closedPR == nil {
					closedPR = &surviving[i]
				}
			}
		}

		switch {
		case open != nil:
			skipped = append(skipped, Candidate{Worktree: wt, Disposition: Skip,
				Reason: fmt.Sprintf("PR #%d still open", open.Number), PR: open})
		case mergedPR != nil:
			merged = append(merged, Candidate{Worktree: wt, Disposition: Merged,
				Reason: fmt.Sprintf("PR #%d merged", mergedPR.Number), PR: mergedPR})
		case closedPR != nil:
			closed = append(closed, Candidate{Worktree: wt, Disposition: ClosedUnmerged,
				Reason: fmt.Sprintf("PR #%d closed", closedPR.Number), PR: closedPR})
		default:
			skipped = append(skipped, Candidate{Worktree: wt, Disposition: Skip, Reason: "no PR"})
		}
	}
	return merged, closed, skipped
}
