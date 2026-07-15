package branch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	xexec "github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
)

var branchTestRepo = github.Repo{Owner: "acme", Name: "widget", DefaultBranch: "main"}

type branchPRs map[string][]github.PR

type branchRunner struct {
	refs          []Ref
	worktrees     string
	prs           branchPRs
	unique        map[string]int
	ancestor      map[string]bool
	headLocal     map[string]bool
	revalidate    map[string]string
	deleteErr     map[string]string
	graphqlErr    string
	revListErr    string
	compareStatus string
	compareErr    string
	listErr       string
	worktreeErr   string
	base          *xexec.Fake
}

func newBranchRunner(refs []Ref, worktrees string, prs branchPRs) *branchRunner {
	b := &branchRunner{
		refs: refs, worktrees: worktrees, prs: prs,
		unique: map[string]int{}, ancestor: map[string]bool{}, headLocal: map[string]bool{},
		revalidate: map[string]string{}, deleteErr: map[string]string{},
	}
	b.base = &xexec.Fake{Responder: b.respond}
	return b
}

func (b *branchRunner) Run(name string, args ...string) xexec.Result {
	return b.base.Run(name, args...)
}

func (b *branchRunner) respond(name string, args ...string) xexec.Result {
	if name == "git" && len(args) > 0 {
		switch args[0] {
		case "for-each-ref":
			if b.listErr != "" {
				return xexec.Result{ExitCode: 1, Stderr: b.listErr}
			}
			var out strings.Builder
			for _, ref := range b.refs {
				fmt.Fprintf(&out, "%s\x00%s\n", ref.Name, ref.OID)
			}
			return xexec.Result{Stdout: out.String()}
		case "rev-parse":
			name := strings.TrimPrefix(args[len(args)-1], "refs/heads/")
			if oid, ok := b.revalidate[name]; ok {
				return xexec.Result{Stdout: oid + "\n"}
			}
			for _, ref := range b.refs {
				if ref.Name == name {
					return xexec.Result{Stdout: ref.OID + "\n"}
				}
			}
			return xexec.Result{ExitCode: 1, Stderr: "unknown ref"}
		case "worktree":
			if b.worktreeErr != "" {
				return xexec.Result{ExitCode: 1, Stderr: b.worktreeErr}
			}
			return xexec.Result{Stdout: b.worktrees}
		case "rev-list":
			if b.revListErr != "" {
				return xexec.Result{ExitCode: 1, Stderr: b.revListErr}
			}
			tip := ""
			for _, arg := range args[1:] {
				if !strings.HasPrefix(arg, "-") {
					tip = arg
					break
				}
			}
			return xexec.Result{Stdout: fmt.Sprintf("%d\n", b.unique[tip])}
		case "cat-file":
			head := strings.TrimSuffix(args[len(args)-1], "^{commit}")
			if len(b.headLocal) == 0 || b.headLocal[head] {
				return xexec.Result{}
			}
			return xexec.Result{ExitCode: 1, Stderr: "missing object"}
		case "merge-base":
			head := args[len(args)-1]
			if b.ancestor[head] {
				return xexec.Result{}
			}
			return xexec.Result{ExitCode: 1}
		case "branch":
			name := args[len(args)-1]
			if message := b.deleteErr[name]; message != "" {
				return xexec.Result{ExitCode: 1, Stderr: message}
			}
			return xexec.Result{}
		}
	}
	if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		if b.graphqlErr != "" {
			return xexec.Result{ExitCode: 1, Stderr: b.graphqlErr}
		}
		return b.graphql(args)
	}
	if name == "gh" && len(args) >= 2 && args[0] == "api" && strings.HasPrefix(args[1], "repos/") {
		if b.compareErr != "" {
			return xexec.Result{ExitCode: 1, Stderr: b.compareErr}
		}
		return xexec.Result{Stdout: b.compareStatus + "\n"}
	}
	return xexec.Result{}
}

func (b *branchRunner) graphql(args []string) xexec.Result {
	aliasBranch := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-f" {
			continue
		}
		key, value, ok := strings.Cut(args[i+1], "=")
		if ok && strings.HasPrefix(key, "b") {
			aliasBranch[key] = value
		}
	}
	repository := map[string]any{}
	for alias, branch := range aliasBranch {
		nodes := []any{}
		for _, pr := range b.prs[branch] {
			nodes = append(nodes, map[string]any{
				"number": pr.Number, "state": string(pr.State), "url": pr.URL,
				"headRefOid":          pr.HeadOID,
				"headRepositoryOwner": map[string]any{"login": pr.Owner},
			})
		}
		repository[alias] = map[string]any{"nodes": nodes}
	}
	payload, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": repository}})
	return xexec.Result{Stdout: string(payload)}
}

