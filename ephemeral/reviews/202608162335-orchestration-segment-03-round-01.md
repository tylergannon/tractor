# Adversarial Review — Orchestration Segment 3, Round 1

**Target:** Engine core traversal/registry/state integration, retry/backoff, tool handler, and codergen handler, reviewed against `docs/spec.md` Sections 3.1–3.7, 4.1–4.7, 5.1–5.4, 8 and DoD 11.3–11.6/11.9, including the negative retry-budget guard and registry wiring. Scope was derived from the spec and worklog, not solely from the launch prompt; no instruction narrowing what defects could be considered was present.

**Files inspected:** `engine/runner.go`, `engine/state.go`, `engine/store.go`, `engine/tool.go`, `engine/codergen.go`, all four engine test files, plus supporting contracts in `graph/graph.go`, `graph/parse.go`, `harness/validate.go`, `harness/backend.go`, spec sections listed above, and the build worklog (segment sequencing decisions).

**Proof executed:**
- `go test ./engine/... ./graph/... ./harness/...` — all pass.
- `go test ./engine/ -count=1 -race` — pass.
- `go vet ./engine/...` — clean.
- Real end-to-end run from a scratchpad module through the public API: parsed a JSON pipeline (start → codergen with two conditioned edges and `max_visits: 2` → tool with `on_fail` loop-back → exit), simulation codergen, real shell tool. Observed: correct loop (`start, plan, check, plan, check`), tool nonzero exit routed to `on_fail`, budget respected, five monotonic zero-padded stage dirs, `latest/` symlinks, and a spec-conformant final checkpoint (`current_node: done`, empty `next_node`, `retry_visit: false`, correct visit/attempt counters, `seq: 5`).

## Assessment

The segment is a faithful, well-factored implementation of the specified engine core. Verified against spec, each explicitly:

- **Core loop (3.2):** terminal check before stop check; offered-set computation with budget exclusion (3.3/3.4, `>=` compare); pre-execution failures (empty offered set, unresolvable handler, pre-dispatch stop, negative retry budget) fail with an explicit reason and write **no** checkpoint (tests assert the prior checkpoint is untouched and no stage dir exists); execution failures write a failure checkpoint naming the failed node as both `current_node` and `next_node` with `retry_visit=true`, `completed_nodes` untouched, sessions snapshotted from `Bindings()` (3.7, 5.3); routing violations (no choice among >1, unoffered `next`) checkpoint the same way; success checkpoints carry the resolved successor. Advance dispatches on `outcome.Next` first, falling through to the lone offered edge — a chooser naming the lone target is validated identically.
- **Retry (3.5) and backoff (3.6):** `max_retries` resolves node → file defaults → 0; `max_attempts = max_retries + 1`; only `retryable` retries; `terminal`/`interrupted` fail immediately (test proves backoff is never entered); fresh stage dir per attempt with engine-written `error.json`; visits counted once per dispatch, attempts per execution; no checkpoint between attempts (asserted from inside attempt 2); backoff is 200ms·2^(n−1) capped at 60s then jittered ×[0.5,1.5) (unit-tested at both bounds and at the 90s capped max); backoff sleep races the stop signal.
- **Stop (3.1, 4.1):** one-shot `StopSignal`; `Runner.Stop()` also calls `Backend.InterruptAll()`; observed pre-dispatch, before each attempt, and during backoff; stop-during-backoff writes a `retry_visit` failure checkpoint (visit was consumed) — all tested.
- **Tool handler (4.7):** runs `/bin/sh -c` in `scope.Workdir`, combined stdout/stderr to `tool.log`, exit-code routing for all five lint-legal shapes (including the advisory single-edge `on_fail` case), budget-exhausted mechanical route is a terminal error naming the budget, timeout and stop kill the whole process group promptly and return `interrupted` (descendant-kill verified by test).
- **Codergen handler (4.5, 5.4, 8):** prompt falls back to label, `$goal` expanded, `prompt.md` written before the backend call, byte-exact choice schema in both arms (no `next` for ≤1 offered; enum + condition-first route description with label-then-ID fallback for >1), full node→defaults→system resolution for model/provider/reasoning/fidelity/timeout, provider auto-detection, `compacted` and `high` defaults, thread key = `thread_id` else node ID and empty under `none` fidelity, backend Errors passed through unchanged (pointer-identity tested), `response.md` as frontmatter + notes, simulation behind a nil backend routing to the first offered target.
- **State (5.1–5.3):** typed `EngineState`, handlers receive only `ExecutionScope`; checkpoint shape matches 5.3 field-for-field; atomic rename replacement (hammered by a concurrent-reader test); `seq` recovery as max(checkpoint, stages scan) ignoring `latest/` and malformed entries; `retry_visit` consumed exactly once by the first `beginVisit` (unit-tested); exit node never dispatched or counted; `NewRunner` refuses to create run files before validation runs.
- **Negative retry-budget guard:** node or defaults `max_retries < 0` is rejected as a terminal pre-execution failure — no dispatch, no visit, no checkpoint (tested).
- **Registry wiring:** `NewRegistry()` installs `start` and `tool`; codergen is registered by the composer with its config (per the worklog's slice plan); `exit` is intentionally unregistered and unreachable (a test proves a registered exit handler is never invoked); replacement-on-reregister and custom type strings work as specified.

Out-of-segment machinery (parallel/fan-in, resume entry point, timeline emission, manifest, steering) is absent by plan; the building blocks that do exist for it (`loadCheckpoint`, `stateFromCheckpoint`, `appendTimeline`) are unit-tested and consistent with 5.3.

## Findings

No critical findings and no issues. Nitpicks only:

### N1 (nitpick, spec-fidelity) — stop between dispatch and first attempt diverges from the 3.2 pseudocode, in favor of the 3.1/3.7 prose
`engine/runner.go:268-277` — the visit increment lives inside `executeWithRetry` after the loop-top stop check, so a stop landing between the run loop's Step-1 check and attempt 1 returns `interrupted` with `attempted=false`: no visit counted, no failure checkpoint. The 3.2 pseudocode increments `node_visits` at Step 3 before `execute_with_retry` and always writes a failure checkpoint on a surviving error, which for this window would checkpoint `retry_visit=true`. The implementation's variant is the more defensible reading — 3.1/3.7 explicitly say a stop caught "while no handler is in flight" writes nothing because the last checkpoint already stands correct — and it is internally consistent (nothing consumed → fresh visit on resume) and pinned by `TestExecuteWithRetryStopBeforeFirstAttemptConsumesNothing`. Worth one worklog line noting the deliberate deviation from the pseudocode so a later reader doesn't "fix" it in either direction.

### N2 (nitpick, usability) — simulation mode demands a resolved model and provider
`engine/codergen.go:51-53` — `ValidateCodergenTurn` runs before the nil-backend branch, so a dry-run of a graph with no `llm_model` anywhere fails terminally ("model must not be empty" / "provider must not be empty") unless the composer supplies `CodergenConfig.DefaultModel`. The spec's simulation branch (4.5) never consults the model. The strictness is deliberate and tested (`TestCodergenHandlerRejectsInvalidResolvedTurnInSimulation`) and defensible under DoD 11.9 ("every codergen turn carries concrete resolved values"), but it makes the zero-config graph dry-run — simulation's main use — need a placeholder model. Also, the failure message when auto-detection fails names the provider, not the unrecognized model that caused it.

### N3 (nitpick, spec-fidelity) — tool notes tail and unlinted `on_fail` diagnostics
`engine/tool.go:110-117` — notes tail the combined stdout+stderr log rather than the spec's `tail(result.stdout or result.stderr)`; the combined form is arguably more useful and the spec's own `tool.log` is combined, so this is cosmetic. Separately, on an unlinted graph whose `on_fail` names a target that is not an outgoing edge, the handler reports "exit-code route X has exhausted its visit budget" — misleading for that defect (it was never an edge). Lint `tool_routing` makes both unreachable in linted graphs.

### N4 (nitpick, duplication) — default-resolution precedence implemented twice
`graph/parse.go:52-77` (`applyDefaults`/`inheritLLM` at parse time) and `engine/runner.go:305-330` + `engine/codergen.go:109-140` (engine-side node→defaults→system resolution) both encode the 2.7/8.2 precedence. For parsed graphs the engine's defaults branch is dead (fields are already inherited); it matters only for hand-constructed graphs. Harmless today and the results agree, but two codepaths encoding one precedence rule can drift — a candidate for consolidation when a later slice touches resolution.

### N5 (nitpick, scaffolding) — `appendTimeline` is engine-dead code this segment
`engine/store.go:93-109` — `timelineEvent`/`appendTimeline` are exercised only by tests; the runner emits no Section 10 events yet. Observability is explicitly a later slice, so this is forward scaffolding, not a gap against the reviewed DoD sections — noted so the observability slice knows the append path is already concurrency-tested.

## Outcome

**only nitpicks remain**
