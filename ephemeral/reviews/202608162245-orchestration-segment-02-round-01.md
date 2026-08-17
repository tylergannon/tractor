# Adversarial Review — Segment 2, Round 1

**Date:** 2026-08-16 22:45 local
**Target:** `lint/` package (spec Sections 7, 11.2) and engine run-state foundation:
`engine/state.go`, `engine/store.go`, `engine/store_test.go` (spec Sections 5.1–5.3,
5.6, 10; DoD 11.3/11.7/11.10 storage claims). Branch `codex/orchestration`,
worktree HEAD `158c2f5` plus untracked `engine/` and `lint/`.

**Scope note:** The launch prompt asked that other engine files be ignored as
next-segment work. Per this skill's rules I did not treat that as a limit on what
I may consider: I read `engine/runner.go` where needed to judge whether the
segment-2 state API is an adequate foundation, and finding 1 below reports a gap
that the segmentation would otherwise have hidden. No finding is suppressed on
segmentation grounds.

## Evidence Inspected

- `docs/spec.md` Sections 2 (schema/ID rules), 4.8 (branch node-set), 5.1–5.6,
  7, 8, 10; DoD 11.2, 11.3, 11.4, 11.5, 11.7, 11.10.
- `lint/lint.go`, `lint/analysis.go`, `lint/rules.go`, `lint/lint_test.go` (all, in full).
- `engine/state.go`, `engine/store.go`, `engine/store_test.go` (all, in full).
- Supporting reads: `graph/graph.go`, `graph/parse.go`, generated
  `graph/jsonschema/Graph.json` (confirmed node-ID pattern
  `^[A-Za-z_][A-Za-z0-9_]*$` is schema-enforced at `Parse`, so raw IDs reaching
  `store.allocateStage` path construction are constrained upstream — no path
  finding); `engine/runner.go` lines 200–260 for state-API usage only.
- Checks run: `go vet ./lint/... ./engine/...` (clean);
  `go test ./lint/... ./engine/... -count=1` (pass); both packages again under
  `-race` (pass).
- Behavioral probes (scratch module outside the repo, `replace`-directive import;
  no project files written): seven adversarial graphs — branch back-edge to its
  own parallel node, fan-in routing back into a branch interior, two sequential
  parallels, legal re-fan-out loop via a downstream chooser, single-branch
  parallel, tool `on_fail` naming a non-edge vs. its only edge, node
  `max_retries=-1` alongside `defaults.max_retries=-1`. All illegal shapes
  produced at least one ERROR (fail-closed); all legal shapes produced no
  diagnostics. Details feed findings 3 and 4.

## Findings

### 1. `stateFromCheckpoint` silently discards `RetryVisit`; the counter API cannot express the spec's visit/attempt arithmetic — **issue** (incomplete requirement)

