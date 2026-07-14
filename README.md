# gitanitor

Keep a git repository's worktrees tidy. Run it from anywhere inside a repo and it finds linked worktrees whose branch has a **merged GitHub PR** and removes them — leaving your unfinished, unpushed, or open-PR worktrees alone.

## Requirements

- `git` and the [GitHub CLI](https://cli.github.com/) (`gh`) on your `PATH`
- Logged in via `gh auth login`

## Install

```
go install github.com/mdedys/gitanitor@latest
```

This puts a `gitanitor` binary in `$(go env GOPATH)/bin` — make sure that's on your `PATH`.

## Usage

```
gitanitor worktrees [--yes|-y] [--dry-run]
```

Run it from the main worktree or any linked worktree of the repository you want to clean.

| Flag | Effect |
|---|---|
| `--yes`, `-y` | Skip the merged-batch confirmation prompt. Never deletes closed-PR or detached-HEAD worktrees (those always require interactive confirmation). |
| `--dry-run` | Print the full classification report and exit without changing anything. Wins over `--yes`. |

### What it does

1. **Preflight** — checks `gh` is installed, that you're inside a git repo, and resolves which GitHub repo `gh` will query. The resolved `owner/repo` is printed in the header so a wrong-remote guess is visible before anything is deleted (`gh` silently prefers a remote named `upstream` over `origin` — fix with `gh repo set-default`).
2. **Classify** each worktree locally, then look up PR state for the rest in one batched GraphQL call.
3. **Act** — prompt individually for closed-unmerged PRs and detached-HEAD worktrees, then confirm the merged batch once, then remove.

The main worktree is never touched. Removal never uses `git worktree remove --force`.

### What gets skipped

A worktree is reported and left alone when it is:

- **dirty** — has modified, staged, or untracked files
- **unpushed** — has local commits not on any remote (including branches whose upstream is gone after delete-on-merge)
- **locked** — the lock reason is shown
- on a branch with an **open PR** or **no PR**

Detached-HEAD worktrees have no branch to look a PR up for, so clean, pushed ones are offered for deletion with a per-worktree prompt — like closed PRs, never under `--yes`.

Prunable entries (the directory is already gone, only git's bookkeeping remains) are cleared automatically.

### Example

```
$ gitanitor worktrees
gitanitor · mdedys/gitanitor

Cleared 1 stale worktree entry.

Closed without merging — asked individually:
  ../wt-experiment   spike-idea   PR #12 closed

Deleting (merged):
  ../wt-feature-a    feat-a       PR #41 merged
  ../wt-feature-b    feat-b       PR #45 merged

Skipped:
  ../wt-wip          feat-c       uncommitted changes
  ../wt-review       feat-d       PR #50 still open
  ../wt-usb          feat-e       locked: usb drive

delete ../wt-experiment (branch spike-idea, PR #12 closed without merging)? [y/N] n
Delete these 2 worktrees? [y/N] y
```

Branch refs are left in place — only the worktree directory and git's admin entry are removed.

### Exit codes

- `0` — any successful run: deletions done, you declined, or nothing to do.
- `1` — a real failure: preflight failure, a git/gh command error, or an individual removal failure.

## Development

```
go test ./...
go vet ./...
```

Domain logic lives in `internal/` — `internal/worktree` (enumeration, classification, removal, flow) and `internal/github` (the `gh` layer). All `git`/`gh` access flows through one exec-helper interface in `internal/exec`, which is also the test seam: unit tests run against a fully faked helper, and integration tests build real git repositories in `t.TempDir()` while faking only `gh`.
