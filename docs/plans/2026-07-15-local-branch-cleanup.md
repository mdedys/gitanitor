# Local Branch Cleanup Implementation Plan

> **For agentic workers:** Execute this plan task-by-task with red-green-refactor verification and keep the existing worktree tests intact.

**Goal:** Add `gitanitor branches` with full local-branch scanning, GitHub PR-head safety classification, explicit consent, local-only deletion, and failure-safe revalidation.

**Architecture:** Extend `internal/github` so repository metadata includes the default branch and PR records include `headRefOid`; keep GraphQL and GitHub comparison calls behind `internal/exec.Runner`. Add a focused `internal/branch` package that enumerates refs/worktrees/remotes, classifies every branch before mutation, reports/prompts, and revalidates each approved ref before `git branch -D -- <name>`. Wire the package into a CLI command with injected flow seams for tests.

**Tech Stack:** Go, urfave/cli, Git/GitHub CLI subprocesses through `internal/exec`, real temporary Git repositories for branch-flow tests.

---

### Task 1: Extend GitHub metadata

**Files:**

- Modify: `internal/github/github.go`
- Modify: `internal/github/github_test.go`

- [ ] Add `Repo.DefaultBranch`, request `defaultBranchRef`, and parse its name while preserving the existing owner/name and stderr behavior.
- [ ] Add `PR.HeadOID`, include `headRefOid` in the batched GraphQL node selection, and parse it.
- [ ] Add a Runner-backed comparison helper for `repos/<owner>/<name>/compare/<base>...<head>` and parse `ahead`/`identical`/non-ancestor statuses with useful errors.
- [ ] Add criterion-H tests for default-branch/head-OID parsing, comparison invocation, and variable-safe GraphQL branch arguments; run the focused package tests red then green.

### Task 2: Build branch inventory and classification

**Files:**

- Create: `internal/branch/branch.go`
- Create: `internal/branch/classify.go`
- Create: `internal/branch/parse_test.go`
- Create: `internal/branch/classify_test.go`

- [ ] Define branch candidates, dispositions, options, prompter, result, and flow types without importing the worktree cleanup package.
- [ ] Enumerate all `refs/heads/*` with current OIDs, parse all worktree checkout paths, and compute locally unique commit counts from existing remote-tracking refs without fetch/prune.
- [ ] Implement default/checked-out/open/no-PR/fork-owner protections and current-tip PR-head coverage, using local ancestry when possible and GitHub comparison metadata when the PR head object is unavailable.
- [ ] Implement merged, closed-unmerged, local-only judgment, newer-pushed skip, and deterministic grouped classification with no mutation during preflight.
- [ ] Add criterion-A through criterion-D classification tests, including exact/ancestor/squash-delete-on-merge/non-default-base/reused/multiple-PR cases and the required prompt/skipped cases; run focused tests red then green.

### Task 3: Add reporting, consent, deletion, and revalidation

**Files:**

- Modify: `internal/branch/branch.go`
- Create: `internal/branch/run.go`
- Create: `internal/branch/run_test.go`

- [ ] Report every branch in deterministic groups, use explicit `Would ...` labels in dry-run, and keep `--yes` limited to the safe merged batch.
- [ ] Prompt closed/local-only candidates individually with default-No questions and confirm the safe merged batch once in normal mode.
- [ ] Re-read each approved ref and worktree map immediately before `git branch -D -- <branch>`; classify movement/disappearance/new checkout as a per-branch failure and continue.
- [ ] Ensure global classification failures cause zero deletion; per-branch classification failures skip only the affected branch while unaffected approved deletions continue; deletion failures continue, errors relay stderr, and exit codes match the spec.
- [ ] Add criterion-E through criterion-G tests for all modes, grouped output, zero dry-run mutation, no fetch/prune, revalidation races, partial continuation, stderr relay, and success/no-op/decline exits; run focused tests red then green.

### Task 4: Wire and verify the CLI

**Files:**

- Modify: `main.go`
- Create: `main_test.go`

- [ ] Add `branches [--yes|-y] [--dry-run]`, reject positional args, preserve dry-run precedence, and run the branch flow with the existing stdin prompter.
- [ ] Add criterion-A and criterion-H CLI/runner-boundary tests without weakening existing tests.
- [ ] Run `go test ./...` and `go vet ./...`, inspect that no vet suppression was added, commit all implementation and test changes, and verify a clean working tree.
