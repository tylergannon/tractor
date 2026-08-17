# Adversarial Review — Git Workspace Foundation (round 01, 00:30)

Date: 2026-08-17 00:30 local
Reviewer: adversarial-review (self-performed, independent)

## Review target

Uncommitted files in `/Users/tyler/src/.worktrees/tractor/orchestration`:

- `engine/git_workspace.go` (233 lines, mtime 2026-08-16 23:00)
- `engine/git_workspace_test.go` (310 lines, mtime 2026-08-16 23:00)

Reviewed against `docs/spec.md` Sections 4.8 (Parallel Handler), 5.3
(Checkpoint), 5.6 (Run Directory Structure), 11.6 (Node Handlers), and 11.7
(State), plus repository instructions and lefthook.yml.

**State note:** the target files changed on disk during this review. My first
read captured an older revision whose detached-HEAD test assertion used a raw
`exec.Command` with inherited environment; that version verifiably failed
`go test` under a linked-worktree Git-hook environment (`GIT_DIR` set —
reproduced). The current revision replaces it with `gitTestFails` (cleaned
environment) and separates stdout/stderr in `gitOutput`. This review's findings
apply to the **current** on-disk revision. A sibling artifact
(`202608170020-git-workspace-round-01.md`, by the other active session)
reviewed the older 225/297-line revision; this round is independent of it and
does not overwrite it.

## Evidence inspected

- Full read of both target files (current revision) and spec Sections 4.8,
  5.3, 5.6, 11.6–11.7; grep for call sites across the module (none outside the
  file — this is an unwired foundation slice; parallel handler and Finalize
  wiring do not exist yet, which matches the slice scope and is not treated as
  a defect).
- `go test ./... -count=1`: **all packages pass** in a normal environment.
- Real hook environment established empirically: a scratch repo's pre-commit
  hook env was dumped, showing `GIT_INDEX_FILE` (relative), `GIT_PREFIX`,
  `GIT_AUTHOR_*`; a **linked worktree** (this repo's actual layout under
  `~/src/.worktrees/`) additionally sets an absolute `GIT_DIR`. Full suite
  re-run with exactly that env (`GIT_DIR`, `GIT_INDEX_FILE`, `GIT_PREFIX`,
  `GIT_AUTHOR_*` exported): **all packages pass**, repeated 5× without flake.
- `golangci-lint run ./engine/...`: 0 issues. `go tool modernize ./engine/...`
  (dry-run): silent. (The mutating lefthook jobs — `goimports -w`,
  `modernize -fix` — were not run, per the read-only constraint.)
- `cleanGitEnvironment`'s strip list compared against
  `git rev-parse --local-env-vars` on this machine: exact match, principled
  rather than ad-hoc.
- Behavioral probes in scratch repos: `git worktree add` + failing
  `post-checkout` hook (finding 1); `symbolic-ref` under inherited `GIT_DIR`;
  `-c core.hooksPath=<empty dir>` suppression (verified working).

## Spec compliance verified

- **4.8 freeze semantics:** HEAD + staged + unstaged + deleted + untracked
  captured; ignored files excluded; user's branch, refs, index bytes, status,
  and files byte-identical after freeze (test asserts all of these, including
  a second freeze proving snapshot-commit determinism via pinned author /
  committer identity and date). Non-git workdir is a terminal error. The
  temporary-index `read-tree`/`add -A`/`write-tree`/`commit-tree` sequence is
  exactly the spec's suggested mechanism, parented on HEAD so worktree HEADs
  pin the snapshot against gc.
- **4.8/5.6 inventory:** `{logs_root}/worktrees.jsonl` append-only,
  `{path, branch_id, ts}` exactly (test asserts the closed field set), one
  line per worktree at creation; cleanup does not rewrite it.
- **11.6/11.7 cleanup:** completed-run sweep removes every inventoried
  worktree, prunes, and removes the fan-out parent; the not-cleaning of failed
  runs is correctly left to the (future) Finalize wiring rather than baked in
  here. Worktrees now live under `logs_root`, so a failed run's evidence
  survives OS temp reaping.