func branchRefs(names ...string) []Ref {
	refs := make([]Ref, 0, len(names))
	for _, name := range names {
		refs = append(refs, Ref{Name: name, OID: name + "-tip"})
	}
	return refs
}

func mainWorktree() string {
	return "worktree /repo/main\nHEAD main-tip\nbranch refs/heads/main\n\n"
}

func namesOf(cs []Candidate) []string {
	got := make([]string, 0, len(cs))
	for _, c := range cs {
		got = append(got, c.Branch.Name)
	}
	return got
}

func hasName(cs []Candidate, name string) bool {
	for _, c := range cs {
		if c.Branch.Name == name {
			return true
		}
	}
	return false
}

func TestCriterionB_HardProtectionsAndForkOwner(t *testing.T) {
	refs := branchRefs("main", "checked", "open", "foreign", "none", "safe")
	worktrees := mainWorktree() + "worktree /repo/checked\nHEAD checked-tip\nbranch refs/heads/checked\n\n"
	prs := branchPRs{
		"open":    {{Number: 10, State: github.Open, Owner: "acme", HeadOID: "open-tip"}},
		"foreign": {{Number: 11, State: github.Merged, Owner: "other", HeadOID: "foreign-tip"}},
		"safe":    {{Number: 12, State: github.Merged, Owner: "acme", HeadOID: "safe-tip"}},
	}
	fake := newBranchRunner(refs, worktrees, prs)
	flow := Flow{Exec: fake, Prompt: noPrompt{}, Out: new(strings.Builder), Opts: Options{DryRun: true, Yes: true}}
	code, result, err := flow.Run(branchTestRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	for _, name := range []string{"main", "checked", "open", "foreign", "none"} {
		if !hasName(result.Skipped, name) {
			t.Errorf("%s must be hard-skipped; skipped=%v", name, namesOf(result.Skipped))
		}
	}
	if !hasName(result.Merged, "safe") {
		t.Fatalf("safe branch should be classified as merged: %+v", result)
	}
	if reason := reasonFor(result.Skipped, "checked"); !strings.Contains(reason, "/repo/checked") {
		t.Errorf("checked-out reason must include path, got %q", reason)
	}
}

func TestCriterionC_OnlyCurrentTipCoveredMergedPRsEnterSafeBatch(t *testing.T) {
	refs := branchRefs("main", "exact", "ancestor", "squash", "nondefault", "reused", "multiple")
	prs := branchPRs{
		"exact":      {{Number: 20, State: github.Merged, Owner: "acme", HeadOID: "exact-tip"}},
		"ancestor":   {{Number: 21, State: github.Merged, Owner: "acme", HeadOID: "ancestor-head"}},
		"squash":     {{Number: 22, State: github.Merged, Owner: "acme", HeadOID: "squash-tip"}},
		"nondefault": {{Number: 23, State: github.Merged, Owner: "acme", HeadOID: "nondefault-tip"}},
		"reused":     {{Number: 24, State: github.Merged, Owner: "acme", HeadOID: "old-head"}},
		"multiple": {
			{Number: 25, State: github.Merged, Owner: "acme", HeadOID: "old-multiple-head"},
			{Number: 26, State: github.Merged, Owner: "acme", HeadOID: "new-multiple-head"},
		},
	}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	fake.ancestor["ancestor-head"] = true
	fake.ancestor["new-multiple-head"] = true
	fake.unique["reused-tip"] = 0
	flow := Flow{Exec: fake, Prompt: noPrompt{}, Out: new(strings.Builder), Opts: Options{DryRun: true}}
	code, result, err := flow.Run(branchTestRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	for _, name := range []string{"exact", "ancestor", "squash", "nondefault", "multiple"} {
		if !hasName(result.Merged, name) {
			t.Errorf("%s should enter safe merged batch; merged=%v", name, namesOf(result.Merged))
		}
	}
	if hasName(result.Merged, "reused") {
		t.Errorf("reused branch with uncovered tip must not be safe")
	}
	if !strings.Contains(reasonFor(result.Skipped, "reused"), "not covered") {
		t.Errorf("reused branch should explain uncovered current tip, got %q", reasonFor(result.Skipped, "reused"))
	}
}

func TestCriterionC_MissingLocalPRHeadUsesGitHubComparisonMetadata(t *testing.T) {
	refs := []Ref{{Name: "main", OID: "main-tip"}, {Name: "stale", OID: "stale-tip"}}
	prs := branchPRs{"stale": {{Number: 27, State: github.Merged, Owner: "acme", HeadOID: "remote-head"}}}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	fake.headLocal = map[string]bool{"remote-head": false}
	fake.compareStatus = "ahead"
	flow := Flow{Exec: fake, Prompt: noPrompt{}, Out: new(strings.Builder), Opts: Options{DryRun: true}}
	code, result, err := flow.Run(branchTestRepo)
	if err != nil || code != 0 || !hasName(result.Merged, "stale") {
		t.Fatalf("comparison-backed coverage failed: code=%d err=%v result=%+v", code, err, result)
	}
	found := false
	for _, call := range fake.base.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 && strings.HasPrefix(call.Args[1], "repos/acme/widget/compare/stale-tip...remote-head") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected GitHub comparison invocation, calls=%v", fake.base.Calls)
	}
}

func TestCriterionD_ClosedAndLocallyUniquePromptCandidatesSkipNewerPushedWork(t *testing.T) {
	refs := branchRefs("main", "closed", "local", "closed-local", "pushed")
	prs := branchPRs{
		"closed":       {{Number: 30, State: github.Closed, Owner: "acme", HeadOID: "closed-tip"}},
		"local":        {{Number: 31, State: github.Merged, Owner: "acme", HeadOID: "old-local-head"}},
		"closed-local": {{Number: 32, State: github.Closed, Owner: "acme", HeadOID: "old-closed-head"}},
		"pushed":       {{Number: 33, State: github.Merged, Owner: "acme", HeadOID: "old-pushed-head"}},
	}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	fake.unique["local-tip"] = 2
	fake.unique["closed-local-tip"] = 3
	fake.unique["pushed-tip"] = 0
	prompt := &recordingPrompt{}
	flow := Flow{Exec: fake, Prompt: prompt, Out: new(strings.Builder), Opts: Options{}}
	code, _, err := flow.Run(branchTestRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	if len(prompt.questions) != 3 {
		t.Fatalf("expected closed plus two local-only prompts, got %v", prompt.questions)
	}
	joined := strings.Join(prompt.questions, "\n")
	for _, want := range []string{"PR #30 closed", "PR #31 merged", "PR #32 closed", "2 unpushed commits", "3 unpushed commits"} {
		if !strings.Contains(joined, want) {
			t.Errorf("prompt output missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "#33") {
		t.Errorf("newer pushed work must not be prompted: %v", prompt.questions)
	}
}

func TestCriterionE_DefaultPromptsJudgmentCallsThenOneMergedBatch(t *testing.T) {
	refs := branchRefs("main", "closed", "safe")
	prs := branchPRs{
		"closed": {{Number: 40, State: github.Closed, Owner: "acme", HeadOID: "closed-tip"}},
		"safe":   {{Number: 41, State: github.Merged, Owner: "acme", HeadOID: "safe-tip"}},
	}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	prompt := &recordingPrompt{}
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: prompt, Out: out}).Run(branchTestRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	if len(prompt.questions) != 2 || !strings.Contains(prompt.questions[0], "#40") || !strings.Contains(prompt.questions[1], "these 1 branches") {
		t.Fatalf("expected individual then batch prompts, got %v", prompt.questions)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("default-No prompts must keep branches, removed=%v", result.Removed)
	}
	for _, want := range []string{"closed", "safe", "Safe merged batch", "Skipped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report missing %q:\n%s", want, out.String())
		}
	}
}

func TestCriterionE_YesSkipsJudgmentCallsAndDeletesOnlyMergedBatch(t *testing.T) {
	refs := branchRefs("main", "closed", "safe")
	prs := branchPRs{
		"closed": {{Number: 42, State: github.Closed, Owner: "acme", HeadOID: "closed-tip"}},
		"safe":   {{Number: 43, State: github.Merged, Owner: "acme", HeadOID: "safe-tip"}},
	}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	prompt := &recordingPrompt{}
	code, result, err := (Flow{Exec: fake, Prompt: prompt, Out: new(strings.Builder), Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	if len(prompt.questions) != 0 {
		t.Fatalf("--yes must not ask judgment calls, got %v", prompt.questions)
	}
	if !hasName(result.Removed, "safe") || hasName(result.Removed, "closed") {
		t.Fatalf("--yes removed wrong branches: %+v", result.Removed)
	}
	if !hasName(result.Skipped, "closed") {
		t.Fatalf("judgment candidate must be reported skipped under --yes: %+v", result.Skipped)
	}
}

func TestCriterionF_RevalidationStopsMovedBranchWithoutFetchOrPrune(t *testing.T) {
	refs := branchRefs("main", "safe")
	prs := branchPRs{"safe": {{Number: 50, State: github.Merged, Owner: "acme", HeadOID: "safe-tip"}}}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	fake.revalidate["safe"] = "moved-tip"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 1 || len(result.Failed) != 1 || len(result.Removed) != 0 {
		t.Fatalf("moved branch must fail without deletion: code=%d result=%+v", code, result)
	}
	if !strings.Contains(out.String(), "branch changed during cleanup") {
		t.Fatalf("missing race reason:\n%s", out.String())
	}
	for _, call := range fake.base.Calls {
		if call.Name != "git" {
			continue
		}
		for _, arg := range call.Args {
			if arg == "fetch" || arg == "prune" || arg == "--prune" {
				t.Fatalf("cleanup must not fetch or prune: %v", call)
			}
		}
	}
}

func TestCriterionG_ClassificationFailureIsMutationAtomic(t *testing.T) {
	refs := branchRefs("main", "safe")
	fake := newBranchRunner(refs, mainWorktree(), nil)
	fake.graphqlErr = "GitHub unavailable"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err == nil || code != 1 || len(result.Removed) != 0 {
		t.Fatalf("classification failure must abort before mutation: code=%d result=%+v err=%v", code, result, err)
	}
	if !strings.Contains(out.String(), "GitHub unavailable") {
		t.Fatalf("classification stderr must be relayed:\n%s", out.String())
	}
	for _, call := range fake.base.Calls {
		if call.Name == "git" && len(call.Args) > 0 && call.Args[0] == "branch" {
			t.Fatalf("no deletion after classification failure: %v", call)
		}
	}
}

func TestCriterionG_EnumerationFailureAbortsBeforeMutation(t *testing.T) {
	fake := newBranchRunner(branchRefs("main", "safe"), mainWorktree(), nil)
	fake.listErr = "cannot enumerate refs"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err == nil || code != 1 || len(result.Removed) != 0 || !strings.Contains(out.String(), "cannot enumerate refs") {
		t.Fatalf("enumeration failure behavior: code=%d err=%v result=%+v output=%s", code, err, result, out.String())
	}
}

func TestCriterionG_AncestryComparisonFailureSkipsFailedBranch(t *testing.T) {
	refs := branchRefs("main", "safe")
	fake := newBranchRunner(refs, mainWorktree(), branchPRs{
		"safe": {{Number: 55, State: github.Merged, Owner: "acme", HeadOID: "remote-head"}},
	})
	fake.headLocal = map[string]bool{"remote-head": false}
	fake.compareErr = "comparison unavailable"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err == nil || code != 1 || len(result.Removed) != 0 || !strings.Contains(out.String(), "comparison unavailable") {
		t.Fatalf("comparison failure behavior: code=%d err=%v result=%+v output=%s", code, err, result, out.String())
	}
}

func TestCriterionG_ComparisonFailureReportsCompleteScanAndContinues(t *testing.T) {
	refs := branchRefs("main", "safe", "later-safe", "untouched")
	fake := newBranchRunner(refs, mainWorktree(), branchPRs{
		"safe":       {{Number: 56, State: github.Merged, Owner: "acme", HeadOID: "remote-head"}},
		"later-safe": {{Number: 57, State: github.Merged, Owner: "acme", HeadOID: "later-safe-tip"}},
	})
	fake.headLocal = map[string]bool{"remote-head": false}
	fake.compareErr = "gh: Not Found (HTTP 404)"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err == nil || code != 1 {
		t.Fatalf("comparison failure must still exit 1: code=%d err=%v", code, err)
	}
	for _, want := range []string{"Scanned 4 local branches", "safe", "later-safe", "untouched", "Deleting (merged)", "classification failed", "Classification failures detected", "HTTP 404"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("failure report missing %q:\n%s", want, out.String())
		}
	}
	if !hasName(result.Removed, "later-safe") || hasName(result.Removed, "safe") {
		t.Fatalf("only fully classified branches may mutate: %+v", result.Removed)
	}
	deletedLaterSafe := false
	for _, call := range fake.base.Calls {
		if call.Name == "git" && len(call.Args) > 0 && call.Args[0] == "branch" && call.Args[len(call.Args)-1] == "safe" {
			t.Fatalf("classification failure must not delete: %v", call)
		}
		if call.Name == "git" && len(call.Args) > 0 && call.Args[0] == "branch" && call.Args[len(call.Args)-1] == "later-safe" {
			deletedLaterSafe = true
		}
	}
	if !deletedLaterSafe {
		t.Fatal("fully classified later branch should still be attempted")
	}
}

func TestCriterionG_ComparisonFailureDoesNotBlockClassifiedBranchConsent(t *testing.T) {
	refs := branchRefs("main", "failed", "safe")
	fake := newBranchRunner(refs, mainWorktree(), branchPRs{
		"failed": {{Number: 58, State: github.Merged, Owner: "acme", HeadOID: "remote-head"}},
		"safe":   {{Number: 59, State: github.Merged, Owner: "acme", HeadOID: "safe-tip"}},
	})
	fake.headLocal = map[string]bool{"remote-head": false}
	fake.compareErr = "gh: Not Found (HTTP 404)"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err == nil || code != 1 {
		t.Fatalf("classification failure must remain visible in exit status: code=%d err=%v", code, err)
	}
	if !hasName(result.Removed, "safe") || hasName(result.Removed, "failed") {
		t.Fatalf("only fully classified branch should be deleted: %+v", result.Removed)
	}
	if !strings.Contains(out.String(), "Classification failures detected") || !strings.Contains(out.String(), "Deleting (merged)") {
		t.Fatalf("report must explain continued consent:\n%s", out.String())
	}
}

func TestCriterionG_DeletionFailureContinuesAndReturnsOne(t *testing.T) {
	refs := branchRefs("main", "a", "b")
	prs := branchPRs{
		"a": {{Number: 60, State: github.Merged, Owner: "acme", HeadOID: "a-tip"}},
		"b": {{Number: 61, State: github.Merged, Owner: "acme", HeadOID: "b-tip"}},
	}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	fake.deleteErr["a"] = "cannot delete a"
	out := &strings.Builder{}
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: out, Opts: Options{Yes: true}}).Run(branchTestRepo)
	if err != nil || code != 1 {
		t.Fatalf("partial failure: code=%d err=%v result=%+v", code, err, result)
	}
	if !hasName(result.Removed, "b") || len(result.Failed) != 1 || !strings.Contains(out.String(), "cannot delete a") {
		t.Fatalf("remaining deletion/stderr behavior wrong: result=%+v output=%s", result, out.String())
	}
	seen := map[string]bool{}
	for _, call := range fake.base.Calls {
		if call.Name == "git" && len(call.Args) > 0 && call.Args[0] == "branch" {
			seen[call.Args[len(call.Args)-1]] = true
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("remaining approved branches must still be attempted: %v", seen)
	}
}

func TestCriterionG_DeclineAndNoOpExitZero(t *testing.T) {
	refs := branchRefs("main", "safe")
	prs := branchPRs{"safe": {{Number: 70, State: github.Merged, Owner: "acme", HeadOID: "safe-tip"}}}
	fake := newBranchRunner(refs, mainWorktree(), prs)
	code, result, err := (Flow{Exec: fake, Prompt: noPrompt{}, Out: new(strings.Builder)}).Run(branchTestRepo)
	if err != nil || code != 0 || len(result.Removed) != 0 {
		t.Fatalf("declined safe batch must be successful no-op: code=%d err=%v result=%+v", code, err, result)
	}
}

type noPrompt struct{}

func (noPrompt) Confirm(string) bool { return false }

type recordingPrompt struct{ questions []string }

func (p *recordingPrompt) Confirm(question string) bool {
	p.questions = append(p.questions, question)
	return false
}

func reasonFor(cs []Candidate, name string) string {
	for _, c := range cs {
		if c.Branch.Name == name {
			return c.Reason
		}
	}
	return ""
}
