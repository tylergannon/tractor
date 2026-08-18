# Adversarial review — spec migration and in-graph supervisors (round 04)

## Review target

The full working tree of the `spec-supervisors` worktree: the upstream
specification migration (graph language, lint, traversal, engine-owned run
logs), the in-graph supervision service, the Codex strict-output compatibility
layer, and the live proof claimed for that work.

Caller constraints honored: read-only outside this artifact; report written to
the requested path; rounds 01–03 untouched. The launch prompt supplied no
narrowing of defects, files, or subject matter, so nothing was ignored; scope
was derived from the authoritative sources below.

## Authorities

- `docs/spec.md`, re-verified byte-identical to `tylergannon/attractor`
  `origin/main` @ `0aca8b748e6ecc23446fc690d2b66690b77fe0d3` (`diff` clean).
- `ephemeral/projects/tractor/spec-supervisors-design.md` (decisions, sequence,
  proof claims).
- `ephemeral/projects/tractor/upstream-spec-audit.md`, `README.md` deviations,
  `ephemeral/worklog/202608171707-spec-supervisors.md`.
- Prior rounds `…-implementation-round-01/02/03.md`.

## Evidence inspected

- `engine/supervisor.go` (~600 lines) in full: patrol clock, `flush`,
  `recordVerdict`, `deliverSupervisorSteer`, briefing record, `stopAndWait`,
  `installBindingCallback`; plus `engine/runner.go:270-300, 347-356, 608-613`,
  `engine/state.go:64-79`.
- `harness/codex/schema_compat.go` (113 lines) and its call sites in
  `harness/codex/adapter.go` (`turnStartParams`, `RunTurn:177-181`).
- `cmd/harness-conformance/main.go` (`backend_supervisor` scenario registered
  at `:159`), `engine/supervisor_test.go` (14 tests), `examples/supervisor/`.
- Hygiene, re-run this round: `go build ./...`, `go vet ./...`,
  `go test ./...`, `go test -race ./engine/... ./harness/...` — all pass.
- Live proof: `/tmp/tractor-spec-supervisors.gE7swc/{codex,claude}-conformance.json`
  (8/8 scenarios pass, Codex CLI 0.147.0 and Claude Code 2.1.233);
  `/tmp/tractor-live-supervisor-logs.NmPg96/` timeline, `events/*.jsonl`
  segments, `supervisors/coach/{inbox.jsonl,briefed.json}`, `checkpoint.json`;
  in-repo `ephemeral/projects/tractor/conformance/*.json`.
- Round-03 findings 2, 3 and the `properties`-less half of finding 4 are
  confirmed addressed (live `backend_supervisor` scenario, shipped supervised
  example, fourth README deviation, audit entry at `upstream-spec-audit.md:15`,
  passthrough at `schema_compat.go:17-20`).

## Findings

### 1. critical — most of the design's live supervision proof claims were never run live

`spec-supervisors-design.md` sequence step 5 requires adding "the supervised
live/resume/shutdown/multi-level scenarios", and its "Proof claims" section
closes by stating that concurrency checks including `-race` "are required
checks rather than substitutes for the live CLI claims."

The only live supervised run is
`/tmp/tractor-live-supervisor-logs.NmPg96`, produced by
`examples/supervisor/live-steering.json`. It proves exactly one claim: a
delivered `steer` changed observable work. Every other supervised proof claim
is backed only by fake-backend unit tests:

- durable digests reaching a patrol — the timeline shows seven
  `SupervisorFlushed` events and **all seven carry `"count":0` with no
  `batch`** (`timeline.jsonl` lines 4, 7, 9, 11, 13, 15, 17). The single
  digest in `supervisors/coach/inbox.jsonl` was appended after `work`
  completed and was never rotated. The nudge payloads in
  `events/000002-coach.jsonl`…`000008-coach.jsonl` are all `"count": 0`, so
  the rotation/tally/batch path — the core §3.10 record path — has no live
  evidence at all;
- resume preserving binding, backlog and monotonic batch numbering — no live run;
- stop interrupting and awaiting supervisor turns — no live run;
- multi-level verdict/coaching digests — no live run; the example graph has a
  single supervisor;
- `current.jsonl` overlap behaviour during a supervisor turn — no live run.

The example is structurally incapable of closing the digest gap: `work` is the
only supervised node and the only stage that can emit an `outcome` digest, and
it emits it at completion, after which the patrol is torn down
(`PipelineCompleted` at `timeline.jsonl:24`, 0.5 s after the last verdict).
Impact: the worklog's `proof:` lines and the design's proof claims assert live
verification the artifacts do not support; the digest, rotation, resume, stop
and multi-level paths ship on unit-test evidence only, exactly what the design
said would not suffice.

