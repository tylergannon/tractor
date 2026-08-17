# Adversarial Review — Parallel Execution Slice (Round 01)

**Target:** uncommitted working-tree changes on `codex/orchestration`:
new `engine/parallel.go`, `engine/parallel_test.go`; modified
`engine/runner.go`, `engine/runner_test.go`, `engine/state.go`,
`engine/git_workspace.go`, `engine/git_workspace_test.go`.

**Reviewed against:** `docs/spec.md` §3.2–3.8 (execution loop, successor
choice, budgets, retry, backoff, run failure, concurrency), §4.8–4.9
(parallel / fan-in handlers), §5.3 (checkpoint), §5.6 (run directory),
§11.3–11.7 (definition of done), §12.4 (run log / segment index), plus
surrounding code (`store.go`, `fan_in.go`, `tool.go`, `codergen.go`,
`lint` helpers) needed to understand the slice.

**Caveat — moving target.** The working tree was being edited concurrently
while this review ran: `git_workspace.go`/`parallel.go` gained stop-aware
git plumbing (`gitOutputWithStop`, `errGitStopped`) mid-review, and
`createBranchWorktrees` was reordered (inventory append now precedes
`git worktree add`) minutes before this artifact was written. The review
verdict applies to the state pinned by these SHA-1s:

```
2e6a5d15adb2aaaef999fe912d80ec945fcee551  engine/parallel.go
512ab5eb130d6831ca20b2896f75bc80f6d8bbac  engine/git_workspace.go   (blob f86e8b7)
08b3d4d7e124db210ba17cb89ac59ba17d139e70  engine/runner.go
84b3b60041ce02597a5bf182ed27bf5a44a7515f  engine/state.go
b0afb456b80e84e3e577a992ebb82110d057121b  engine/parallel_test.go
e967e53a4968cbcab9a55ede5033f22fe902f68d  engine/git_workspace_test.go
4248d3effd10aa74947cf4016b0e4dabefab01cb  engine/runner_test.go
```

## Evidence inspected / proof run

- `go test -count=1 ./...` — all packages pass; `go test -race -count=1
  ./engine/` passes; `go vet ./...` clean (all on the pinned state).
