# Adversarial Review — Orchestration Observability, Round 02

## Review Target

Re-review of the full working-tree diff on branch `codex/orchestration`
(Section 10 observability, Section 3.9 steering/control surface, backend
result validation) against `docs/spec.md` and repository instructions
(`lefthook.yml` default-linter policy), after the round-01 rework.

Files re-read in full this round: `engine/control.go`, `engine/runner.go`,
`engine/parallel.go`, `engine/store.go`, `harness/backend.go`, and the diffs to
`engine/runner_test.go`, `engine/observability_test.go`,
`engine/definition_proof_test.go`, `harness/backend_test.go`. Spec sections
re-consulted: 3.9, 4.8, 5.6, 10, 11.10, 11.14, 12.1, 12.2.

## Proof Executed

- `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./...` (defaults) — 0 issues.
- `go test ./...` — all packages pass.
- `go test -race -count=1 ./engine/ ./harness/` — pass.
- `go tool goimports -l` over changed trees — nothing to fix. `go tool
  modernize` flags only pre-existing generated code
  (`harness/codex/schema/types_gen.go`), untouched by this diff.
- Live end-to-end with the rebuilt CLI: start→tool→exit pipeline in a fresh
  process. `manifest.json` advertises a working `control_socket` (long-path
  fallback exercised); `POST /steer` over the socket returns `409` with empty
  body while a non-LLM node runs; `timeline.jsonl` narrates
  PipelineStarted → CheckpointSaved → Stage events → PipelineCompleted with
  validated `next` values; the socket is removed after the run.

## Round-01 Findings — Disposition

1. **Branch skip on timeline-append failure (issue) — resolved.**
   `engine/parallel.go:123-155`: a failed `ParallelBranchStarted` append now
   records a proper failed `BranchResult` for that branch (ID, workdir, empty
   path/stage/segment slices) instead of a zero value, and
   `engine/parallel.go:76-93` now writes `branches.json` and attributes
   segments *before* surfacing the event error, so completed-branch evidence
   is no longer lost. The policy is now uniform with the rest of the diff:
   a timeline append failure is terminal everywhere. Siblings continuing to
   completion matches the engine's existing branch-failure semantics (branches
   never cancel each other; the fan-in receives all results as evidence).
2. **`StageCompleted.next` re-derived routing (nitpick) — resolved.**
   `resolveNext` now runs inside `executeWithRetry`
   (`engine/runner.go:434-446`) before the event is emitted; the event carries
   the single validated `nextID`, the duplication is gone, and an unoffered
   choice never appears as a completed stage's `next`
   (`engine/runner_test.go:150` covers the rejection path end-to-end).
3. **Lock held across steering (nitpick) — unaddressed**, carried below.
4. **No timestamps on timeline events (nitpick) — unaddressed**, carried below.
5. **Empty-bodied error branch in the Serve goroutine (nitpick) — resolved**
   (`engine/control.go:77`).

## Findings

### 1. [nitpick] `serveControl` still holds `activeMu` across backend steering and audit file I/O

`engine/control.go:137-151` holds the mutex the walk loop needs for
`beginTopLevel`/`clearTopLevel` (`engine/runner.go:309-311`) while calling
`Backend.Steer` and appending `steering.jsonl`. The lock is what keeps the
active-target check and the audit directory consistent, and the in-repo
adapters bound `Steer` at 5s (`controlTimeout`), so the worst case is a
bounded stall of node transitions — but the `CodergenBackend` interface makes
no boundedness promise for other backends. Capturing the target under the lock
and releasing it before the `Steer`/append pair would decouple walk progress
from control-surface latency at the cost of a slightly weaker
audit-vs-transition ordering guarantee.

### 2. [nitpick] Timeline events carry no timestamps

`engine/store.go:94-110`; confirmed again in the live run's `timeline.jsonl`.
Spec Section 10 defines the payloads without timestamps and only says
implementations MAY attach fields, so this conforms — but the file is the
"external observer's spine", and without a `ts` field an after-the-fact reader
gets relative durations with no absolute anchor (the steering audit and
run-log events both carry timestamps). One added field in `appendTimeline`
would fix it.

### 3. [nitpick] `manifest.json` is written non-atomically

`engine/control.go:102` uses `writeJSON` (plain `os.WriteFile`), while the
checkpoint writer uses the tmp-then-rename pattern
(`engine/store.go:82-91`). The manifest is exactly the file external operators
poll to discover `control_socket` (spec 3.9/5.6); a reader racing the initial
write or a resume rewrite can see a torn file. Low probability and
self-healing on the next read, but the atomic-write helper already exists two
files away.

### 4. [nitpick] Redundant double-accounting for a failed `ParallelBranchStarted` append

`engine/parallel.go:127-141` both records the failure as the branch's
`BranchResult.Error` and folds the same error into `eventErr`; either one
alone fails the parallel node (`engine/parallel.go:81-83` and `:94-98`). The
`eventErr` channel is genuinely needed only for `ParallelBranchCompleted`
append failures on otherwise-successful branches. Harmless, but one of the two
paths for the started-case is dead weight.

## Outcome

`only nitpicks remain`
