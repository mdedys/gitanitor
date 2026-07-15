package github

import (
	"strings"
	"testing"

	"github.com/mdedys/gitanitor/internal/exec"
)

func TestResolveRepo(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stdout: `{"owner":{"login":"mdedys"},"name":"gitanitor"}`}
	}}
	repo, err := ResolveRepo(f)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Owner != "mdedys" || repo.Name != "gitanitor" {
		t.Errorf("got %+v", repo)
	}
	if repo.String() != "mdedys/gitanitor" {
		t.Errorf("String() = %q", repo.String())
	}
}

func TestCriterionH_ResolveRepoParsesDefaultBranch(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stdout: `{"owner":{"login":"mdedys"},"name":"gitanitor","defaultBranchRef":{"name":"trunk"}}`}
	}}
	repo, err := ResolveRepo(f)
	if err != nil {
		t.Fatal(err)
	}
	if repo.DefaultBranch != "trunk" {
		t.Fatalf("DefaultBranch = %q, want trunk", repo.DefaultBranch)
	}
	if len(f.Calls) != 1 || !containsArgContaining(f.Calls[0].Args, "defaultBranchRef") {
		t.Fatalf("repo lookup must request defaultBranchRef, calls=%+v", f.Calls)
	}
}

func TestResolveRepo_FailureRelaysStderr(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stderr: "no git remotes found", ExitCode: 1}
	}}
	_, err := ResolveRepo(f)
	if err == nil || !strings.Contains(err.Error(), "no git remotes found") {
		t.Fatalf("expected relayed stderr, got %v", err)
	}
}

// Branch names must be passed as GraphQL variables (-f b<i>=<branch>), never
// interpolated into the query text.
func TestLookupPRs_BranchesArePassedAsVariables(t *testing.T) {
	var captured []string
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		captured = args
		return exec.Result{Stdout: `{"data":{"repository":{}}}`}
	}}
	branch := "feat/with\"quote"
	_, err := LookupPRs(f, Repo{Owner: "o", Name: "r"}, []string{branch})
	if err != nil {
		t.Fatal(err)
	}

	var query, varArg string
	for i := 0; i < len(captured)-1; i++ {
		if captured[i] == "-f" && strings.HasPrefix(captured[i+1], "query=") {
			query = captured[i+1]
		}
		if captured[i] == "-f" && strings.HasPrefix(captured[i+1], "b0=") {
			varArg = captured[i+1]
		}
	}
	if strings.Contains(query, branch) {
		t.Errorf("branch name must not be interpolated into query:\n%s", query)
	}
	if varArg != "b0="+branch {
		t.Errorf("branch must be passed as a variable, got %q", varArg)
	}
	if !strings.Contains(query, "$b0: String!") {
		t.Errorf("query must declare a typed variable:\n%s", query)
	}
}

func TestLookupPRs_ParsesNodes(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stdout: `{"data":{"repository":{
			"b0":{"nodes":[{"number":41,"state":"MERGED","url":"http://x/41","headRepositoryOwner":{"login":"mdedys"}}]},
			"b1":{"nodes":[]}
		}}}`}
	}}
	got, err := LookupPRs(f, Repo{Owner: "mdedys", Name: "r"}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	// Aliases map back to the branches in order.
	if len(got["a"]) != 1 || got["a"][0].Number != 41 || got["a"][0].State != Merged {
		t.Errorf("branch a: got %+v", got["a"])
	}
	if got["a"][0].Owner != "mdedys" {
		t.Errorf("owner should be captured for the fork guard, got %+v", got["a"])
	}
	if len(got["b"]) != 0 {
		t.Errorf("unknown branch should map to empty, got %+v", got["b"])
	}
}

func TestCriterionH_LookupPRsParsesRecordedHeadOID(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stdout: `{"data":{"repository":{"b0":{"nodes":[{"number":41,"state":"MERGED","headRefOid":"abc123","headRepositoryOwner":{"login":"mdedys"}}]}}}}`}
	}}
	got, err := LookupPRs(f, Repo{Owner: "mdedys", Name: "r"}, []string{"feat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["feat"]) != 1 || got["feat"][0].HeadOID != "abc123" {
		t.Fatalf("got PRs = %+v", got["feat"])
	}
}

func TestCriterionH_CompareCommitsUsesRunnerAndReportsStatus(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		if name != "gh" {
			t.Fatalf("command = %q, want gh", name)
		}
		return exec.Result{Stdout: "ahead\n"}
	}}
	status, err := CompareCommits(f, Repo{Owner: "mdedys", Name: "gitanitor"}, "baseoid", "headoid")
	if err != nil {
		t.Fatal(err)
	}
	if status != "ahead" {
		t.Fatalf("status = %q, want ahead", status)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "gh" || !containsArg(f.Calls[0].Args, "repos/mdedys/gitanitor/compare/baseoid...headoid") {
		t.Fatalf("unexpected comparison invocation: %+v", f.Calls)
	}
}

func TestLookupPRs_EmptyBranchesNoCall(t *testing.T) {
	called := false
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		called = true
		return exec.Result{}
	}}
	got, err := LookupPRs(f, Repo{Owner: "o", Name: "r"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Errorf("no gh call should be made for zero branches")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map")
	}
}

func TestLookupPRs_FailureRelaysStderr(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stderr: "HTTP 401: Bad credentials", ExitCode: 1}
	}}
	_, err := LookupPRs(f, Repo{Owner: "o", Name: "r"}, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("expected relayed stderr, got %v", err)
	}
}

func TestCriterionG_GraphQLErrorsAreReturned(t *testing.T) {
	f := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		return exec.Result{Stdout: `{"data":{"repository":null},"errors":[{"message":"rate limit exceeded"}]}`}
	}}
	_, err := LookupPRs(f, Repo{Owner: "o", Name: "r"}, []string{"feature"})
	if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("expected GraphQL error, got %v", err)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsArgContaining(args []string, want string) bool {
	for _, arg := range args {
		if strings.Contains(arg, want) {
			return true
		}
	}
	return false
}