`engine/state.go:46-55` restores every checkpoint field into `engineState`
except `RetryVisit`, and `engineState` has no field to hold it. Spec 5.3 and the
DoD claim named for this segment (11.7: "Resume from a failure checkpoint
(`retry_visit`) skips the retried node's visit increment exactly once; a
`max_visits=1` node failed and resumed repeatedly re-executes each time with
`node_visits` still 1") make this flag the load-bearing half of failure-resume
budget arithmetic. As written, any future resume path built on
`stateFromCheckpoint` gets a state object from which the flag is unrecoverable —
the restore function, whose entire contract is "restore", loses data with no
compile-time or test signal.

Relatedly, `beginExecution` (`engine/state.go:74-79`) couples the visit and
attempt increments into one atomic operation. Spec 3.5's attempt loop increments
`node_attempts` once per attempt (including retries) while `node_visits`
increments once per dispatch, and the `retry_visit` rule requires a
visit-skipped dispatch that still counts an attempt. Neither combination is
expressible through the current API without modifying this file. Retry and
resume are later segments, but the segment-2 foundation as merged cannot support
them unmodified, and nothing in the code or tests records the deferral.

Impact: failure-resume of a `max_visits=1` node becomes either unresumable or
double-counted the moment resume is wired to this API; retry attempts would
inflate `node_visits`. Fix: carry `retryVisit` into `engineState` (consumed once
by the first `beginExecution` after restore, or exposed to the runner) and split
attempt-only increment from visit increment. Repro: construct
`stateFromCheckpoint(Checkpoint{RetryVisit: true, ...})` — no observable
difference from `RetryVisit: false`.

### 2. `thread_harness_consistent` reports a configuration error as a graph ERROR under default options — **nitpick**

`lint/analysis.go:313-316` returns "harness resolver is not configured" when
`Options.ResolveHarness` is nil, and `lint/rules.go:483-489` converts that into
an error-severity `thread_harness_consistent` diagnostic. Package-level
`lint.Validate`/`lint.ValidateOrError` (no options) therefore reject every graph
that legitimately shares a thread via `thread_id`, with a message describing the
validator's configuration rather than a graph defect. Failing closed is
defensible — the check genuinely needs runtime knowledge — but the diagnostic
text should say the graph could not be checked, and the doc comment on
`Validate` ("runs the built-in rules with default options") gives no hint that
shared-thread graphs cannot pass it. Impact: confusing UX for any caller using
the convenience functions; the engine path that supplies a resolver is
unaffected.

### 3. A branch back-edge to its own parallel node yields actively misleading diagnostics — **nitpick**

`walkToFirstFanIn` (`lint/analysis.go:178-215`) stops only at fan-in and exit
nodes, so an edge from a branch interior back to the owning parallel node pulls
the parallel node itself — and, through it, every sibling branch — into that
branch's node-set. Probe output for `left -> par`:

```
[error] branch_disjoint: node belongs to branches "left" and "right" (node=right)
[error] no_nested_parallel: parallel node is nested inside branches of "par" (node=par)
```

The graph is correctly rejected (fail-closed, so no correctness hole), but both
messages misdescribe the defect — "par is nested inside branches of par" — and
neither points at the offending `left -> par` edge. Spec 4.8 defines branch
node-sets as reachability that does not pass through the fan-in; treating
re-encountering the owning parallel node as a boundary (or a dedicated
diagnostic) would name the real problem. Impact: author debugging time on a
shape the spec's loop guidance (bound the loop at the parallel node, route back
from downstream of the fan-in) makes an easy authoring mistake.

### 4. Node-level `max_retries`/`fidelity` diagnostics are suppressed when the value equals an invalid default — **nitpick**

`lint/rules.go:376-378` and `433-435` skip the node-scoped diagnostic when the
node's invalid value equals the invalid default. Because `graph.Parse` copies
defaults into node fields (`applyDefaults`), this deduplicates the
inherited-value case — but it cannot distinguish inheritance from an explicitly
authored identical value, so `defaults.max_retries=-1` plus an authored
`max_retries=-1` on a node reports only the defaults-scoped diagnostic (probe
confirmed). Validation still blocks execution, so impact is limited to
diagnostic precision; an author fixing only `defaults` will get a fresh
node-scoped error on the next run.

### 5. Checkpoint replacement is not fsynced before rename — **nitpick**

