package branch

import (
	"reflect"
	"testing"

	"github.com/mdedys/gitanitor/internal/exec"
)

func TestCriterionH_LocalInventoryParsesRefsAndWorktreeCheckoutPaths(t *testing.T) {
	fake := &exec.Fake{Responder: func(name string, args ...string) exec.Result {
		if name == "git" && len(args) > 0 && args[0] == "for-each-ref" {
			return exec.Result{Stdout: "zeta\x00z-tip\nalpha\x00a-tip\n"}
		}
		if name == "git" && len(args) > 0 && args[0] == "worktree" {
			return exec.Result{Stdout: "worktree /repo/main\nHEAD main-tip\nbranch refs/heads/main\n\nworktree /repo/feature\nHEAD f-tip\nbranch refs/heads/feature\n\n"}
		}
		return exec.Result{}
	}}
	refs, err := List(fake)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Ref{{Name: "alpha", OID: "a-tip"}, {Name: "zeta", OID: "z-tip"}}; !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
	checked, err := checkedOutBranches(fake)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checked["feature"], []string{"/repo/feature"}) {
		t.Fatalf("checked-out paths = %+v", checked)
	}
	if len(fake.Calls) != 2 || fake.Calls[0].Args[0] != "for-each-ref" || fake.Calls[1].Args[0] != "worktree" {
		t.Fatalf("unexpected runner calls: %+v", fake.Calls)
	}
}
