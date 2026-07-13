package worktree

import (
	"strings"

	"github.com/mdedys/gitanitor/internal/exec"
)

// Worktree is one entry from `git worktree list --porcelain`.
type Worktree struct {
	Path        string
	Branch      string // refs/heads/ stripped; empty for detached/bare
	Detached    bool
	Bare        bool
	Locked      bool
	LockReason  string
	Prunable    bool
	PruneReason string
	IsMain      bool
}

// List enumerates worktrees. The first stanza is always the main worktree,
// regardless of the directory gitanitor runs from.
func List(r exec.Runner) ([]Worktree, error) {
	res := r.Run("git", "worktree", "list", "--porcelain")
	if res.ExitCode != 0 {
		return nil, &GitError{Stderr: res.Stderr}
	}
	return parseList(res.Stdout), nil
}

// GitError carries git's stderr so callers can relay it verbatim.
type GitError struct {
	Stderr string
}

func (e *GitError) Error() string { return strings.TrimSpace(e.Stderr) }

func parseList(stdout string) []Worktree {
	var worktrees []Worktree
	var cur *Worktree

	flush := func() {
		if cur != nil {
			worktrees = append(worktrees, *cur)
			cur = nil
		}
	}

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Worktree{Path: value}
		case "HEAD":
			// carried implicitly; not needed for classification
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
				cur.LockReason = value
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
				cur.PruneReason = value
			}
		}
	}
	flush()

	if len(worktrees) > 0 {
		worktrees[0].IsMain = true
	}
	return worktrees
}

// isDirty reports whether the worktree has modified, staged, or untracked
// files. Any non-`#` line from status --porcelain=v2 --branch means dirty.
func isDirty(r exec.Runner, path string) (bool, error) {
	res := r.Run("git", "-C", path, "status", "--porcelain=v2", "--branch")
	if res.ExitCode != 0 {
		return false, &GitError{Stderr: res.Stderr}
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			return true, nil
		}
	}
	return false, nil
}

// isUnpushed reports whether the worktree has local commits not on any remote.
// It reads branch.ab from status when an upstream exists; when there is no
// upstream (never pushed, or remote branch deleted on merge), it falls back to
// checking whether HEAD is contained by any remote ref.
func isUnpushed(r exec.Runner, path string) (bool, error) {
	res := r.Run("git", "-C", path, "status", "--porcelain=v2", "--branch")
	if res.ExitCode != 0 {
		return false, &GitError{Stderr: res.Stderr}
	}

	hasUpstream := false
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if ab, ok := strings.CutPrefix(line, "# branch.ab "); ok {
			hasUpstream = true
			ahead := parseAhead(ab)
			if ahead > 0 {
				return true, nil
			}
		}
	}
	if hasUpstream {
		return false, nil
	}

	// No upstream: fall back to remote containment.
	contains := r.Run("git", "-C", path, "branch", "-r", "--contains", "HEAD")
	if contains.ExitCode != 0 {
		return false, &GitError{Stderr: contains.Stderr}
	}
	return strings.TrimSpace(contains.Stdout) == "", nil
}

// parseAhead extracts N from a "+N -M" ahead/behind pair.
func parseAhead(ab string) int {
	fields := strings.Fields(ab)
	if len(fields) == 0 {
		return 0
	}
	ahead := strings.TrimPrefix(fields[0], "+")
	n := 0
	for _, c := range ahead {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Remove deletes a worktree's directory and admin entry. It never passes
// --force: the states requiring force are exactly the states gitanitor skips.
func Remove(r exec.Runner, path string) error {
	res := r.Run("git", "worktree", "remove", path)
	if res.ExitCode != 0 {
		return &GitError{Stderr: res.Stderr}
	}
	return nil
}
