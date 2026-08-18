# Adversarial review — spec migration and in-graph supervisors (round 05)

## Review target

The full working tree of the `spec-supervisors` worktree: the upstream
specification migration (graph language, schema generation, lint, traversal,
engine-owned run logs), the in-graph supervision service, the backend
supervisor seam and Codex strict-output compatibility layer, and the live proof
now preserved under `ephemeral/projects/tractor/spec-supervisors-proof/`.

Caller constraints honored: read-only outside this artifact; report written to
the requested path; rounds 01–04 untouched. The launch prompt supplied no
narrowing of defects, files, or subject matter, so nothing was ignored; scope
was derived from the authoritative sources below.

## Authorities

- `docs/spec.md`, re-verified byte-identical to `tylergannon/attractor`
  `origin/main` @ `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`.
- `ephemeral/projects/tractor/spec-supervisors-design.md` (decisions, sequence,
  proof claims), `upstream-spec-audit.md`, `README.md` deviations,
  `ephemeral/worklog/202608171707-spec-supervisors.md`.
- Prior rounds `…-implementation-round-01/02/03/04.md`.

## Evidence inspected

- `engine/supervisor.go` in full (patrol, `flush`, `rotate`/`digestTally`,
  `appendAttempt`, `renderNudge`, `verdictSchema`, `recordVerdict`,
  `deliverSupervisorSteer`, briefing record, `stopAndWait`,
  `installBindingCallback`), plus `engine/runner.go:270-300, 347-356, 409-513,
  640-660`, `engine/control.go:130-176`, `engine/state.go`,
  `internal/runlog/allocator.go`.
- `harness/backend.go` in full for the seam: `Run`, `RunSupervisor`,
  `prepareBinding`/`prepareSupervisorBinding`, `startTurnLog`/`finishTurnLog`
  and `current.jsonl` pinning, `Steer`, `InterruptAll`, `decodeVerdict`;
  `harness/validate.go`, `harness/codex/schema_compat.go`,
  `harness/codex/adapter.go`.
- `lint/rules.go` supervision rules (`supervises_valid`,
  `supervisor_not_targeted`, `supervisor_cycle`) and `lint/analysis.go`
  routing-edge construction, checked against `docs/spec.md:1875-1898`.
- Hygiene re-run this round: `go build ./...`, `go vet ./...`,
  `go test ./...`, `go test -race ./engine/... ./harness/...` — all pass.
- Preserved proof: `spec-supervisors-proof/REPORT.md` and every artifact under
  `live-steering/`, `resume/`, `hierarchy/`, `stop/`, `agent/`; the refreshed
  eight-scenario `conformance/{codex-0.147.0,claude-2.1.233}.json`;
  `examples/supervisor/` and `examples/README.md`.

Round-04 findings 1–4 are confirmed addressed: the live proof set now covers
resume, multi-level supervision, operator stop, batch rotation with `count: 1`,
and `current.jsonl` overlap, and it is preserved in-repo alongside refreshed
conformance reports; `engine/supervisor.go:532` now names the supervisor thread
key in its `CheckpointSaved` event; `engine/supervisor.go:454` now records the
turn-error reason in `errors.jsonl` (`live-steering/errors.jsonl`,
`stop/errors.jsonl`). Spot-checks that came back clean this round include the
`current.jsonl` pinning rule (`harness/backend.go:410-440` matches
`docs/spec.md:2463-2472` exactly, including staying pinned until the live count
returns to zero), fidelity `none` never reusing a stored session
(`harness/backend.go:100-102, 220`), tool executions registered without a
segment path (`engine/runner.go:453, 640-643`), and digest emission after the
stage artifact is finalized on both the success and failure paths
(`engine/runner.go:466, 486`).

## Findings

### 1. issue — a rotated batch can be permanently withheld from the supervisor, contrary to the spec's backlog guarantee

`docs/spec.md:1013-1021` guarantees that "the first patrol that finds life
flushes the whole backlog, pre-crash lines included", excusing exactly one
degenerate case: "a zero-length `inbox.jsonl` … counts as empty".

`flush` rotates first and can then abandon the batch it just detached:

- `engine/supervisor.go:333-337` — `rotate()` renames `inbox.jsonl` to
  `inbox.{n}.jsonl` and only afterwards runs `digestTally`; a partially written
  final line (precisely the crash-mid-append case the spec contemplates, and
  not zero-length) makes `json.Decoder.Decode` fail, so `flush` records
  `rotate_inbox` and returns;