- **Hook-environment hygiene:** every engine and test git invocation runs with
  repository-local `GIT_*` variables stripped; `extraEnv` appended last wins
  (Go ≥1.19 `exec.Cmd.Env` dedup keeps the last value). Verified under the
  genuine lefthook pre-commit env of a linked worktree.
- No over-engineering of note: no abstractions beyond four package-private
  functions and three small structs; no speculative options or interfaces.

## Findings

### 1. Issue — `git worktree add` runs the user repo's `post-checkout` hook; a failing hook orphans a registered worktree and fails the fan-out

`engine/git_workspace.go:94` invokes `git worktree add --detach` without
suppressing hooks. Real Git behavior (reproduced in a scratch repo): the
`post-checkout` hook runs inside the new worktree; if it exits nonzero,
`worktree add` returns exit 1 **after** the worktree is fully created and
registered. Reproduction: repo with `.git/hooks/post-checkout` exiting 1 →
`git worktree add --detach <path> HEAD` prints the hook output, exits 1, yet
`<path>` contains the checkout and `git worktree list` shows it registered.

Consequences in `createBranchWorktrees`: the error return fires **before**
`appendWorktreeInventory` (line 94 vs 98), so the created, registered worktree
is named nowhere — `worktrees.jsonl` (which Section 4.8 designates as the
durable inventory precisely for fan-outs that die before completing) can never
sweep it. The fan-out also fails spuriously on a healthy repository whose hook
merely dislikes detached checkouts — post-checkout hooks are common (lefthook,
husky). A hook that *succeeds* but writes files breaks the "every branch
starting from the same frozen state" guarantee instead; the pristine-status
test assertion (`git_workspace_test.go:94`) would not hold in such repos.

Fix, verified working: pass `-c core.hooksPath=<empty>` (e.g. `/var/empty` or
an empty temp dir) on the `worktree add` (and arguably the `remove`) calls.
Engine-invoked git should not execute user hooks.

### 2. Nitpick — non-repo check swallows the underlying error

`engine/git_workspace.go:34-37`: any `rev-parse --is-inside-work-tree` failure
— git not installed, permission denied — reports "parallel execution requires
a git worktree", discarding `err`. Wrap the real error so a missing git binary
doesn't masquerade as a non-repo workdir.

### 3. Nitpick — inventory is appended after worktree creation

`engine/git_workspace.go:94-98`: a crash between `worktree add` and the
inventory append leaves a registered worktree `worktrees.jsonl` never names —
the same durable-inventory gap as finding 1 without needing a hook. Appending
the intent line first (and making cleanup tolerate a path that was never
created) closes the window in the direction the spec's Finalize sweep expects.

### 4. Nitpick — `{logs_root}/worktrees-XXXXXX/` is an undocumented run-directory entry

Section 5.6 enumerates the run directory contents; `os.MkdirTemp(logsRoot,
"worktrees-")` (line 87) adds a randomly-suffixed directory per fan-out that
the layout does not name. A fixed, documented `worktrees/` subtree (per-fan-out
subdirs inside it) would match the spec's "implementation-chosen location
recorded in branches.json" while keeping the run dir legible. Secondary
interaction: if an author points `logs_root` inside the repository work tree,
each later freeze captures earlier fan-outs' worktrees as embedded-repo
gitlinks (untracked, not ignored — normative capture), polluting snapshots and
`git status`; worth a doc note or a `.gitignore`/exclude decision when the
parallel handler is wired.

### 5. Nitpick — uncommented load-bearing test setup and minor duplication

`git_workspace_test.go:32` `t.Setenv("GIT_TRACE", "1")` is real hardening (it
proves stderr chatter cannot corrupt `gitOutput` parsing) but reads as leftover
debugging; a one-line comment would protect it from future deletion.
`createBranchWorktrees` and `appendWorktreeInventory` both `MkdirAll(logsRoot)`
(git_workspace.go:84, 108) — one suffices.

## Outcome

**material findings remain** — finding 1 (post-checkout hook execution during
engine-invoked `worktree add`) is a reproduced real-Git failure mode with a
verified one-line mitigation; everything else is nitpick-grade. Tests, lint,
and the linked-worktree lefthook environment all pass on the current revision.