`saveCheckpoint` (`engine/store.go:83-89`) writes the temp file and renames it
without `File.Sync()` (or a directory sync). Rename gives atomicity against
process crash — which the test suite verifies well — but after power loss some
filesystems may surface the renamed file with empty or partial content, and
`checkpoint.json` exists precisely for crash recovery (spec 5.3). Standard
practice for a recovery-critical replace is write → fsync file → rename →
fsync directory. Low probability, bounded blast radius (one run's resume point),
hence nitpick.

## What Was Checked and Held Up

- All 28 built-in rules exist, match spec 7.2 identifiers and severities 1:1,
  and the table-driven test asserts each fires with the right severity plus a
  28-count completeness guard. Diagnostic shape carries rule, severity, message,
  node, edge, per 11.2's final claim; determinism is tested.
- Cascade suppression is deliberate and tested: a missing branch target degrades
  to `edge_target_exists` without spraying parallel-rule noise.
- Convergence analysis correctly accepts branch-interior cycles that can still
  reach the fan-in, rejects closed SCCs and dead ends, and handles sequential
  parallels, single-branch parallels, and legal re-fan-out loops (probes).
- Thread rules correctly exclude `none`-fidelity nodes, use resolved
  fidelity with the spec's `compacted` default, allow cross-provider sharing
  when the resolver maps to one harness, and key off static `ThreadKey`
  resolution per 5.4.
- `ValidateOrError` returns the full diagnostic set inside a typed
  `ValidationError`; custom rules get the interface's name backfilled.
- Checkpoint struct matches spec 5.3 field-for-field; save/load round-trips;
  mid-write readers never observe partial JSON (tested with a concurrent
  reader); timeline appends are serialized and complete under concurrency
  (race-detector clean).
- Sequence recovery implements `max(checkpoint.seq, stages/ scan)` exactly,
  ignores `latest/`, malformed names, and non-directories — both directions
  (scan ahead of checkpoint, checkpoint ahead of scan) are tested.
- Stage directories are `%06d-{node_id}`, created before execution, never
  reused; `stages/latest/{node_id}` is repointed atomically only on success and
  left absent after a failed stage (tested); failed stages get `error.json`,
  successes get engine-written `outcome.json`, per 5.6/11.3/11.7.
- No over-engineering observed in either package: no speculative abstraction,
  the `analysis` struct is a justified shared index, and the store API is
  minimal for its consumers.

## Outcome

**material findings remain** — finding 1 (issue); findings 2–5 are nitpicks.

---

# Adjudication of Finding 1 — Round 2 (2026-08-16)

**Target:** re-review of finding 1 only, against the retry refactor in
`engine/state.go`, `engine/runner.go`, `engine/runner_test.go`.

**Checks run:** `go vet ./engine/...` (clean); `go test ./engine/... -count=1
-race` (pass); verbose run of the retry/stop/retry-visit tests (all pass).

## Verification

Every element of finding 1 is addressed, and each has a test that pins it:

- **`RetryVisit` survives restore.** `engineState` now carries a `retryVisit`
  field (`state.go:36`) and `stateFromCheckpoint` restores it (`state.go:55`).
- **Consumed exactly once, at the first real attempt.** `beginVisit`
  (`state.go:76-84`) clears the flag and skips the visit increment once;
  `executeWithRetry` calls it only on attempt 1 (`runner.go:271-273`).
  `TestRetryVisitRestoresAndIsConsumedByFirstActualAttempt` verifies the spec
  11.7 arithmetic directly: restored visits stay at 1 through the resumed
  attempt, the next `beginVisit` counts normally.
- **Visit/attempt increments are split.** `beginExecution` is gone; `beginVisit`
  and `beginAttempt` are separate, and retries call only `beginAttempt`
  (`runner.go:274`). `TestRunnerRetriesRetryableErrorsWithFreshStagesAndOneVisit`
  confirms 1 visit / 3 attempts across a two-retry recovery, with fresh
  per-attempt stage dirs, per-attempt `error.json`, no checkpoint written
  between attempts (asserted from inside attempt 2), and `latest/` pointing at
  the successful stage. The budget check in `offeredSuccessors` reads
  `visits()`, so retries no longer inflate visit budgets (spec 11.4).
- **Pre-attempt stop race.** A stop landing before the first attempt returns
  interrupted with `attempted=false`, and the runner then writes no failure
  checkpoint — the prior checkpoint stands, exactly per spec 3.7/5.3 ("a
  written failure checkpoint always follows a consumed execution"); the
  unconsumed `retryVisit` flag also survives for the next resume.
  `TestExecuteWithRetryStopBeforeFirstAttemptConsumesNothing` and
  `TestRunnerStopBeforeDispatchPreservesInitialCheckpoint` pin both halves;
  `TestRunnerStopDuringBackoffEndsWithoutAnotherAttempt` covers the
  consumed-execution side (checkpoint with `retry_visit=true`, counters 1/1,
  no second stage dir).

Non-retryable categories bypass backoff entirely (test fails the run from
inside `retryDelay` if entered), and node-level `max_retries` correctly
overrides the file default. **Finding 1 is resolved.**

## Residual observations (not findings against segment 2)

- The `retryVisit` flag is not node-scoped: `beginVisit` consumes it for
  whichever node dispatches first. That is correct for the designed resume flow
  (the first dispatch after a failure checkpoint is the failed node itself),
  but resume is not yet wired to `stateFromCheckpoint` outside tests — worth an
  assertion or comment when the resume segment lands.
- Out-of-segment, noted for the engine-core review (the launch prompt asked not
  to broaden this round; recorded rather than dropped): `resolvedMaxRetries`
  can return a negative authored value, making `maxAttempts <= 0` in
  `executeWithRetry` — the loop body never runs and the function hits
  `panic("unreachable")` (`runner.go:297`). Reachable only through a
  `ValidateFunc` that is not wired to lint (`max_retries_nonnegative` blocks it
  otherwise); clamp or reject at `NewRunner` for defense in depth.

## Revised Segment-2 Verdict

Finding 1 (the only issue) is fixed and test-covered. Findings 2–5 remain open
as nitpicks.

**only nitpicks remain**