- `engine/supervisor.go:342-345` and `:351-354` — a `timeline.jsonl` append
  failure or a nudge render failure returns after the rename as well;
- `engine/supervisor.go:361-364` — an operator stop landing between rotation
  and dispatch returns without a turn.

Because a nudge only ever names the batch rotated by *that* flush
(`renderNudge`, `engine/supervisor.go:412-419`), the abandoned batch is never
named by any later nudge, in this run or after a resume: `recoverSupervisorBatch`
restores only the numbering, not the unread batch. The digests remain on disk
but leave supervision's view forever. `TestSupervisorRotatedBatchFailureRemains
Discoverable` (`engine/supervisor_test.go:466-497`) pins the current behaviour,
so this is a deliberate design point rather than an oversight — but it is
narrower than what the spec promises, and the stop variant is an ordinary
operator action, not a crash. Impact: after a Ctrl-C during a flush, a resumed
run's supervisor silently never sees the pre-stop attempt digests it was
promised.

### 2. issue — the preserved proof records outputs but not inputs, and no supervisor run-log segment survives, so several stated claims remain unauditable

`spec-supervisors-proof/REPORT.md` asserts, among others, "Across all
supervisor segments, the briefing occurred once" and "During the overlapping
worker and supervisor turns, `current.jsonl` was observed pointing to
`events/index.jsonl`"; the worklog adds "did not resend the briefing". The
directory contains no `events/` segment from any scenario, so the nudge
payload, the briefing text, and the once-per-session property — the substance
of the spec checklist item at `docs/spec.md:2218` ("carry the nudge … with the
briefing prepended exactly once per session") — have no preserved artifact at
all. `live-steering/current-overlap.txt` is a bare one-line string
(`events/index.jsonl`) with no timestamp tying it to the overlap window, and
`live-steering/` preserves no `briefed.json` even though
`examples/supervisor/README.md` lists that record as a success criterion.

Equally, three of the four supervised scenarios (`resume/`, `hierarchy/`,
`stop/`) were driven by pipelines that exist nowhere in the repository: only
`examples/supervisor/live-steering.json` is shipped, and `REPORT.md` records no
commands. Impact: the strongest new claims — resume without re-briefing,
multi-level flow, stop interrupting an in-flight turn — cannot be re-run or
independently checked from the repository, which is the gap round 04 raised and
only partially closed.

### 3. nitpick — every clean supervised run ends with a `SupervisorVerdict verdict="error"`

`stopAndWait` interrupts in-flight flushes at normal finalization
(`engine/supervisor.go:153-186`), and the interrupted turn is recorded as
`verdict: "error"` (`:453-456, :477`). All four preserved timelines show it on
otherwise successful runs (`live-steering/timeline.jsonl:23`,
`resume/timeline.jsonl:27`, `hierarchy/timeline.jsonl` twice at 01:17:46). This
follows `docs/spec.md:1076-1085` literally, and the reason is now recoverable
from `errors.jsonl`, but at the timeline level a routine shutdown is
indistinguishable from a genuine supervisor failure, and the shipped example
reproduces it on every run.

### 4. nitpick — a run-log segment and index line are allocated before every abort path in `flush`

`engine/supervisor.go:328` allocates (creating the segment file and appending
its `events/index.jsonl` line) before the rotate, timeline, briefing-read,
render, and stop checks at `:333, :342, :351, :357, :361`. Each abort leaves an
empty numbered segment and an index entry for a turn that never ran, so
`events/index.jsonl` — described at `docs/spec.md:2457-2462` as the discovery
manifest of segments as they are born — gains entries no watcher will ever see
content for. Carried unchanged from round 03.

### 5. nitpick — the Codex compatibility layer still normalizes only root properties

`codexCompatibleOutputSchema` (`harness/codex/schema_compat.go:12-70`) and
`omitOptionalNulls` (`:88-113`) handle one level: nested object schemas,
`$defs`/`$ref`, `allOf`/`anyOf` branches and array `items` are untouched. Every
in-repo caller is flat, so nothing is broken today, but this is a public
adapter seam documented in `README.md` as a general deviation, and the first
nested optional property will hit the same Codex strict-output rejection the
layer exists to prevent, with no test or comment marking the boundary. Carried
unchanged from round 04.

## Outcome

material findings remain
