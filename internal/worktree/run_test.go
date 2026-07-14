package worktree

import (
	"bytes"
	"os"
	"strings"
	"testing"

	xexec "github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

var testRepo = github.Repo{Owner: "mdedys", Name: "gitanitor"}

// newFlow builds a Flow over a hybrid runner with the given prompter.
func (l *lab) newFlow(prs map[string][]github.PR, prompt Prompter, opts Options) (*Flow, *xexec.Hybrid, *bytes.Buffer) {
	h := l.newHybrid(prs)
	out := &bytes.Buffer{}
	f := &Flow{Exec: h, Prompt: prompt, Out: out, Opts: opts}
	return f, h, out
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func forceUsed(calls []xexec.Invocation) bool {
	for _, c := range calls {
		if c.Name != "git" {
			continue
		}
		isRemove := len(c.Args) >= 3 && c.Args[0] == "worktree" && c.Args[1] == "remove"
		if !isRemove {
			continue
		}
		for _, a := range c.Args {
			if a == "--force" || a == "-f" {
				return true
			}
		}
	}
	return false
}

// Criterion (b): a confirmed merged-PR worktree is deleted, including when the
// remote branch is gone after delete-on-merge, and nothing else is touched.
func TestCriterionB_MergedWorktreeDeleted(t *testing.T) {
	l := newLab(t)
	keep := l.addWorktree("keep-me")
	l.pushBranch(keep, "keep-me")
	merged := l.addWorktree("feat-merged")
	l.pushBranch(merged, "feat-merged")

	// Simulate GitHub delete-on-merge: the remote branch is gone, so the
	// worktree has no upstream, yet its commits are on the remote's main line.
	// Push then delete the remote ref to reproduce the gone-upstream state
	// while HEAD remains reachable from origin/main.
	l.gitIn(merged, "push", "origin", "--delete", "feat-merged")

	prs := map[string][]github.PR{
		"feat-merged": {{Number: 41, State: github.Merged, Owner: "mdedys"}},
		"keep-me":     {{Number: 42, State: github.Open, Owner: "mdedys"}},
	}
	f, h, out := l.newFlow(prs, alwaysYes{}, Options{})

	code, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if dirExists(merged) {
		t.Errorf("merged worktree %s should have been removed", merged)
	}
	if !dirExists(keep) {
		t.Errorf("open-PR worktree %s must not be touched", keep)
	}
	if !dirExists(l.dir) {
		t.Errorf("main worktree must never be touched")
	}
	if len(res.Removed) != 1 || res.Removed[0].Worktree.Branch != "feat-merged" {
		t.Errorf("expected exactly feat-merged removed, got %+v", res.Removed)
	}
	if forceUsed(h.Calls) {
		t.Errorf("no removal may use --force")
	}
}

// Criterion (c): each skip reason reported with its reason and not deleted.
func TestCriterionC_DirtySkip(t *testing.T) {
	l := newLab(t)
	dirty := l.addWorktree("feat-dirty")
	l.pushBranch(dirty, "feat-dirty")
	writeFile(t, dirty, "scratch.txt", "uncommitted")

	prs := map[string][]github.PR{"feat-dirty": {{Number: 1, State: github.Merged, Owner: "mdedys"}}}
	f, h, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})

	code, res, err := f.Run(testRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	if !dirExists(dirty) {
		t.Fatalf("dirty worktree must not be deleted")
	}
	assertSkipped(t, res, dirty, "uncommitted changes")
	if !strings.Contains(out.String(), "uncommitted changes") {
		t.Errorf("report must mention the skip reason:\n%s", out.String())
	}
	if forceUsed(h.Calls) {
		t.Errorf("no --force")
	}
}

func TestCriterionC_UnpushedSkip(t *testing.T) {
	l := newLab(t)
	// Pushed then add a local commit ahead of upstream.
	wt := l.addWorktree("feat-ahead")
	l.pushBranch(wt, "feat-ahead")
	l.commitIn(wt, "extra.txt", "local only", "local work")

	prs := map[string][]github.PR{"feat-ahead": {{Number: 2, State: github.Merged, Owner: "mdedys"}}}
	f, _, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})

	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("unpushed worktree must not be deleted")
	}
	assertSkipped(t, res, wt, "unpushed commits")
	if !strings.Contains(out.String(), "unpushed commits") {
		t.Errorf("report must mention unpushed:\n%s", out.String())
	}
}

