package worktree

import (
	"testing"

	"github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

// cannedGit routes faked git status/branch responses by worktree path.
func cannedGit(status, contains map[string]string) func(name string, args ...string) exec.Result {
	return func(name string, args ...string) exec.Result {
		// git -C <path> status --porcelain=v2 --branch
		if len(args) >= 5 && args[0] == "-C" && args[2] == "status" {
			return exec.Result{Stdout: status[args[1]]}
		}
		// git -C <path> branch -r --contains HEAD
		if len(args) >= 5 && args[0] == "-C" && args[2] == "branch" {
			return exec.Result{Stdout: contains[args[1]]}
		}
		return exec.Result{}
	}
}

func TestClassifyLocal_DirtyDetectedFromStatusV2(t *testing.T) {
	f := Flow{Exec: &exec.Fake{Responder: cannedGit(
		map[string]string{
			"/wt/clean": "# branch.oid abc\n# branch.head feat\n# branch.upstream origin/feat\n# branch.ab +0 -0\n",
			"/wt/dirty": "# branch.oid abc\n# branch.head feat\n1 .M N... 100644 100644 100644 aaa bbb file.go\n",
		}, nil)}}

	worktrees := []Worktree{
		{Path: "/wt/main", IsMain: true},
		{Path: "/wt/clean", Branch: "clean"},
		{Path: "/wt/dirty", Branch: "dirty"},
	}
	_, _, candidates, skipped, err := f.classifyLocal(worktrees)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Branch != "clean" {
		t.Errorf("clean branch should be a candidate, got %+v", candidates)
	}
	if len(skipped) != 1 || skipped[0].Reason != "uncommitted changes" {
		t.Errorf("dirty branch should skip with reason, got %+v", skipped)
	}
}

func TestClassifyLocal_UnpushedAhead(t *testing.T) {
	f := Flow{Exec: &exec.Fake{Responder: cannedGit(
		map[string]string{
			"/wt/ahead": "# branch.oid abc\n# branch.head feat\n# branch.upstream origin/feat\n# branch.ab +2 -0\n",
		}, nil)}}
	_, _, candidates, skipped, err := f.classifyLocal([]Worktree{
		{Path: "/wt/main", IsMain: true},
		{Path: "/wt/ahead", Branch: "ahead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("ahead branch must not be a candidate")
	}
	if len(skipped) != 1 || skipped[0].Reason != "unpushed commits" {
		t.Errorf("expected unpushed skip, got %+v", skipped)
	}
}

// Gone-upstream fallback: branch.ab absent + empty remote-contains ⇒ unpushed.
func TestClassifyLocal_GoneUpstreamFallbackUnpushed(t *testing.T) {
	f := Flow{Exec: &exec.Fake{Responder: cannedGit(
		map[string]string{
			"/wt/gone": "# branch.oid abc\n# branch.head feat\n",
		},
		map[string]string{
			"/wt/gone": "", // no remote ref contains HEAD ⇒ local-only
		})}}
	_, _, candidates, skipped, err := f.classifyLocal([]Worktree{
		{Path: "/wt/main", IsMain: true},
		{Path: "/wt/gone", Branch: "gone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("local-only branch must not be a candidate")
	}
	if len(skipped) != 1 || skipped[0].Reason != "unpushed commits" {
		t.Errorf("expected unpushed skip via fallback, got %+v", skipped)
	}
}

// Gone-upstream but HEAD is on a remote ref (delete-on-merge) ⇒ candidate.
func TestClassifyLocal_GoneUpstreamButOnRemoteIsCandidate(t *testing.T) {
	f := Flow{Exec: &exec.Fake{Responder: cannedGit(
		map[string]string{
			"/wt/merged": "# branch.oid abc\n# branch.head feat\n",
		},
		map[string]string{
			"/wt/merged": "  origin/main\n", // HEAD reachable from a remote ref
		})}}
	_, _, candidates, skipped, err := f.classifyLocal([]Worktree{
		{Path: "/wt/main", IsMain: true},
		{Path: "/wt/merged", Branch: "merged"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Errorf("delete-on-merge branch should be a candidate, got %+v skipped=%+v", candidates, skipped)
	}
}

// Detached worktrees: clean and pushed ⇒ per-worktree prompt candidate; the
// dirty and unpushed safety checks still win; locked wins over detached.
func TestClassifyLocal_Detached(t *testing.T) {
	f := Flow{Exec: &exec.Fake{Responder: cannedGit(
		map[string]string{
			"/wt/det-clean": "# branch.oid abc\n# branch.head (detached)\n",
			"/wt/det-dirty": "# branch.oid abc\n# branch.head (detached)\n1 .M N... 100644 100644 100644 aaa bbb file.go\n",
			"/wt/det-local": "# branch.oid abc\n# branch.head (detached)\n",
		},
		map[string]string{
			"/wt/det-clean": "  origin/main\n", // HEAD reachable from a remote ref
			"/wt/det-local": "",                // local-only commit
		})}}

	worktrees := []Worktree{
		{Path: "/wt/main", IsMain: true},
		{Path: "/wt/det-clean", Detached: true},
		{Path: "/wt/det-dirty", Detached: true},
		{Path: "/wt/det-local", Detached: true},
		{Path: "/wt/det-locked", Detached: true, Locked: true, LockReason: "usb drive"},
	}
	_, detached, candidates, skipped, err := f.classifyLocal(worktrees)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Errorf("detached worktrees never join the PR lookup, got %+v", candidates)
	}
	if len(detached) != 1 || detached[0].Worktree.Path != "/wt/det-clean" {
		t.Errorf("clean pushed detached worktree should be a prompt candidate, got %+v", detached)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Worktree.Path] = s.Reason
	}
	if reasons["/wt/det-dirty"] != "uncommitted changes" {
		t.Errorf("dirty detached reason = %q", reasons["/wt/det-dirty"])
	}
	if reasons["/wt/det-local"] != "unpushed commits" {
		t.Errorf("local-only detached reason = %q", reasons["/wt/det-local"])
	}
	if reasons["/wt/det-locked"] != "locked: usb drive" {
		t.Errorf("locked detached reason = %q", reasons["/wt/det-locked"])
	}
}

func TestClassifyByPR_Precedence(t *testing.T) {
	candidates := []Worktree{
		{Path: "/wt/open", Branch: "open"},
		{Path: "/wt/merged", Branch: "merged"},
		{Path: "/wt/closed", Branch: "closed"},
		{Path: "/wt/nopr", Branch: "nopr"},
		{Path: "/wt/mixed", Branch: "mixed"},
	}
	prs := map[string][]github.PR{
		"open":   {{Number: 1, State: github.Open, Owner: "mdedys"}},
		"merged": {{Number: 2, State: github.Merged, Owner: "mdedys"}},
		"closed": {{Number: 3, State: github.Closed, Owner: "mdedys"}},
		"nopr":   {},
		// open wins over merged and closed
		"mixed": {
			{Number: 4, State: github.Closed, Owner: "mdedys"},
			{Number: 5, State: github.Merged, Owner: "mdedys"},
			{Number: 6, State: github.Open, Owner: "mdedys"},
		},
	}
	merged, closed, skipped := classifyByPR(candidates, prs, "mdedys")

	if len(merged) != 1 || merged[0].Worktree.Branch != "merged" {
		t.Errorf("expected only 'merged' as merged, got %+v", merged)
	}
	if len(closed) != 1 || closed[0].Worktree.Branch != "closed" {
		t.Errorf("expected only 'closed' as closed, got %+v", closed)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Worktree.Branch] = s.Reason
	}
	if reasons["open"] != "PR #1 still open" {
		t.Errorf("open reason = %q", reasons["open"])
	}
	if reasons["nopr"] != "no PR" {
		t.Errorf("nopr reason = %q", reasons["nopr"])
	}
	if reasons["mixed"] != "PR #6 still open" {
		t.Errorf("mixed should be open (precedence), reason = %q", reasons["mixed"])
	}
}

// Fork-collision guard: a same-named branch PR from a stranger's fork is
// discarded, leaving the branch classified as no-PR.
func TestClassifyByPR_ForkGuard(t *testing.T) {
	candidates := []Worktree{{Path: "/wt/x", Branch: "feat"}}
	prs := map[string][]github.PR{
		"feat": {{Number: 99, State: github.Merged, Owner: "stranger"}},
	}
	merged, _, skipped := classifyByPR(candidates, prs, "mdedys")
	if len(merged) != 0 {
		t.Errorf("fork PR must be discarded, got merged %+v", merged)
	}
	if len(skipped) != 1 || skipped[0].Reason != "no PR" {
		t.Errorf("expected no-PR skip after fork guard, got %+v", skipped)
	}
}
