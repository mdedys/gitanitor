package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/mdedys/gitanitor/internal/branch"
	xexec "github.com/mdedys/gitanitor/internal/exec"
)

func TestCriterionA_BranchesCommandHasFullScanFlagsAndRejectsPositionalNames(t *testing.T) {
	command := branchesCommand()
	if command.Name != "branches" {
		t.Fatalf("command name = %q", command.Name)
	}
	if len(command.Flags) != 2 {
		t.Fatalf("flags = %+v", command.Flags)
	}
	yesFlag, ok := command.Flags[0].(*cli.BoolFlag)
	if !ok || yesFlag.Name != "yes" || len(yesFlag.Aliases) != 1 || yesFlag.Aliases[0] != "y" {
		t.Fatalf("--yes/-y flag = %+v", command.Flags[0])
	}
	dryRunFlag, ok := command.Flags[1].(*cli.BoolFlag)
	if !ok || dryRunFlag.Name != "dry-run" {
		t.Fatalf("--dry-run flag = %+v", command.Flags[1])
	}
	app := &cli.App{Commands: []*cli.Command{command}}
	err := app.Run([]string{"gitanitor", "branches", "feature-x"})
	if err == nil || !strings.Contains(err.Error(), "does not accept positional") {
		t.Fatalf("expected positional-argument rejection, got %v", err)
	}
}

func TestCriterionA_DryRunWinsOverYesWithoutPromptOrMutation(t *testing.T) {
	fake := &xexec.Fake{}
	fake.Responder = func(name string, args ...string) xexec.Result {
		if name == "git" {
			switch args[0] {
			case "rev-parse":
				return xexec.Result{Stdout: "true\n"}
			case "for-each-ref":
				return xexec.Result{Stdout: "main\x00main-tip\nfeat\x00feat-tip\n"}
			case "worktree":
				return xexec.Result{Stdout: "worktree /repo/main\nHEAD main-tip\nbranch refs/heads/main\n\n"}
			}
		}
		if name == "gh" && len(args) >= 2 && args[0] == "repo" {
			return xexec.Result{Stdout: `{"owner":{"login":"acme"},"name":"widget","defaultBranchRef":{"name":"main"}}`}
		}
		if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
			aliasBranch := map[string]string{}
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-f" {
					key, value, ok := strings.Cut(args[i+1], "=")
					if ok && strings.HasPrefix(key, "b") {
						aliasBranch[key] = value
					}
				}
			}
			repository := map[string]any{}
			for alias, name := range aliasBranch {
				nodes := []any{}
				if name == "feat" {
					nodes = append(nodes, map[string]any{"number": 7, "state": "MERGED", "headRefOid": "feat-tip", "headRepositoryOwner": map[string]any{"login": "acme"}})
				}
				repository[alias] = map[string]any{"nodes": nodes}
			}
			payload, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": repository}})
			return xexec.Result{Stdout: string(payload)}
		}
		return xexec.Result{}
	}
	prompted := false
	out := &strings.Builder{}
	code := runBranchesWith(fake, branchOptionsForTest(true, true), promptFunc(func(string) bool {
		prompted = true
		return true
	}), out)
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out.String())
	}
	if prompted {
		t.Fatal("dry-run must not prompt even when --yes is also set")
	}
	for _, call := range fake.Calls {
		if call.Name == "git" && len(call.Args) >= 2 && call.Args[0] == "branch" {
			t.Fatalf("dry-run must not mutate refs: %v", call)
		}
	}
	if !strings.Contains(out.String(), "Would delete (merged)") {
		t.Fatalf("dry-run report missing Would label:\n%s", out.String())
	}
}

type promptFunc func(string) bool

func (p promptFunc) Confirm(question string) bool { return p(question) }

func branchOptionsForTest(yes, dryRun bool) branch.Options {
	return branch.Options{Yes: yes, DryRun: dryRun}
}