- Independent real-Git end-to-end flow (external program importing the
  engine, not the repo's tests): fresh repo with a committed base, an
  uncommitted tracked modification, and an untracked file; `parallel`
  node with two `tool` branches; **`RunnerConfig.Workdir` set to
  `repo/sub/dir`, below the repository root**. Verified with real git:
  - freeze captured both the tracked modification and the untracked file
    (branch `cat root.txt` printed `root-modified`; untracked file
    present in the worktree) without mutating the main workspace —
    `git status` after the run showed exactly the original ` M root.txt`
    and `?? sub/dir/untracked.txt`, no new commits/refs on the branch;
  - branches ran in isolated `logs/worktrees-*/branch-00N` dirs; branch
    writes (`mark.txt`, `wd.txt`) never appeared in the main repo;
  - `branches.json` written to the parallel node's stage dir in offered
    order with outcome, notes, path, workdir, stage_dirs, segments;
  - exit-node Finalize removed all worktrees and the fan-out dir
    (inventory `worktrees.jsonl` retained); **repeat-finalize** via
    `ResumeRunner` on the completed run returned COMPLETED and re-swept
    idempotently.
- Repo tests independently re-verified the remaining requested behavior
  on the pinned state: max_parallel cap (peak concurrency asserted),
  ordered evidence + segment attribution with an earlier fan-out's index
  entries excluded by byte offset, counter rollback on branch failure
  (branch visits/attempts back to 0, parallel node's own visit=1 /
  attempt=1 retained, `retry_visit` set, `completed_nodes` untouched),
  failed-run worktree retention, stop interrupting active and queued
  branches with interrupted evidence for both, stop cancelling in-flight
  git commands, and repeat cleanup after a manually deleted worktree.

Spec conformance spot-checks that came up clean: branch walks write no
checkpoints and never touch `last_stage`/`last_response`/`completed_nodes`
(§3.8, §5.3); pre-execution failures (empty offered set, unresolvable
handler, stop before first attempt) write no checkpoint while routing
failures and consumed executions checkpoint with `retry_visit` (§3.2,
§3.7 — the `attempted` flag threading in `executeNode`/`executeWithRetry`
implements this correctly); `branchError` maps every branch failure to
terminal except interrupted, so a parallel execution can never return
retryable (§4.8); `branches.json` is written before any branch error
propagates (§4.8 step 3, §11.6); stage-dir seq is allocated under the
state mutex and recovered by directory scan on resume, so crash-mid-fan-out
directories are never reused (§5.3); segment attribution is exactly the
"index offset at fan-out start + node_id on walked path" rule (§4.8);
`resolveNext` accepts the fan-in as the parallel node's `next` despite it
being unoffered (§3.2/3.3 special routing); rollback restores whole
counter maps, which is equivalent to "roll back branch deltas, keep the
parallel node's own counters" because the snapshot is taken after the
parallel node's own visit/attempt increments and the top-level walk is
otherwise single-threaded during the fan-out.

## Findings

### 1. Issue — a workdir below the repository root loses its subdirectory mapping inside branches

`walkBranch` passes the worktree **root** as every branch execution's
workdir (`engine/parallel.go:106`, `engine/git_workspace.go` —
`createBranchWorktreesWithStop` returns worktree roots), while
`freezeGitWorkspaceWithStop` deliberately resolves and snapshots from
`git rev-parse --show-toplevel`. Nothing re-derives the
workdir-relative-to-root suffix. Demonstrated with the real flow above:
with `Workdir = repo/sub/dir`, a top-level `tool` node runs its command
in `repo/sub/dir`, but the *same node executed as a branch* runs in the
worktree root — my branch tool's `cat root.txt` succeeded where the
top-level equivalent would have failed, and the fan-in then ran back in
`repo/sub/dir`. Any pipeline authored for a monorepo subdirectory
(`pnpm test`, relative paths in prompts) silently changes semantics the
moment it crosses a parallel node.

Spec honesty: §4.8's pseudocode literally passes `workdir=wt`, so the
implementation matches the spec's letter; the spec is simply silent about
sub-root workdirs. That makes this a joint spec/implementation gap rather
than a plain bug, but the behavioral inconsistency is real, reproducible,
and lands exactly in the scenario this slice claims to support (the caller
explicitly built and tested sub-root workdir freezing). Either map the
branch workdir to `wt/<rel>` (where `rel = Workdir relative to repoRoot`)
or record a deliberate spec decision that branch scopes are always repo
roots.

**Impact:** wrong-directory execution for branch tool/codergen nodes in
sub-root-workdir runs. **Evidence:** real run transcript — branch
`tool.log` contains `root-modified` from `cat root.txt` executed at the
worktree root.

### 2. Nitpick — `Run()` silently replaces any caller-registered `parallel` handler; post-construction registrations are dropped

`engine/runner.go:245` registers the internal `parallelHandler` on every
`Run()`, overriding a caller's `"parallel"` registration without error,
and `cloneRegistry` (`engine/runner.go:178,210`) means `Register` calls
made on the caller's registry after `NewRunner` no longer reach the
runner — a behavior change from the previous shared-registry semantics.
Both are defensible (the parallel handler needs run-scoped state; cloning
prevents leaking it into the caller's registry) but are invisible
contracts. Worth a doc comment on `NewRunner`/`Register`, or an explicit
error on a user-supplied `"parallel"` handler.

### 3. Nitpick — `resolveNext` parallel case is narrower than spec and has a degenerate message

Spec §3.2/§3.3 defines allowed targets for a parallel node as "offered
targets, plus the designated fan-in"; `engine/runner.go:477-487` accepts
*only* the fan-in. Unreachable in practice because the engine owns the
only parallel handler (which always names the fan-in), so this is
recorded as spec-text divergence, not a defect. Cosmetic: when
`outcome.Next` is empty the failure reads `parallel handler named invalid
fan-in successor: ` with a trailing empty value.

### 4. Nitpick — `BranchResult.Notes` keeps the last *non-empty* notes, not the final walk notes

`engine/parallel.go:147-149` skips empty notes, so a branch whose final
node returns empty notes reports an earlier node's notes as its
`walk.final_notes` (§4.8). Marginal observability skew in the fan-in's
rendered evidence; a one-line semantic decision either way.

### 5. Nitpick — idiom and dead-weight details

- `file.Seek(offset, 0)` at `engine/parallel.go:215` — use `io.SeekStart`.
- `freezeGitWorkspace` / `createBranchWorktrees` non-stop wrappers
  (`engine/git_workspace.go:36,90`) are now used only by tests;
  production code calls the `WithStop` variants. Either migrate the tests
  or drop the wrappers.
- `bufio.Scanner` defaults cap lines at 64KB in the three JSONL readers
  (inventory, segment index); paths won't hit it, but a `Buffer` call or
  a comment would make the bound deliberate.
- `gitOutputWithStop` reports `errGitStopped` even when the underlying
  command already succeeded before the stop landed; harmless here (every
  call site treats it as an interruption point, and the
  append-inventory-before-`worktree add` ordering keeps a killed add
  sweepable) — noting so the discarded-success semantics stay intentional.

## Not findings (checked and passed)

Counter rollback equivalence; worktree cleanup only on completed runs
plus repeat-finalize/resume idempotency (including a manually deleted
worktree); ordered evidence and offset-based segment attribution across
repeated fan-outs; fan-in special routing; branch error wrapping
(terminal/interrupted, never retryable); stop interrupting active,
queued, and in-flight-git work; evidence-before-error write ordering;
checkpoint discipline (branch walks silent, failure checkpoints with
`retry_visit`, pre-execution failures silent); seq recovery across crash
resume; race detector clean. Over-engineering scan: the worker pool,
counter snapshot, `nodeExecution` refactor, and git environment
scrubbing are all proportionate to spec requirements; no unrequested
abstraction found. The concurrent edits observed mid-review (stop-aware
git, inventory-before-add) each *closed* windows rather than opening
them.

## Outcome

**material findings remain** — finding 1 (sub-root workdir mapping) needs
either an implementation change or an explicit spec ruling; everything
else is nitpick-grade.

---

# Round 02 — Re-review after the sub-root workdir fix

**Reviewed state** (working tree, `codex/orchestration`):

```
0af28123949da5bd6972f052ac32783d393e4cfc  engine/parallel.go
c689cfa65ad32fd5ee86753628edae859d8c4fb5  engine/git_workspace.go
08b3d4d7e124db210ba17cb89ac59ba17d139e70  engine/runner.go      (unchanged since round 01)
84b3b60041ce02597a5bf182ed27bf5a44a7515f  engine/state.go       (unchanged since round 01)
05ad8ae7d5f3da1a24d986e47707fc9f73a76fb2  engine/parallel_test.go
b1325c371fc1a0aeb7589e10051ffbadfae4ef8d  engine/git_workspace_test.go
4248d3effd10aa74947cf4016b0e4dabefab01cb  engine/runner_test.go (unchanged since round 01)
```

## Round-01 finding 1 (material) — RESOLVED, verified

`gitWorkspaceSnapshot` now carries `workdirRel` (`git_workspace.go:23`),
computed as `Rel(repoRoot, EvalSymlinks(workdir))` at freeze
(`git_workspace.go:54-61`); each `branchWorktree` keeps both `Path` (the
worktree root, used for inventory and cleanup) and `Workdir` (the mapped
`root/rel` used for execution, `git_workspace.go:125-129`), created via
`MkdirAll` so an empty, untracked configured subdirectory — which git
cannot materialize in a worktree — exists before the branch runs.
`runBranches` passes `worktree.Workdir` into the branch walk
(`parallel.go:106`), so `BranchResult.Workdir` and every branch scope
name the mapped directory.

Verified two independent ways on real git:

- Repo tests: `parallel_test.go` now runs from `repo/nested` (an empty
  untracked directory in the test repo — exercising the MkdirAll path),
  asserts every branch scope's workdir basename is `nested`, rejects the
  main workspace, and pins `worktrees.jsonl` entry paths as the parents
  of the evidence workdirs. `git_workspace_test.go` asserts
  `snapshot.workdirRel == "nested"` and `branch.Workdir ==
  branch.Path/nested`.
- My round-01 external real-Git flow re-run unmodified in substance
  (`Workdir = repo/sub/dir`, a *non-empty* tracked subdir with an
  untracked file): branch tool cwd is now
  `logs/worktrees-*/branch-00N/sub/dir` (captured via `pwd` in
  `tool.log`), `cat root.txt` fails inside branches exactly as it does
  at the top level (round 01 showed it wrongly succeeding from the
  worktree root), the fan-in still runs in `repo/sub/dir`, the main repo
  is untouched, finalize sweeps both worktrees, and repeat-finalize via
  `ResumeRunner` returns COMPLETED idempotently.

## Additional repairs since round 01 — verified

- **Cancellable git setup.** `gitOutputWithStop` checks the stop signal
  before spawning and kills the in-flight process via
  `exec.CommandContext` + a stop-watching goroutine
  (`git_workspace.go:213-249`); the whole freeze and worktree-creation
  pipeline threads the run's stop signal, and the parallel handler maps
  `errGitStopped` to an interrupted (not terminal) Error
  (`parallel.go:86-91`), so a stop during git setup produces a proper
  interrupted failure checkpoint with branch counters rolled back.
  Covered by `TestGitOutputStopCancelsCommand` (fake git that sleeps;
  asserts prompt cancellation).
- **Pre-add durable inventory.** The `worktrees.jsonl` entry is appended
  *before* `git worktree add` (`git_workspace.go:118-124`), closing the
  round-01 window where a stop or crash mid-add left an orphan worktree
  the Finalize sweep had never heard of — this matches §4.8's intent
  that the inventory also names worktrees of a fan-out that died before
  its walks returned. Covered by
  `TestStoppedWorktreeCreationLeavesCleanupInventory` (add killed
  mid-flight; the inventory entry survives).
- **Stale-entry cleanup without a global prune.** `cleanupBranchWorktrees`
  attempts `git worktree remove --force` first and only tolerates the
  failure when the path no longer exists (`git_workspace.go:165-173`),
  so a pre-add inventory entry whose worktree never materialized, or a
  manually deleted worktree, no longer fails repeat-finalize — while a
  removal failure on a still-present worktree is still surfaced. I
  independently confirmed with real git that `worktree remove --force`
  on a manually deleted worktree exits 0 *and clears the
  `.git/worktrees` registration*, so skipping a repo-wide
  `git worktree prune` leaves no stale registrations behind in the
  user's repository. Covered by the repeat-cleanup +
  manually-deleted-worktree additions to
  `TestGitWorkspaceSnapshotFanoutInventoryAndCleanup`.

## Proof run (round-02 state)

- `go test -count=1 ./...` — all packages pass.
- `go test -race -count=1 ./engine/` — pass.
- `go vet ./...` — clean.
- External real-Git sub-root flow + repeat-finalize resume as described
  above.

## Remaining findings

Round-01 nitpicks 2–5 stand unchanged (`runner.go`/`state.go` are
byte-identical to round 01): silent override of a caller-registered
`parallel` handler and post-`NewRunner` registration drop; `resolveNext`
parallel case narrower than §3.2's "offered targets plus fan-in" (with a
degenerate message on empty `Next`); `BranchResult.Notes` keeping the
last non-empty rather than final notes; `Seek(offset, 0)` vs
`io.SeekStart`, test-only non-stop wrappers, and the default
`bufio.Scanner` line cap. One new nitpick:

- **Nitpick — `BranchResult.Workdir` now records the mapped
  subdirectory, not the worktree root.** §4.8 describes the field as
  "the branch's **worktree path**" and §4.9 renders it as `Worktree:`
  in the fan-in prompt. With a sub-root workdir the rendered path points
  inside the worktree (`…/branch-001/sub/dir`), which is arguably the
  more useful path for the fan-in agent — but it diverges from the
  spec's wording, and the worktree root is now only recoverable from
  `worktrees.jsonl`. Either update the spec text or carry both paths in
  the evidence. (`git_workspace.go:129`, `parallel.go:106,122`.)

## Outcome (round 02)

**only nitpicks remain** — the material sub-root workdir finding is
fixed and verified end-to-end on real git; the three internal repairs
(cancellable git, pre-add inventory, tolerant cleanup) are sound and
tested; everything still open is nitpick-grade.
