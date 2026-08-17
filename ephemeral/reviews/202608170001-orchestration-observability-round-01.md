# Adversarial Review — Orchestration Observability, Round 01

## Review Target

Working-tree diff on branch `codex/orchestration` implementing Section 10
(Observability and Events), Section 3.9 (External Steering), and the backend
result-validation Definition-of-Done item, reviewed against `docs/spec.md` and
repository instructions (`lefthook.yml` default-linter policy).

Changed/added files reviewed in full:

- `engine/control.go` (new: run manifest, control socket, `POST /steer`, active-execution tracking)
- `engine/runner.go` (timeline lifecycle events, checkpoint events, active-stage tracking)
- `engine/parallel.go` (parallel/branch timeline events)
- `engine/store.go` (`appendSteering` audit writer)
- `harness/backend.go` (result validated against the exact turn schema before Outcome conversion)
- `engine/observability_test.go`, `engine/definition_proof_test.go`, `harness/backend_test.go` (new proof)

Surrounding code read: `engine/store.go`, `engine/runner.go` (full),
`engine/parallel.go` (full), `harness/backend.go` (full), `harness/result.go`,
`harness/claude/adapter.go` (Steer path), `cmd/tractor/root.go`. Spec sections
read: 3.9, 5.6, 10, 11.10, 12.1, 12.2 (interface notes), 11.14 backend DoD
checklist, and the routing/validation passages at lines 580–590.

## Caller-Scope Note

The launch prompt lists the user's acceptance concerns (simplicity, removal of
over-engineering, etc.). These were treated as priorities, not as limits on
subject matter; the review considered all defect classes. "Do not edit any
other files" was honored as a valid read-only operating constraint.

## Proof Executed

- `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./...` (default config per `lefthook.yml`) — 0 issues.
- `go test ./...` — all packages pass.
- `go test -race -count=1 ./engine/ ./harness/` — pass; no data races across
  the new control-server/parallel-timeline concurrency.
- Live end-to-end: built `cmd/tractor`, ran a start→tool→exit pipeline in a
  fresh process. Verified `manifest.json` (id, name, goal, started_at,
  `control_socket`), a complete `timeline.jsonl` narration
  (PipelineStarted → CheckpointSaved → Stage events → PipelineCompleted), and
  a live `POST /steer` over the advertised Unix socket returning `409` (no
  live backend turn) with an empty body. The long scratchpad path exercised
  the macOS socket-path fallback: the manifest advertised the actual temp
  socket, steering reached it, and the socket was removed after the run.

## Verified Non-Findings (checked, held up)

- **Backend schema re-validation is spec-required, not belt-and-suspenders.**
  Initial reading suggested duplicate validation (adapters already validate per
  Section 12.2 note 4). The backend DoD (Section 11.14) overrules this: proven
  with scripted adapters through the public surface, "a nonconforming result
  surfaces as a terminal Error" and "`next` present exactly when the choice
  schema required it". `harness/backend.go:95` + `:134-141` implement exactly
  that; the worklog's `doc_bug` note matches. New table test covers all four
  cells.
- **Parallel steering rejection is engine-side before backend handoff**
  (spec 3.9, 11.10): `engine/control.go:143` rejects on
  `nodeType == "parallel"` under the same mutex the walk uses;
  `TestControlRejectsTopLevelParallelBeforeBackendHandoff` proves zero backend
  calls. Branch stages cannot become the steering target because
  `setActiveStage` (`engine/control.go:164-170`) is guarded by node ID.
- **macOS socket-path fallback is necessary, not future-proofing**: without it,
  `net.Listen("unix", ...)` fails for any logs root ≥ ~104 bytes (the live
  proof triggered it). The manifest advertises the real path, per spec 3.9.
- **Timeline event order and payloads** match Section 10, verified by
  `TestRunnerTimelineNarratesRetriesParallelBranchesAndCheckpoints` and by the
  live run.
- **Steering audit** lands in the active execution's stage directory as
  `steering.jsonl` (spec 5.6), verified by test and code path.

## Findings

### 1. [issue] Timeline-append failure inside `runBranches` silently skips a branch's work while the remaining branches keep running

`engine/parallel.go:123-147`. If `appendTimeline` fails for
`ParallelBranchStarted` (e.g. disk error on `timeline.jsonl`), the worker
`continue`s — `walkBranch` never runs for that index, leaving a zero-value
`BranchResult` — while the other workers proceed to run their branches to
completion (real LLM cost) even though the fan-out is already guaranteed to
fail. `Execute` then returns the append error (`engine/parallel.go:77-79`)
*before* `attributeBranchSegments` and before `branches.json` is written, so
the evidence of the branches that did complete is dropped. The handling is
also internally inconsistent: a failed `ParallelBranchCompleted` append does
not skip anything, and every other append failure in the diff returns
immediately. Concrete failure scenario: fill or revoke write permission on
`timeline.jsonl` mid-fan-out; one branch is skipped without its own error
record, siblings burn tokens, and completed-branch evidence is lost. The
`eventMu`/`errors.Join` accumulation plumbing exists only to support this
special case; treating a branch-event append failure the same as every other
engine error (fail the branch, or fail the pool promptly) would delete it.

### 2. [nitpick] `StageCompleted.next` re-derives routing instead of using the engine's one resolver

`engine/runner.go:440-449` re-implements the single-successor default
(`next == "" && len(offered) == 1`) that `resolveNext`
(`engine/runner.go:564-587`) owns, and emits the event before `resolveNext`
validates the choice. When a handler names an unoffered successor, the
timeline records `StageCompleted` with a `next` the engine immediately
rejects, and the only trace of the routing rejection is the terminal
`PipelineFailed`. Duplicated routing logic is exactly the kind of divergence
risk the spec's "one chooser, one validator" design avoids; emitting the event
after `resolveNext` (or recording raw `outcome.Next` only, per Section 10's
"`next` is the chosen successor when one was chosen") removes the duplication.

### 3. [nitpick] `serveControl` holds `activeMu` across backend steering and audit file I/O

`engine/control.go:141-155` holds the same mutex the walk loop needs for
`beginTopLevel`/`clearTopLevel` (`engine/runner.go:309-311`) while calling
`Backend.Steer` (adapter I/O; bounded at 5s by the in-repo adapters'
`controlTimeout`, but the `CodergenBackend` interface makes no such promise)
and while appending `steering.jsonl`. A slow steer stalls node transitions for
its full duration. The lock is what makes the active-target check and the
audit-directory choice consistent, so this is a deliberate trade-off — but
capturing the target under the lock and releasing it before the
`Steer`/append pair would keep spec semantics (steering is fire-and-forget,
3.9) without coupling walk progress to control-surface latency.

### 4. [nitpick] Timeline events carry no timestamps

`engine/store.go:94-110` writes events exactly as constructed; no event has a
`ts` field (confirmed in the live run's `timeline.jsonl`). Spec Section 10
defines the payloads without timestamps and says implementations MAY attach
fields, so this is conforming — but the file is pitched as "the external
observer's spine" that supervisors tail, and without timestamps an operator
reading it after the fact cannot place any event in time (durations exist,
absolute times do not; `steering.jsonl` and run-log events do carry `ts`).
One field in one function would fix it.

### 5. [nitpick] Empty-bodied error branch in the control-server goroutine

`engine/control.go:77-81`: the `Serve` goroutine checks the error only to
`return` in both arms — `go func() { _ = server.Serve(listener) }()` says the
same thing honestly. Purely idiomatic; default linters accept it.

## Outcome

`material findings remain`
