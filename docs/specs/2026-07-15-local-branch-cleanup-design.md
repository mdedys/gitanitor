# Local Branch Cleanup Design

## Objective

Add a `gitanitor branches` command that classifies every local Git branch using
live GitHub pull-request metadata, removes branches whose work is demonstrably
complete, and gives the user explicit control over ambiguous destructive cases.
The command deletes local branch refs only. It never deletes remote branches or
worktree directories.

## Command Interface

```text
gitanitor branches [--yes|-y] [--dry-run]
```

The command always scans every local branch. It does not accept branch names,
patterns, or other positional filters.

- Default mode prints the complete classification, asks about judgment calls
  individually, and then confirms the safe merged batch once.
- `--yes` or `-y` skips the safe merged-batch confirmation. It never consents
  to judgment calls.
- `--dry-run` prints the complete classification using explicit `Would ...`
  labels, does not prompt, and does not modify refs. It wins over `--yes`.

The output header identifies the GitHub `owner/repo` selected by `gh`, matching
the existing worktree command. Every scanned local branch appears in a sorted,
grouped report with its disposition and reason.

## Architecture

Create a focused `internal/branch` package parallel to `internal/worktree`.
The package owns local-branch enumeration, worktree checkout detection,
classification, reporting, prompting, deletion, and result/exit-code handling.
It depends on the existing `internal/exec` runner seam and `internal/github`
metadata layer. The CLI may reuse `worktree.StdinPrompter` through the branch
package's compatible prompt interface without coupling branch domain logic to
worktree cleanup.

Extend `internal/github` in two focused ways:

1. Repository resolution returns the GitHub default branch in addition to the
   owner and name.
2. PR lookup returns each PR's recorded head commit OID in addition to its
   number, state, URL, and head-repository owner.

Branch names remain GraphQL variables rather than interpolated query text. The
existing fork guard remains: only PRs whose head repository owner matches the
queried repository owner participate in branch classification.

## Inputs and Data Flow

Before changing any ref, the flow gathers and validates all required inputs:

1. Resolve the GitHub repository and its default branch with `gh`.
2. Enumerate `refs/heads/*`, recording each branch name and current commit OID.
3. Enumerate Git worktrees and map checked-out branch refs to worktree paths.
4. Record whether each branch tip is reachable from any locally known
   remote-tracking ref. The command does not fetch or prune first.
5. Look up all relevant GitHub PRs in a batched GraphQL request, including PR
   states and recorded head commit OIDs.
6. Classify every branch using the precedence below.
7. Print the complete report.
8. Prompt judgment calls individually, then confirm the safe merged batch.
9. Revalidate and delete each approved local ref.

No branch is deleted unless its own classification succeeds. Global preflight,
enumeration, or PR lookup failures abort before mutation; a per-branch
ancestry or local-count failure is reported and skipped so fully classified
branches can still receive their normal consent flow, with a final exit code
of `1`.

## PR-to-Tip Matching

A PR covers a local branch tip when the tip is equal to, or an ancestor of,
the PR's recorded head OID. Exact OID equality needs no further lookup. For a
non-equal OID, use local Git ancestry when both objects are available; when the
PR head object is not available locally, use GitHub comparison metadata to
prove ancestry. An explicit non-ancestor result is not a match. If the required
comparison cannot be completed, classification fails and the run aborts before
mutation.

This relationship prevents a historical merged PR from authorizing deletion
after a branch name has been reused. It also handles a stale local branch that
is behind the final pushed PR head.

The matching PR head is authoritative evidence that the covered commits were
pushed. It overrides the absence of a local remote-tracking ref, which is
common after delete-on-merge and squash-merge workflows. "Unpushed" therefore
means commits beyond or outside every applicable recorded PR head that are not
reachable from any locally known remote-tracking ref.

Any merged PR qualifies regardless of its base branch.

## Classification Precedence

Classification uses this order so stronger protections cannot be weakened by
older PR history:

1. **Default branch:** always skip with `default branch`. There is no override.
2. **Checked out:** always skip and include the worktree path, for example
   `checked out at ../wt-feat-x`. There is no override.
3. **Open PR:** if any matching-owner PR for the branch is open, always skip
   with its PR number. An open PR protects the branch even when an older PR was
   merged.
4. **Covered merged PR:** when the current local tip is covered by a merged
   PR head, add it to the safe merged batch.
5. **Covered closed-unmerged PR:** offer the branch as an individual judgment
   call. The prompt includes the PR number.
6. **Newer or divergent local-only work after a merged or closed PR:** offer
   the branch individually and warn with the number of locally unique commits.
   When a closed PR is also involved, include both facts in the prompt.
7. **Newer or divergent pushed work after historical PRs:** skip because no
   merged or closed PR head covers the current tip.