// Criterion (c): the gone-upstream fallback for unpushed detection — a branch
// whose upstream is gone AND whose commits are not on any remote must be
// treated as unpushed.
func TestCriterionC_UnpushedGoneUpstreamFallback(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-gone")
	// Never pushed at all: no upstream, commit is local-only.
	l.commitIn(wt, "local.txt", "never pushed", "local work")

	prs := map[string][]github.PR{"feat-gone": {{Number: 3, State: github.Merged, Owner: "mdedys"}}}
	f, _, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})

	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("local-only worktree (gone upstream) must not be deleted")
	}
	assertSkipped(t, res, wt, "unpushed commits")
	_ = out
}

func TestCriterionC_LockedSkip(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-locked")
	l.pushBranch(wt, "feat-locked")
	l.git("worktree", "lock", "--reason", "usb drive", wt)

	prs := map[string][]github.PR{"feat-locked": {{Number: 4, State: github.Merged, Owner: "mdedys"}}}
	f, h, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})

	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("locked worktree must not be deleted")
	}
	assertSkippedReasonContains(t, res, wt, "locked")
	if !strings.Contains(out.String(), "locked") {
		t.Errorf("report must mention locked:\n%s", out.String())
	}
	if forceUsed(h.Calls) {
		t.Errorf("no --force")
	}
}

func TestCriterionC_DetachedSkip(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-detach")
	// Detach HEAD in the worktree.
	sha := strings.TrimSpace(l.gitIn(wt, "rev-parse", "HEAD"))
	l.gitIn(wt, "checkout", sha)

	// alwaysYes would delete anything prompted — proving --yes must not prompt.
	f, _, out := l.newFlow(map[string][]github.PR{}, alwaysYes{}, Options{Yes: true})

	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("detached worktree must never be deleted under --yes")
	}
	assertSkippedReasonContains(t, res, wt, "detached HEAD, skipped")
	_ = out
}

// Detached worktrees that are clean and pushed are offered per-worktree;
// answering y deletes them.
func TestDetachedPromptedAndDeleted(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-detach")
	// Detach at main's commit, which is on origin/main.
	sha := strings.TrimSpace(l.gitIn(wt, "rev-parse", "HEAD"))
	l.gitIn(wt, "checkout", sha)

	prompt := &scriptedPrompt{replies: []bool{true}}
	f, h, out := l.newFlow(map[string][]github.PR{}, prompt, Options{})
	code, res, err := f.Run(testRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	if len(prompt.asked) != 1 || !strings.Contains(prompt.asked[0], "detached HEAD at") {
		t.Fatalf("expected one detached prompt, got %v", prompt.asked)
	}
	if dirExists(wt) {
		t.Errorf("consented detached worktree should have been removed")
	}
	if len(res.Removed) != 1 || res.Removed[0].Disposition != Detached {
		t.Errorf("expected exactly the detached worktree removed, got %+v", res.Removed)
	}
	if !strings.Contains(out.String(), "Detached HEAD — asked individually:") {
		t.Errorf("report must group detached worktrees:\n%s", out.String())
	}
	if forceUsed(h.Calls) {
		t.Errorf("no --force")
	}
}

func TestDetachedDeclinedKept(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-detach")
	sha := strings.TrimSpace(l.gitIn(wt, "rev-parse", "HEAD"))
	l.gitIn(wt, "checkout", sha)

	f, _, _ := l.newFlow(map[string][]github.PR{}, alwaysNo{}, Options{})
	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("declined detached worktree must be kept")
	}
	if len(res.Removed) != 0 {
		t.Errorf("nothing should be removed, got %+v", res.Removed)
	}
}

// A detached worktree with commits not on any remote is skipped as unpushed,
// never prompted.
func TestDetachedUnpushedSkipped(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-detach")
	sha := strings.TrimSpace(l.gitIn(wt, "rev-parse", "HEAD"))
	l.gitIn(wt, "checkout", sha)
	l.commitIn(wt, "local.txt", "never pushed", "local work on detached HEAD")

	f, _, _ := l.newFlow(map[string][]github.PR{}, alwaysYes{}, Options{})
	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("unpushed detached worktree must not be deleted")
	}
	assertSkipped(t, res, wt, "unpushed commits")
}

func TestCriterionC_OpenPRSkip(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-open")
	l.pushBranch(wt, "feat-open")

	prs := map[string][]github.PR{"feat-open": {{Number: 50, State: github.Open, Owner: "mdedys"}}}
	f, _, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})

	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("open-PR worktree must not be deleted")
	}
	assertSkippedReasonContains(t, res, wt, "still open")
	if !strings.Contains(out.String(), "#50 still open") {
		t.Errorf("report must mention open PR:\n%s", out.String())
	}
}