---

# Round 02 — Re-review after the material fix

Date: 2026-08-17 (round 02)
Reviewer: adversarial-review (self-performed, independent)

## Target and adjudications

Current on-disk revision (verified by hash at review time):

- `engine/git_workspace.go` — sha1 `40abce287f677980e5a0e871114ae26ee8fb300d` (227 lines)
- `engine/git_workspace_test.go` — sha1 `c6bd3f912f5fa4441ce2ecede57122297a441611` (316 lines)

Caller adjudications of round-01 nitpicks, accepted as authoritative design
decisions and not re-raised: inventory stays after successful creation (an
entry denotes a *created* worktree); unique run-local `worktrees-*` directories
stay (multiple fan-outs need distinct locations); no cleanup idempotence or
embedded-repository machinery (non-required future-proofing). These constrain
the solution, not the review; nothing new surfaced in those areas that rises
above the adjudicated trade-offs.

## Fix verification

- **Finding 1 (hook execution) — fixed and pinned.** `git_workspace.go:94` now
  passes `-c core.hooksPath=/dev/null` on `worktree add`. The test configures a
  real failing `post-checkout` hook (exit 97) via `core.hooksPath` on the test
  repo *before* fan-out (`git_workspace_test.go:33-38`), so the passing test
  proves: no spurious failure, three worktrees created, all three inventoried,
  cleanup leaves `git worktree list` with only the main entry — no orphan.
  **Mutation check:** in a scratch copy of both files, deleting only the
  `-c core.hooksPath=/dev/null` argument makes the test fail with the hook's
  exit status 97 (`run_processes_parallel: done: exit status 97`), so the test
  genuinely pins the suppression rather than passing vacuously.
  `git worktree remove` runs no hooks (cleanup succeeds in the same test with
  the failing hook still configured), so leaving the `remove` calls
  unsuppressed is correct, not an omission.
- **Round-01 nitpick 5 (duplicate `MkdirAll`) — fixed**:
  `appendWorktreeInventory` no longer re-creates `logsRoot`.
- **Repository-global `worktree prune` — removed** from cleanup, as stated.
  Correct: a successful `worktree remove` already deletes the admin entry, and
  the old prune could have collected other tools' prunable worktrees repo-wide.

## Evidence

- `go test ./... -count=1`: all packages pass (quiet run, no failures).
- `go test ./engine/ -count=1` under the real linked-worktree lefthook hook
  environment (`GIT_DIR`, `GIT_INDEX_FILE`, `GIT_PREFIX`, `GIT_AUTHOR_*`
  exported, values matching an actual hook-env dump): passes.
- `golangci-lint run ./engine/...`: 0 issues.
- Full re-read of both files at the hashes above; no call-site changes
  elsewhere in the module (still the unwired foundation slice, as scoped).
- `/dev/null` as `core.hooksPath`: git resolves hooks as
  `/dev/null/<hook-name>`, which can never exist, so all hooks are skipped;
  no warning is emitted (and stderr is separated regardless). Sound on the
  supported platforms.

## Round-02 findings

### 1. Nitpick (carried, unadjudicated) — non-repo check still swallows the underlying error

`engine/git_workspace.go:34-37`: any `rev-parse --is-inside-work-tree` failure
(git missing from PATH, permission denied) still reports only "parallel
execution requires a git worktree", discarding `err`. Wrapping the real error
would keep a missing git binary from masquerading as a non-repo workdir.

### 2. Nitpick (carried, partial) — load-bearing test setup is uncommented

`git_workspace_test.go:32-38`: both `t.Setenv("GIT_TRACE", "1")` (proves
stderr chatter cannot corrupt `gitOutput` parsing) and the failing
`post-checkout` hook installation (pins hook suppression) are deliberate
hardening with no comment saying so; either could plausibly be deleted as
leftover debris by a future edit. One line of intent over each would protect
them.

## Round-02 outcome

**only nitpicks remain** — the material finding is fixed and mutation-verified;
the two remaining items are cosmetic robustness/maintainability notes. Tests,
lint, and the real hook environment all pass at the reviewed hashes.