### 2. issue — the claimed live proof exists only in volatile `/tmp`, and the in-repo conformance evidence is stale

All four `proof:` entries in `ephemeral/worklog/202608171707-spec-supervisors.md`
cite `/tmp/tractor-spec-supervisors.gE7swc/…`,
`/tmp/tractor-live-supervisor-work.J8lsLl`,
`/tmp/tractor-live-supervisor-logs.NmPg96/…`,
`/tmp/tractor-agent-{codex,claude}-work.*` — machine-local scratch paths that
no reviewer or successor can read. Meanwhile the repository's own preservation
discipline is visible and unfollowed: earlier phases preserved
`ephemeral/projects/tractor/live-examples/`, `final-validation/`,
`claude-live-proof.md`, and `conformance/`. The two in-repo conformance
reports, `conformance/claude-2.1.233.json` and `conformance/codex-0.147.0.json`
(both 17:05), still contain only the original **seven** scenarios; neither
lists `backend_supervisor`, the scenario this work added and claims passed.

Impact: the repository's audit trail says the supervisor backend seam is
unverified against real harnesses, while the only contradicting evidence is
outside the repository and will not survive a reboot. Round 03's proof gap is
reported as closed on evidence that cannot be re-checked.

### 3. issue — the binding-open callback writes a `CheckpointSaved` event that names no node

`engine/supervisor.go:531` emits
`timelineEvent{"type": "CheckpointSaved", "node_id": checkpoint.CurrentNode}`,
but `Checkpoint.CurrentNode` is the *last completed* node, whereas every walk
emission passes the node the checkpoint is about:
`engine/runner.go:355` takes an explicit `nodeID`, and `runner.go:272` saves
`state.checkpoint("", r.startID, …)` while reporting `node_id:"work"`.

Live consequence, `timeline.jsonl` lines 2 and 5: the same checkpoint content
is announced twice with two different identities — `{"node_id":"work"}` from
the walk and `{"node_id":""}` from the supervisor binding callback 2 s later.
Impact: `CheckpointSaved` (spec §10, `docs/spec.md:2007`) loses its single
field for supervisor-triggered saves; timeline consumers correlating
checkpoints to nodes see an unattributable event, and an operator cannot tell
which save refreshed the supervisor session binding.

### 4. issue — an advisory turn interrupted by normal finalization is recorded as an indistinguishable `error` verdict with no reason persisted

`stopAndWait` (`engine/supervisor.go:153-186`) calls `Backend.InterruptAll` in
a 10 ms loop until in-flight flushes return; the interrupted turn reaches
`recordVerdict` with a non-nil `runErr`, which sets `verdictName = "error"`
(`:453-456`) and writes `SupervisorVerdict{verdict:"error"}` (`:477`). The
error message is placed only in the digest forwarded to *parent* supervisors
(`:486-496`); with no parent — the ordinary single-supervisor case — it is
discarded. `recordError` is not called, so nothing lands in `errors.jsonl`.

Live evidence: `timeline.jsonl:23` records
`{"supervisor":"coach","verdict":"error"}` 0.5 s after the last stage
completed; `supervisors/coach/` contains only `briefed.json` and `inbox.jsonl`
(no `errors.jsonl`), and the corresponding segment
`events/000008-coach.jsonl` holds only the nudge and a zero-token usage line.
Impact: a fully successful run ends with an `error` verdict in its timeline,
and that record is byte-identical to a real supervisor failure (timeout,
schema violation, provider outage) with no recoverable reason anywhere on
disk — the shipped example reproduces this on every run.

### 5. nitpick — the Codex compatibility layer normalizes only root properties

`codexCompatibleOutputSchema` (`harness/codex/schema_compat.go:12-70`) walks
`root["properties"]` one level deep; nested object schemas, `$defs`/`$ref`
targets, `allOf`/`anyOf` branches and array `items` are left untouched, and
`omitOptionalNulls` (`:88-113`) likewise deletes only root-level nulls. Today's
in-repo callers (`verdictSchema`, `choiceSchema`, `cmd/agent`) are all flat, so
nothing is broken. But this sits on a public adapter seam documented in
`README.md` as a general deviation ("presents optional root properties as
required nullable fields"), and the first nested optional property will fail at
Codex with the same strict-output rejection this layer exists to prevent — with
no test or lint marking the boundary.

## Outcome

material findings remain
