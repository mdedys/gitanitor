package worktree

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	xexec "github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

// lab is a real git repository built in t.TempDir() with a bare "remote",
// used to exercise enumeration, state detection, and removal end-to-end.
type lab struct {
	t      *testing.T
	dir    string // main worktree
	remote string // bare remote repo
}

// git runs a real git command inside the lab's main worktree and fails the
// test on nonzero exit.
func (l *lab) git(args ...string) string {
	l.t.Helper()
	return l.gitIn(l.dir, args...)
}

func (l *lab) gitIn(dir string, args ...string) string {
	l.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=lab", "GIT_AUTHOR_EMAIL=lab@example.com",
		"GIT_COMMITTER_NAME=lab", "GIT_COMMITTER_EMAIL=lab@example.com",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+l.dir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		l.t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newLab creates a main worktree with an initial commit and a bare remote,
// with the main branch pushed and tracking.
func newLab(t *testing.T) *lab {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	l := &lab{
		t:      t,
		dir:    filepath.Join(root, "main"),
		remote: filepath.Join(root, "remote.git"),
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l.gitIn(root, "init", "--bare", "remote.git")
	l.git("init", "-b", "main")
	l.git("config", "user.name", "lab")
	l.git("config", "user.email", "lab@example.com")
	writeFile(t, l.dir, "README.md", "hello")
	l.git("add", ".")
	l.git("commit", "-m", "initial")
	l.git("remote", "add", "origin", l.remote)
	l.git("push", "-u", "origin", "main")
	return l
}

// addWorktree creates a linked worktree on a new branch off main and returns
// its path.
func (l *lab) addWorktree(name string) string {
	l.t.Helper()
	path := filepath.Join(filepath.Dir(l.dir), name)
	l.git("worktree", "add", "-b", name, path, "main")
	return path
}

// pushBranch pushes a worktree's branch to the remote with tracking.
func (l *lab) pushBranch(path, branch string) {
	l.t.Helper()
	l.gitIn(path, "push", "-u", "origin", branch)
}

// commitIn makes a commit inside a worktree.
func (l *lab) commitIn(path, file, content, msg string) {
	l.t.Helper()
	writeFile(l.t, path, file, content)
	l.gitIn(path, "add", ".")
	l.gitIn(path, "commit", "-m", msg)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ghGraphQLResponder builds a canned gh GraphQL response for the given
// branch→PRs map.
// It reads the alias→branch mapping out of the -f b<i>=<branch> args so the
// response matches whatever query the code generated.
func ghGraphQLResponder(prsByBranch map[string][]github.PR) func(args ...string) xexec.Result {
	return func(args ...string) xexec.Result {
		aliasBranch := map[string]string{}
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-f" {
				k, v, ok := strings.Cut(args[i+1], "=")
				if ok && strings.HasPrefix(k, "b") && k != "b" {
					aliasBranch[k] = v
				}
			}
		}
		repository := map[string]any{}
		for alias, branch := range aliasBranch {
			nodes := []any{}
			for _, pr := range prsByBranch[branch] {
				nodes = append(nodes, map[string]any{
					"number":              pr.Number,
					"state":               string(pr.State),
					"url":                 pr.URL,
					"headRepositoryOwner": map[string]any{"login": ownerOr(pr.Owner)},
				})
			}
			repository[alias] = map[string]any{"nodes": nodes}
		}
		payload := map[string]any{"data": map[string]any{"repository": repository}}
		b, _ := json.Marshal(payload)
		return xexec.Result{Stdout: string(b)}
	}
}

func ownerOr(o string) string {
	if o == "" {
		return "mdedys"
	}
	return o
}

// newHybrid builds a Hybrid runner wired to real git and a canned gh responder.
func (l *lab) newHybrid(prsByBranch map[string][]github.PR) *xexec.Hybrid {
	return &xexec.Hybrid{
		Git: gitRunner{env: l.env(), dir: l.dir},
		GH:  ghGraphQLResponder(prsByBranch),
	}
}

func (l *lab) env() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=lab", "GIT_AUTHOR_EMAIL=lab@example.com",
		"GIT_COMMITTER_NAME=lab", "GIT_COMMITTER_EMAIL=lab@example.com",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+l.dir,
	)
}

// gitRunner is a Runner that executes real git with a fixed environment and
// working directory, mirroring how gitanitor runs from inside a repository.
type gitRunner struct {
	env []string
	dir string
}

func (g gitRunner) Run(name string, args ...string) xexec.Result {
	cmd := exec.Command(name, args...)
	cmd.Env = g.env
	cmd.Dir = g.dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
			stderr.WriteString(err.Error())
		}
	}
	return xexec.Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

// alwaysYes / alwaysNo are test prompters.
type alwaysYes struct{}

func (alwaysYes) Confirm(string) bool { return true }

type alwaysNo struct{}

func (alwaysNo) Confirm(string) bool { return false }

// scriptedPrompt answers each question with the next queued reply and records
// the questions asked so tests can assert on ordering.
type scriptedPrompt struct {
	replies []bool
	asked   []string
}

func (s *scriptedPrompt) Confirm(q string) bool {
	s.asked = append(s.asked, q)
	if len(s.asked)-1 < len(s.replies) {
		return s.replies[len(s.asked)-1]
	}
	return false
}
