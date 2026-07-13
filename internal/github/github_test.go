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
