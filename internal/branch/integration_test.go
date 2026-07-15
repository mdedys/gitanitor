package branch

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

type realGitHubRunner struct {
	dir   string
	env   []string
	calls []xexec.Invocation
	gh    func(args ...string) xexec.Result
}

func (r *realGitHubRunner) Run(name string, args ...string) xexec.Result {
	r.calls = append(r.calls, xexec.Invocation{Name: name, Args: append([]string(nil), args...)})
	if name == "gh" {
		return r.gh(args...)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return xexec.Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=branch-test", "GIT_AUTHOR_EMAIL=branch@example.com",
		"GIT_COMMITTER_NAME=branch-test", "GIT_COMMITTER_EMAIL=branch@example.com",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestCriterionF_LocalBranchDeletionPreservesRemoteAndWorktree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "main")
	remote := filepath.Join(root, "remote.git")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", remote)
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "branch-test")
	runGit(t, dir, "config", "user.email", "branch@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "remote", "add", "origin", remote)
	runGit(t, dir, "push", "-u", "origin", "main")
	runGit(t, dir, "switch", "-c", "safe")
	if err := os.WriteFile(filepath.Join(dir, "safe.txt"), []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "safe.txt")
	runGit(t, dir, "commit", "-m", "safe")
	safeOID := strings.TrimSpace(runGit(t, dir, "rev-parse", "refs/heads/safe"))
	runGit(t, dir, "push", "-u", "origin", "safe")
	runGit(t, dir, "switch", "main")

	runner := &realGitHubRunner{
		dir: dir,
		env: append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir),
	}
	runner.gh = func(args ...string) xexec.Result {
		if len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
			t.Fatalf("unexpected gh invocation: %v", args)
		}
		aliases := map[string]string{}
		for i := 0; i+1 < len(args); i++ {
			if args[i] != "-f" {
				continue
			}
			key, value, ok := strings.Cut(args[i+1], "=")
			if ok && strings.HasPrefix(key, "b") {
				aliases[key] = value
			}
		}
		repository := map[string]any{}
		for alias, name := range aliases {
			nodes := []any{}
			if name == "safe" {
				nodes = append(nodes, map[string]any{
					"number": 80, "state": "MERGED", "headRefOid": safeOID,
					"headRepositoryOwner": map[string]any{"login": "acme"},
				})
			}
			repository[alias] = map[string]any{"nodes": nodes}
		}
		payload, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": repository}})
		return xexec.Result{Stdout: string(payload)}
	}

	code, result, err := (Flow{Exec: runner, Prompt: noPrompt{}, Out: new(strings.Builder), Opts: Options{Yes: true}}).Run(github.Repo{Owner: "acme", Name: "widget", DefaultBranch: "main"})
	if err != nil || code != 0 || len(result.Removed) != 1 {
		t.Fatalf("cleanup: code=%d err=%v result=%+v", code, err, result)
	}
	if got := runGitAllowFailure(t, dir, "show-ref", "--verify", "--quiet", "refs/heads/safe"); got != 1 {
		t.Fatalf("local safe ref exit = %d, want missing", got)
	}
	if got := runGitAllowFailure(t, dir, "show-ref", "--verify", "--quiet", "refs/remotes/origin/safe"); got != 0 {
		t.Fatalf("remote-tracking safe ref exit = %d, want present", got)
	}
	if got := runGitAllowFailure(t, remote, "show-ref", "--verify", "--quiet", "refs/heads/safe"); got != 0 {
		t.Fatalf("remote branch exit = %d, want present", got)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("main worktree changed: %v", err)
	}

	sawDelete := false
	for _, call := range runner.calls {
		for _, arg := range call.Args {
			if arg == "fetch" || arg == "prune" || arg == "--prune" {
				t.Fatalf("cleanup must not fetch or prune: %v", call)
			}
		}
		if call.Name == "git" && len(call.Args) == 4 && call.Args[0] == "branch" && call.Args[1] == "-D" && call.Args[2] == "--" && call.Args[3] == "safe" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("expected exact local deletion invocation, calls=%v", runner.calls)
	}
}

func runGitAllowFailure(t *testing.T, dir string, args ...string) int {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	t.Fatal(err)
	return 1
}