8. **No PR:** always skip with `no PR`, even when Git's commit graph says the
   branch is merged or the branch has locally unique commits.

When several non-open historical PRs exist, the current-tip relationship picks
the relevant PR. A covered merged PR is safe; a covered closed PR is a judgment
call; no covered PR follows the local-only or pushed-mismatch rules above.

Branches marked protected on GitHub receive no special local treatment. The
remote ref is untouched, and the GitHub default branch already has an explicit
hard protection.

## Reporting and Prompts

Normal output groups all branches into:

- safe merged branches pending one batch confirmation;
- closed-unmerged branches asked individually;
- branches with newer or divergent unpushed commits asked individually; and
- skipped branches with their exact reasons.

Closed-unmerged branches with local-only commits stay in the individual group
and show both risks, for example:

```text
delete feat-x (PR #12 closed without merging, 2 unpushed commits)? [y/N]
```

Merged branches with newer local-only work use an equally explicit warning,
for example:

```text
delete feat-x despite 2 new unpushed commits since merged PR #41? [y/N]
```

Prompts default to No. Under `--yes`, all individual judgment calls are
reported and skipped. Under `--dry-run`, groups use labels such as
`Would delete (merged)` and `Would ask individually`; no prompt is issued.

## Deletion and Concurrency Safety

Approved branches are deleted with:

```text
git branch -D -- <branch>
```

The force form is required because Git's graph does not recognize all GitHub
merge strategies, especially squash merges. Gitanitor's PR and commit checks
are the safety gate. The `--` separator prevents a branch name from being
interpreted as an option.

Immediately before each deletion, re-read the branch ref and current worktree
map. If the branch moved, disappeared, or became checked out after
classification, do not delete it. Report `branch changed during cleanup`,
continue with other approved branches, and make the final exit code `1`.

Deleting a local branch must not delete or prune any remote-tracking ref or
remote branch.

## Errors and Exit Codes

- Preflight, enumeration, or PR lookup failures abort before mutation and exit
  `1`. A per-branch ancestry-comparison or local-count failure reports the
  complete scan, skips that branch, allows fully classified branches to finish
  their normal consent/deletion flow, and still exits `1`.
- A per-branch deletion or revalidation failure prints Git's error, continues
  with the remaining approved branches, and makes the final exit code `1`.
- Successful deletions, declined prompts, dry runs, and nothing-to-clean runs
  exit `0`.
- Git and GitHub stderr is relayed without replacing its useful details.

## Acceptance Criteria

**(a) CLI contract.** `gitanitor branches` scans every local branch and accepts
`--yes`/`-y` and `--dry-run`, with `--dry-run` taking precedence; positional
branch names or patterns are rejected. At least one passing test named with
`CriterionA` exercises the command and these flag semantics, and
`go test ./...` exits `0` with output shown. The implementation must not weaken
or skip pre-existing tests.

**(b) Hard protections.** The GitHub default branch, every branch checked out
in any worktree, every branch with any matching-owner open PR, and every branch
with no qualifying PR are never deleted or offered for deletion; checked-out
reports include the worktree path, and same-named PRs from other owners do not
affect classification. At least one passing test named with `CriterionB`
exercises each protection, and `go test ./...` exits `0` with output shown. The
implementation must not weaken or skip pre-existing tests.

**(c) Merged-PR qualification.** A branch enters the safe merged batch only
when its local tip equals or is an ancestor of a matching-owner merged PR's
recorded head commit; the PR qualifies regardless of base branch, and this
PR-head evidence remains sufficient after squash/delete-on-merge removes
remote containment. Historical merged PRs do not qualify a reused branch whose
current tip is newer or divergent, and multiple historical PRs are resolved by
their relationship to the current tip. Passing tests named with `CriterionC`
exercise exact, ancestor, deleted-remote, non-default-base, reused-branch, and
multiple-PR cases; `go test ./...` exits `0` with output shown. The
implementation must not weaken or skip pre-existing tests.

**(d) Judgment calls.** Covered closed-unmerged branches and branches with
newer or divergent commits not reachable from any locally known
remote-tracking ref after a merged or closed PR are offered only through
individual default-No prompts; prompts identify the PR state/number and
locally unique commit count where applicable. Newer pushed work without a
covering merged or closed PR is hard-skipped. Passing tests named with
`CriterionD` cover closed, unpushed, combined-warning, decline, and
newer-pushed cases; `go test ./...` exits `0` with output shown. The
implementation must not weaken or skip pre-existing tests.