func TestCriterionC_NoPRSkip(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-nopr")
	l.pushBranch(wt, "feat-nopr")

	prs := map[string][]github.PR{"feat-nopr": {}}
	f, _, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})

	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(wt) {
		t.Fatalf("no-PR worktree must not be deleted")
	}
	assertSkipped(t, res, wt, "no PR")
	if !strings.Contains(out.String(), "no PR") {
		t.Errorf("report must mention no PR:\n%s", out.String())
	}
}

// Criterion (c): branch refs are unchanged after a run.
func TestCriterionC_BranchRefsUnchanged(t *testing.T) {
	l := newLab(t)
	merged := l.addWorktree("feat-refkeep")
	l.pushBranch(merged, "feat-refkeep")

	before := strings.TrimSpace(l.git("rev-parse", "refs/heads/feat-refkeep"))

	prs := map[string][]github.PR{"feat-refkeep": {{Number: 60, State: github.Merged, Owner: "mdedys"}}}
	f, _, _ := l.newFlow(prs, alwaysYes{}, Options{Yes: true})
	if _, _, err := f.Run(testRepo); err != nil {
		t.Fatal(err)
	}
	if dirExists(merged) {
		t.Fatalf("expected worktree removed")
	}
	after := strings.TrimSpace(l.git("rev-parse", "refs/heads/feat-refkeep"))
	if before != after {
		t.Errorf("branch ref changed: %s -> %s", before, after)
	}
}

// Criterion (c): no `git worktree remove` invocation ever includes --force,
// across a run that removes a worktree.
func TestCriterionC_NeverForce(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-noforce")
	l.pushBranch(wt, "feat-noforce")

	prs := map[string][]github.PR{"feat-noforce": {{Number: 70, State: github.Merged, Owner: "mdedys"}}}
	f, h, _ := l.newFlow(prs, alwaysYes{}, Options{Yes: true})
	if _, _, err := f.Run(testRepo); err != nil {
		t.Fatal(err)
	}
	if forceUsed(h.Calls) {
		t.Fatalf("a removal used --force")
	}
	// Confirm at least one remove was actually issued.
	sawRemove := false
	for _, c := range h.Calls {
		if c.Name == "git" && len(c.Args) >= 2 && c.Args[0] == "worktree" && c.Args[1] == "remove" {
			sawRemove = true
		}
	}
	if !sawRemove {
		t.Fatalf("expected a worktree remove invocation")
	}
}

// Criterion (d): closed-PR prompts come before the merged batch, and closed
// PRs are never deleted under --yes.
func TestCriterionD_ClosedPromptsBeforeMergedBatch(t *testing.T) {
	l := newLab(t)
	closed := l.addWorktree("feat-closed")
	l.pushBranch(closed, "feat-closed")
	merged := l.addWorktree("feat-merged")
	l.pushBranch(merged, "feat-merged")

	prs := map[string][]github.PR{
		"feat-closed": {{Number: 12, State: github.Closed, Owner: "mdedys"}},
		"feat-merged": {{Number: 41, State: github.Merged, Owner: "mdedys"}},
	}
	prompt := &scriptedPrompt{replies: []bool{false, false}} // decline both
	f, _, _ := l.newFlow(prs, prompt, Options{})
	if _, _, err := f.Run(testRepo); err != nil {
		t.Fatal(err)
	}

	if len(prompt.asked) < 2 {
		t.Fatalf("expected a closed prompt and a merged-batch prompt, got %v", prompt.asked)
	}
	if !strings.Contains(prompt.asked[0], "closed without merging") {
		t.Errorf("first prompt should be the closed-PR prompt, got %q", prompt.asked[0])
	}
	if !strings.Contains(prompt.asked[len(prompt.asked)-1], "Delete these") {
		t.Errorf("merged-batch prompt should come last, got %q", prompt.asked[len(prompt.asked)-1])
	}
}

func TestCriterionD_ClosedNeverDeletedUnderYes(t *testing.T) {
	l := newLab(t)
	closed := l.addWorktree("feat-closed")
	l.pushBranch(closed, "feat-closed")

	prs := map[string][]github.PR{"feat-closed": {{Number: 12, State: github.Closed, Owner: "mdedys"}}}
	// alwaysYes would delete anything prompted — proving --yes must not prompt.
	f, _, out := l.newFlow(prs, alwaysYes{}, Options{Yes: true})
	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(closed) {
		t.Fatalf("closed-PR worktree must never be deleted under --yes")
	}
	for _, r := range res.Removed {
		if r.Worktree.Branch == "feat-closed" {
			t.Fatalf("closed PR was deleted under --yes")
		}
	}
	assertSkippedReasonContains(t, res, closed, "closed PR, skipped")
	_ = out
}

