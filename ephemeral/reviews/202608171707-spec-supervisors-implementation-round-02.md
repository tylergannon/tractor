# Adversarial review — spec migration and in-graph supervisors (round 02)

## Review target

Worktree `/Users/tyler/src/.worktrees/tractor/spec-supervisors`, HEAD `06b8621`
with 39 modified tracked files plus untracked `engine/supervisor.go`,
`engine/supervisor_test.go`, and `internal/`. The implementation was revised
after round 01 was delivered (`engine/supervisor.go` 523 → 596 lines,
`engine/supervisor_test.go` 327 → 632 lines, `engine/runner.go` re-touched);
this round re-reviews the current tree, not the round-01 tree.

## Authorities

- `docs/spec.md`, re-verified byte-for-byte identical to
  `/Users/tyler/src/attractor` `origin/main:docs/spec.md`
  (`0aca8b748e6ecc23446fc690d2b66690b77fe0d3`, 2532 lines) — normative.
- `README.md` (documented Tractor deviations) and repository instructions.
- `ephemeral/projects/tractor/spec-supervisors-design.md` (decisions, sequence,
  proof claims) and `ephemeral/projects/tractor/upstream-spec-audit.md`.
- `ephemeral/worklog/*` — this repository's own recorded empirical discoveries.

No caller instruction narrowed the subject matter; the read-only constraint and
the artifact path were honoured as stated. Scope was derived from the
authorities above, not from the launch prompt.

## Evidence inspected

- `engine/supervisor.go` in full; `engine/runner.go` (`Stop`, `Run`,
  `saveCheckpoint`, `executeWithRetry`, `bindings`, `allocateRunLog`);
  `engine/control.go` steering path; `engine/store.go` call sites.
- `engine/supervisor_test.go` in full (13 tests, including the new resume,
  fresh-binding-briefing, stop/finalize interrupt, oversized-line, error-record,
  and rotated-batch tests); `harness/backend_test.go` supervisor sections.
- `harness/backend.go`, `harness/contract.go`, `harness/codex/adapter.go`
  (`turnStartParams`), `harness/claude/adapter.go`, `cmd/harness-conformance/main.go`.
- `docs/spec.md` §§3.9, 3.10, 5.6, 7.2, 10, 11.2, 11.14, 12.1, 12.2, 12.4.
- `examples/` tree, `README.md`, `ephemeral/worklog/202608171707-spec-supervisors.md`.
- `go build ./...`, `go vet ./...`, `go test ./...`, and
  `go test -race ./engine/... ./harness/... ./internal/... ./lint/...` — all pass.

Round-01 findings 1 (briefing suppression keyed to turn-start evidence), 2
(silently swallowed supervision I/O errors), 4 (64 KiB scanner limit), and 5
(binding callback firing for every session) are fixed, each with a covering
test. Round-01 finding 3 is partly fixed (resume, stop/finalize, flush-event
shape, and out-of-scope tests now exist) and partly outstanding — see finding 3.

## Findings

### 1. critical — the spec's verdict schema is dispatched verbatim to Codex, which this repository has already recorded as rejecting it

`verdictSchema` (`engine/supervisor.go:393-402`) builds exactly the spec §3.10
schema: three declared properties (`verdict`, `target`, `message`) with
`"required": ["verdict"]` and `additionalProperties: false`. It is handed to
`harness.SupervisorTurn.OutputSchema` (`engine/supervisor.go:382, 387-389`) and
passed through the backend to the adapter unchanged; the Codex adapter forwards
it straight into the native turn
(`harness/codex/adapter.go:424-440`: `json.Unmarshal(input.OutputSchema, …)` →
`schema.TurnStartParams.OutputSchema`), with no normalization anywhere.

This repository's own live discovery says that construction is rejected:

> `ephemeral/worklog/202608161836-implement-attractor.md:32` — "Codex 0.147.0
> rejects native structured-output schemas unless the top-level `required`
> array includes every declared property".

That constraint is honoured for codergen turns — `engine/codergen.go:188-204`
always lists every declared property in `required` — and is honoured nowhere
for supervisors.

Failure scenario: any pipeline whose supervisor node resolves to the Codex
provider. Every patrol flush constructs this schema, `turn/start` is rejected by
the provider, `RunSupervisor` returns a terminal Error, and the advisory
contract absorbs it: `recordVerdict` writes `SupervisorVerdict{verdict:"error"}`
and the run continues (`engine/supervisor.go:364, 425-433`). The supervisor
never coaches anything for the entire run; batches rotate, tokens are spent on
nothing, and no test, event assertion, or exit code distinguishes this from a
healthy supervisor that keeps returning `ok`. In-graph supervision — the whole
point of this migration — is silently inert on one of the two supported
providers.

Nothing in the tree contradicts the recorded discovery, and nothing verifies it
either: every supervisor test uses the in-package `supervisorBackend` double
(`engine/supervisor_test.go:449-…`), and the live conformance controller has no
supervisor scenario (finding 3). One live Codex `run_supervisor` turn settles it
in a minute. If the rejection reproduces, the honest resolutions are to record
it in `ephemeral/projects/tractor/upstream-spec-audit.md` as an upstream spec
defect (the audit is silent on it today, 60 lines, no `required`/verdict entry)
and to surface it as a first-class, visible failure rather than an absorbed
advisory error — not to silently rewrite the normative schema.

### 2. issue — patrol clocks do not stop when the operator stop signal is set, so new supervisor turns start after Stop