**(e) Reporting and consent.** Default mode prints every scanned branch in
deterministic grouped output, asks judgment calls individually before one safe
merged-batch confirmation, and defaults every prompt to No. `--yes` deletes
only the safe merged batch without prompting and skips judgment calls.
`--dry-run` wins over `--yes`, uses explicit `Would ...` labels, never prompts,
and performs zero mutation. Passing tests named with `CriterionE` exercise all
three modes and grouped output; `go test ./...` exits `0` with output shown.
The implementation must not weaken or skip pre-existing tests.

**(f) Mutation boundaries and revalidation.** Approved refs are deleted
locally with the equivalent of `git branch -D -- <branch>` only after
rechecking the expected commit OID and worktree checkout state. A branch that
moved, disappeared, or became checked out is not deleted. No run deletes or
prunes a remote branch, remote-tracking ref, or worktree directory, and no run
invokes fetch or prune. Passing tests named with `CriterionF` assert local
deletion, untouched remote/worktree state, no fetch/prune invocation, and
last-moment change protection; `go test ./...` exits `0` with output shown.
The implementation must not weaken or skip pre-existing tests.

**(g) Failure and exit semantics.** Preflight, enumeration, or GitHub lookup
failures abort before mutation and exit `1`; a per-branch ancestry-comparison
or local-count failure reports the complete scan, skips only that branch,
allows fully classified branches to finish their normal consent/deletion flow,
and still makes the final exit `1`. Individual revalidation or deletion
failures relay useful Git/GitHub error details, allow remaining approved
deletions to continue, and make the final exit `1`. Successful deletions,
declines, dry runs, and no-op runs exit `0`. Passing tests named with
`CriterionG` cover each failure class, classified-branch continuation, stderr
relay, and success exit; `go test ./...` exits `0` with output shown. The
implementation must not weaken or skip pre-existing tests.

**(h) Package and integration boundaries.** Branch cleanup lives in a focused
`internal/branch` package; repository resolution exposes the GitHub default
branch; PR lookup exposes recorded head OIDs and preserves branch names as
GraphQL variables; all Git/`gh` execution remains behind
`internal/exec.Runner`. Passing tests named with `CriterionH` verify parsing,
variable-safe query construction, and runner invocations; `go test ./...` and
`go vet ./...` both exit `0` with output shown. The implementation must not
weaken or skip pre-existing tests or add vet-suppression workarounds.

## Testing Strategy

Implementation follows red-green-refactor. Focused GitHub tests verify default
branch and PR-head parsing, GraphQL variable safety, and comparison errors.
Branch package tests use real temporary Git repositories while faking GitHub,
following the existing runner seam.

Tests cover at least:

- safe merged-PR deletion and untouched remote refs;
- squash/delete-on-merge PR-head evidence;
- default, checked-out, open-PR, and no-PR hard skips;
- checked-out skip reasons containing worktree paths;
- same-name fork PR filtering;
- reused branches with newer pushed commits;
- merged and closed branches with newer local-only commits;
- closed-unmerged individual confirmations;
- multiple historical PRs selected by current-tip relationship;
- merged PRs targeting non-default branches;
- `--yes` skipping all judgment calls;
- `--dry-run` wording and zero mutation;
- full grouped reporting;
- global lookup failure before any deletion and per-branch classification
  failure with continued consent for unaffected branches;
- ref movement or checkout between classification and deletion;
- continued processing and exit `1` after an individual deletion failure; and
- successful no-op and declined-prompt exit codes.

Final verification runs:

```text
go test ./...
go vet ./...
```

## Out of Scope

- Remote branch deletion or remote-tracking ref pruning.
- Implicit `git fetch` or `git fetch --prune`.
- Worktree-directory removal from the `branches` command.
- GitHub branch-protection or ruleset queries.
- Positional branch filters, glob patterns, configuration files, or allowlists.

## Goal

```text
/goal every criterion (a)-(h) in docs/specs/2026-07-15-local-branch-cleanup-design.md holds, specifically: (a) the branches CLI and flags satisfy the full-scan contract; (b) default, checked-out, open-PR, no-PR, and fork-owner protections hold; (c) only current-tip-covered merged PRs enter the safe batch, including squash/delete-on-merge and non-default-base cases; (d) closed and locally unique work receive the required individual judgment prompts while newer pushed work is skipped; (e) default, --yes, and --dry-run reporting and consent semantics hold; (f) deletion remains local-only and revalidates each ref without fetch or prune; (g) classification is mutation-atomic and failures have the specified continuation and exit behavior; (h) package, GitHub metadata, GraphQL-variable, and Runner boundaries hold; each criterion has at least one passing test whose name references its criterion letter, each check is run and its output shown, `go test ./...` and `go vet ./...` exit 0, no pre-existing test is weakened or skipped, no vet-suppression workaround is added, all implementation work is committed, and the working tree is clean; or stop after 30 turns and report what's blocking
```