// Criterion (e): prunable entries are cleared automatically and reported.
func TestCriterionE_PrunableClearedAndReported(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-prune")
	l.pushBranch(wt, "feat-prune")
	// Delete the directory out from under git to make the entry prunable.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	f, h, out := l.newFlow(map[string][]github.PR{}, alwaysNo{}, Options{})
	_, res, err := f.Run(testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if res.ClearedPrunable != 1 {
		t.Errorf("expected 1 prunable cleared, got %d\n%s", res.ClearedPrunable, out.String())
	}
	if !strings.Contains(out.String(), "Cleared 1 stale worktree entry") {
		t.Errorf("report must mention cleared prunable:\n%s", out.String())
	}
	// The stale entry should be gone from git's bookkeeping.
	list := l.git("worktree", "list", "--porcelain")
	if strings.Contains(list, "feat-prune") {
		t.Errorf("prunable entry not cleared:\n%s", list)
	}
	if forceUsed(h.Calls) {
		t.Errorf("no --force")
	}
}

// Criterion (e): --dry-run performs zero writes.
func TestCriterionE_DryRunNoWrites(t *testing.T) {
	l := newLab(t)
	merged := l.addWorktree("feat-merged")
	l.pushBranch(merged, "feat-merged")
	prune := l.addWorktree("feat-prune")
	l.pushBranch(prune, "feat-prune")
	if err := os.RemoveAll(prune); err != nil {
		t.Fatal(err)
	}

	prs := map[string][]github.PR{
		"feat-merged": {{Number: 41, State: github.Merged, Owner: "mdedys"}},
	}
	f, h, out := l.newFlow(prs, alwaysYes{}, Options{DryRun: true, Yes: true})
	code, res, err := f.Run(testRepo)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !dirExists(merged) {
		t.Errorf("--dry-run must not remove the merged worktree")
	}
	if len(res.Removed) != 0 {
		t.Errorf("--dry-run removed %d worktrees", len(res.Removed))
	}
	// No mutating git commands may have been issued.
	for _, c := range h.Calls {
		if c.Name == "git" && len(c.Args) >= 2 && c.Args[0] == "worktree" && c.Args[1] == "remove" {
			t.Errorf("--dry-run issued a mutating command: %s", c.Line())
		}
	}
	// The report is still printed and lists the merged candidate.
	if !strings.Contains(out.String(), "Deleting (merged)") {
		t.Errorf("dry-run should still print classification:\n%s", out.String())
	}
}

// Criterion (f): report header contains the resolved owner/repo.
func TestCriterionF_HeaderContainsRepo(t *testing.T) {
	l := newLab(t)
	f, _, out := l.newFlow(map[string][]github.PR{}, alwaysNo{}, Options{})
	if _, _, err := f.Run(testRepo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mdedys/gitanitor") {
		t.Errorf("header must contain owner/repo:\n%s", out.String())
	}
}

// Criterion (f): a gh failure exits 1 with no removals performed.
func TestCriterionF_GHFailureExits1NoRemovals(t *testing.T) {
	l := newLab(t)
	wt := l.addWorktree("feat-x")
	l.pushBranch(wt, "feat-x")

	h := &xexec.Hybrid{
		Git: gitRunner{env: l.env(), dir: l.dir},
		GH: func(args ...string) xexec.Result {
			return xexec.Result{Stderr: "HTTP 401: Bad credentials", ExitCode: 1}
		},
	}
	out := &bytes.Buffer{}
	f := &Flow{Exec: h, Prompt: alwaysYes{}, Out: out, Opts: Options{Yes: true}}
	code, res, err := f.Run(testRepo)
	if err == nil {
		t.Fatalf("expected error from gh failure")
	}
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if len(res.Removed) != 0 {
		t.Errorf("no removals should happen on gh failure")
	}
	if !dirExists(wt) {
		t.Errorf("worktree must not be removed on gh failure")
	}
	if !strings.Contains(out.String(), "Bad credentials") {
		t.Errorf("gh stderr should be relayed:\n%s", out.String())
	}
}

func assertSkipped(t *testing.T, res Result, path, reason string) {
	t.Helper()
	for _, s := range res.Skipped {
		if s.Worktree.Path == path && s.Reason == reason {
			return
		}
	}
	t.Errorf("expected %s skipped with reason %q, got %+v", path, reason, res.Skipped)
}

func assertSkippedReasonContains(t *testing.T, res Result, path, substr string) {
	t.Helper()
	for _, s := range res.Skipped {
		if s.Worktree.Path == path && strings.Contains(s.Reason, substr) {
			return
		}
	}
	t.Errorf("expected %s skipped with reason containing %q, got %+v", path, substr, res.Skipped)
}
