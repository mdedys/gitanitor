package branch

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

// Ref is a local branch ref and the commit it pointed to during preflight.
type Ref struct {
	Name string
	OID  string
}

// Disposition identifies the action group assigned during classification.
type Disposition int

const (
	Skip Disposition = iota
	Merged
	ClosedUnmerged
	LocalOnly
)

// Candidate is a classified local branch.
type Candidate struct {
	Branch        Ref
	Disposition   Disposition
	Reason        string
	PR            *github.PR
	UniqueCommits int
}

// Options controls one branch-cleanup run.
type Options struct {
	Yes    bool
	DryRun bool
}

// Prompter answers a default-No yes/no question.
type Prompter interface {
	Confirm(question string) bool
}

// StdinPrompter reads one y/N answer per question.
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
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// Result captures classification and mutation outcomes for tests and exit
// code decisions.
type Result struct {
	Merged       []Candidate
	Closed       []Candidate
	LocalOnly    []Candidate
	Skipped      []Candidate
	Removed      []Candidate
	Failed       []Candidate
	MutationFail bool
}

// Flow owns one complete local-branch scan and cleanup run.
type Flow struct {
	Exec   exec.Runner
	Prompt Prompter
	Out    io.Writer
	Opts   Options
}

// GitError carries git stderr so callers can relay useful details.
type GitError struct{ Stderr string }

func (e *GitError) Error() string { return strings.TrimSpace(e.Stderr) }

// List enumerates every local branch and records its current tip.
func List(r exec.Runner) ([]Ref, error) {
	res := r.Run("git", "for-each-ref", "--format=%(refname:short)%00%(objectname)", "refs/heads")
	if res.ExitCode != 0 {
		return nil, &GitError{Stderr: res.Stderr}
	}
	refs, err := parseRefs(res.Stdout)
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

func parseRefs(stdout string) ([]Ref, error) {
	refs := []Ref{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, &GitError{Stderr: fmt.Sprintf("could not parse local branch ref %q", line)}
		}
		refs = append(refs, Ref{Name: parts[0], OID: parts[1]})
	}
	return refs, nil
}

// checkedOutBranches returns every worktree path keyed by its checked-out
// branch. Detached and bare worktrees have no branch ref to protect here.
func checkedOutBranches(r exec.Runner) (map[string][]string, error) {
	res := r.Run("git", "worktree", "list", "--porcelain")
	if res.ExitCode != 0 {
		return nil, &GitError{Stderr: res.Stderr}
	}
	paths := map[string][]string{}
	var path, branch string
	flush := func() {
		if branch != "" {
			paths[branch] = append(paths[branch], path)
		}
		path, branch = "", ""
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	flush()
	for name := range paths {
		sort.Strings(paths[name])
	}
	return paths, nil
}

func refOID(r exec.Runner, name string) (string, error) {
	res := r.Run("git", "rev-parse", "--verify", "--end-of-options", "refs/heads/"+name)
	if res.ExitCode != 0 {
		return "", &GitError{Stderr: res.Stderr}
	}
	oid := strings.TrimSpace(res.Stdout)
	if oid == "" {
		return "", &GitError{Stderr: "git returned an empty branch OID"}
	}
	return oid, nil
}

func localUniqueCount(r exec.Runner, tip string, historicalHeads []string) (int, error) {
	args := []string{"rev-list", "--count", tip, "--not", "--remotes"}
	args = append(args, historicalHeads...)
	res := r.Run("git", args...)
	if res.ExitCode != 0 {
		return 0, &GitError{Stderr: res.Stderr}
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(res.Stdout), "%d", &count); err != nil || count < 0 {
		return 0, &GitError{Stderr: fmt.Sprintf("could not parse local-only commit count %q", strings.TrimSpace(res.Stdout))}
	}
	return count, nil
}