Spec §3.10 (`docs/spec.md:1079-1081`): "When the stop signal is set, and at
Finalize, the patrol clocks stop and interrupts in-flight supervisor turns
through the ordinary `interrupt_all()` path".

`Runner.Stop` (`engine/runner.go:222-228`) sets `r.stop` and calls
`Backend.InterruptAll()` once. It does not touch the supervision service.
`patrol` (`engine/supervisor.go:189-204`) consults only `s.done` and
`s.stopping`, and both are set exclusively by `stopAndWait`
(`engine/supervisor.go:154-160`), which the runner calls only *after* the walk
returns (`engine/runner.go:285-287`). `r.stop` appears nowhere in
`engine/supervisor.go`.

Failure scenario: an operator stops a run whose in-scope work does not unwind
immediately — a `tool` node running a command to completion, a codergen turn
whose adapter finishes its current step after an interrupt, or a `parallel` step
whose branches are still draining. Those executions stay in the live registry,
so the next patrol tick after Stop still sees an in-scope entry, rotates a
batch, and dispatches a brand-new `run_supervisor` turn — possibly opening a new
supervisor session and triggering a checkpoint re-save — into a backend the
operator already interrupted. The single `InterruptAll()` in `Stop` fired before
that turn existed, so nothing interrupts it until the walk finally returns and
`stopAndWait`'s ticker loop begins. Tokens are spent, and a session is created,
after the run was told to stop.

`TestSupervisorStopAndFinalizeInterruptAndAwait`
(`engine/supervisor_test.go:207-…`) does not catch this: its handler returns
promptly after `Stop`, and the assertions cover only interrupt-and-await of the
turn already in flight, never "no new flush starts after the stop signal".

### 3. issue — supervision has no live proof, no example, and no backend-seam conformance scenario

`ephemeral/projects/tractor/spec-supervisors-design.md` step 5 requires live CLI
proof and supervised scenarios; spec §11.14 (`docs/spec.md:2215`) delegates
backend-seam supervisor conformance to §11.2, whose **Verdict conversion**
checklist item (`docs/spec.md:2182-2186`) requires that `run_supervisor` reuse
or open-and-bind the supervisor thread and convert a conforming object to a
native `Verdict` against a real harness.

Current state: `cmd/harness-conformance/main.go:147-157` registers seven
scenarios (`real_workspace_turn`, `continuation_and_reconstruction`,
`isolation_and_serialization`, `invalid_inputs_and_public_errors`, `steering`,
`interruption_and_timeout`, `compaction`) — none exercises `RunSupervisor`;
`grep -rn RunSupervisor cmd/` returns nothing. `examples/` contains only
`parallel`, `steering`, and `yaml`; there is no supervised pipeline, and
`examples/README.md` and the README's closing line advertise only the two
existing live workflows. `ephemeral/worklog/202608171707-spec-supervisors.md`
records decisions but no `proof:` line for supervision.

Impact: the entire supervision path has been proven only against an in-package
test double that cannot reject a schema, cannot refuse a session, and cannot
reproduce provider strictness. That is precisely why finding 1 is invisible and
finding 2 is untested. Every other feature in this repository carries a captured
live run (`ephemeral/worklog/202608170739-live-examples.md`); the headline
feature of this migration carries none.

### 4. nitpick — the README's documented-deviation list is stale and omits a new on-disk artifact

`README.md:17-21` still describes the briefing deviation as "A completed logged
supervisor turn suppresses a resend; missing or inconclusive completion evidence
resends" — the round-01 run-log-evidence design. The implementation no longer
uses run-log evidence: `supervisorNeedsBriefing`/`markSupervisorBriefed`
(`engine/supervisor.go:557-580`) suppress a resend when `briefed.json` records a
`ThreadBinding` equal to the backend's current binding for that supervisor, and
the marker is written only when the turn returned with `runErr == nil`
(`engine/supervisor.go:359-363`). The new rule is better; the documented one is
now wrong.

Related: `recordError` writes `{logs_root}/supervisors/{id}/errors.jsonl`
(`engine/supervisor.go:262-270`), a file the spec's directory layout
(`docs/spec.md:1831-1834`) does not define. It is a sound answer to round-01
finding 2, but it is a fourth deviation from a spec-defined layout and appears
in neither the README list nor the design doc.

### 5. nitpick — a batch rotated by a flush that then aborts is never named in any later nudge

`rotate` renames `inbox.jsonl` to a permanent numbered batch and commits
`nextBatch` before `digestTally` parses it (`engine/supervisor.go:272-292`). If
the tally fails, `flush` records `rotate_inbox` and returns
(`engine/supervisor.go:331-335`), and the same happens for a failed
`appendTimeline` (`:336-341`). The next flush rotates a *new* batch and the
nudge names only that one, so the abandoned batch's digests are never pointed at
again — only `errors.jsonl` mentions the path.
`TestSupervisorRotatedBatchFailureRemainsDiscoverable`
(`engine/supervisor_test.go:396-…`) locks this in as intended behaviour. Spec
§3.10 tolerates an unnamed batch after a turn Error ("still on disk for the next
patrol to find"), so this is defensible; carrying the pending-batch list into
the next nudge would make it whole. Also in this area, `renderNudge` discards its
marshal error (`engine/supervisor.go:413`, `raw, _ :=`), so a future
unmarshalable nudge field would silently send an empty payload.

## Outcome

material findings remain
