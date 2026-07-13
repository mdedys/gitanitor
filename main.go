package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/mdedys/gitanitor/internal/exec"
	"github.com/mdedys/gitanitor/internal/github"
	"github.com/mdedys/gitanitor/internal/worktree"
)

func main() {
	app := &cli.App{
		Name:  "gitanitor",
		Usage: "keep a git repository's worktrees tidy",
		Commands: []*cli.Command{
			worktreesCommand(),
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func worktreesCommand() *cli.Command {
	return &cli.Command{
		Name:  "worktrees",
		Usage: "remove linked worktrees whose branch has a merged GitHub PR",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the merged-batch confirmation"},
			&cli.BoolFlag{Name: "dry-run", Usage: "print the classification report and exit without modifying anything"},
		},
		Action: func(c *cli.Context) error {
			code := runWorktrees(exec.System{}, worktree.Options{
				Yes:    c.Bool("yes"),
				DryRun: c.Bool("dry-run"),
			})
			if code != 0 {
				return cli.Exit("", code)
			}
			return nil
		},
	}
}

// runWorktrees performs preflight then hands off to the flow. It returns the
// process exit code.
func runWorktrees(runner exec.Runner, opts worktree.Options) int {
	if !exec.LookPath("gh") {
		fmt.Fprintln(os.Stderr, "gitanitor requires the GitHub CLI (gh)")
		return 1
	}

	if res := runner.Run("git", "rev-parse", "--is-inside-work-tree"); res.ExitCode != 0 {
		fmt.Fprint(os.Stderr, res.Stderr)
		return 1
	}

	repo, err := github.ResolveRepo(runner)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}

	flow := worktree.Flow{
		Exec:   runner,
		Prompt: worktree.StdinPrompter{In: os.Stdin, Out: os.Stdout},
		Out:    os.Stdout,
		Opts:   opts,
	}
	code, _, _ := flow.Run(repo)
	return code
}
