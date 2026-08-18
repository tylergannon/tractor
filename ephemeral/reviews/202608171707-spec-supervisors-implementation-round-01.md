# Adversarial review — spec migration and in-graph supervisors, implementation round 01

**Target:** working tree of `codex/spec-supervisors` (worktree
`/Users/tyler/src/.worktrees/tractor/spec-supervisors`, HEAD `06b8621`) — the
uncommitted spec migration plus the new `engine/supervisor.go`,
`internal/runlog`, backend supervisor seam, graph/lint/CLI changes.

**Authorities consulted**

- Upstream `tylergannon/attractor` `origin/main:docs/spec.md` (`0aca8b7`,
  2532 lines), read in full for Sections 2.4–2.7, 3.9, 3.10, 4.1, 4.6–4.7,
  5.3, 5.6, 7.2, 10, 11.2–11.3, 12.1–12.4.
- Repository instructions (`README.md`; no `CLAUDE.md`/`AGENTS.md` in this repo).
- `ephemeral/projects/tractor/spec-supervisors-design.md` and
  `ephemeral/projects/tractor/upstream-spec-audit.md`.

**Evidence inspected**

- `diff origin/main:docs/spec.md` vs `docs/spec.md` → byte-for-byte identical
  (design decision 1 and README claim verified).
- Full read: `engine/supervisor.go`, `engine/runner.go`, `engine/control.go`,
  `engine/store.go`, `engine/codergen.go`, `engine/parallel.go`,
  `engine/fan_in.go` (head), `harness/backend.go`, `harness/contract.go`,
  `harness/validate.go`, `internal/runlog/allocator.go`, `graph/graph.go`,
  `graph/parse.go`, `graph/schema.go`, `lint/rules.go`, `lint/analysis.go`,
  `lint/lint.go`, `cmd/tractor/root.go`, `engine/supervisor_test.go`,
  `harness/claude/adapter.go` and `harness/codex/adapter.go` (turn paths).
- Generated `graph/jsonschema/Graph.json` supervisor branch (required
  `type,id,prompt,supervises`; closed object; duration pattern) — conforms to
  Section 2.5.
- Lint rule registry cross-checked against the full Section 7.2 table: all 27
  rule IDs present with matching severities.
- `go build ./...`, `go vet ./...`, `go test ./...`, and
  `go test -race ./engine/... ./harness/... ./internal/...` — all clean.
- Repository search for supervised examples / CLI coverage / live proof
  artifacts under `examples/`, `cmd/`, `ephemeral/projects/tractor/`.

No caller instruction narrowed the subject matter; the launch prompt supplied
only a read-only boundary and an artifact path, both of which are valid
operating constraints and were honoured (this file is the only write).

Overall the migration is faithful: the spec copy is exact, the typed node
schema and the 27 lint rules match Section 7.2, the run-log allocator
implements the Section 12.4 allocate→create→index→publish→dispatch order and
the sticky `current.jsonl` pinning rule, digest append/rotation is correctly
serialized under one mutex, and the steerable-vs-live turn split matches
Section 12.1. The findings below are the residue.

---

## Finding 1 — Briefing suppression is keyed to a marker file written on turn *start* evidence, so an unbriefed supervisor session runs silently for the rest of the run (critical)

`engine/supervisor.go:313`, `:326-328`, `:491-499`.

```go
briefing := !supervisorBriefed(runtime.dir)                    // :313
...
verdict, runErr := s.runner.config.Backend.RunSupervisor(turn) // :324
s.runner.endExecution(liveID)
if briefing && runLogHasEvents(segment.Path) {                 // :326
    _ = os.WriteFile(filepath.Join(runtime.dir, "briefed"), ...)
}
```

`runLogHasEvents` is `os.Stat(path).Size() > 0`. Both shipped adapters write
the `user` event into the segment *before the turn is awaited and before the
model has seen anything*: `harness/claude/adapter.go:159`
(`projector.user(input.Parts)` precedes `session.Send`) and
`harness/codex/adapter.go:170` (precedes `waitTurn`). A non-empty segment
therefore proves only "the turn was started", never "a completed supervisor
turn containing the briefing", which is exactly the rule the design
(`spec-supervisors-design.md`, briefing decision) and `README.md` commit to:
"A completed logged supervisor turn suppresses a resend; missing or
inconclusive completion evidence resends the same briefing."

**Reproduction A (interrupt, no crash needed).** A supervisor's first flush
turn is in flight when the operator stops the run or the walk finalizes.
`stopAndWait` (`:135-165`) drives `Backend.InterruptAll()`; the adapter returns
an `interrupted` Error. The segment already holds the `user` line, so `briefed`
is written at `:327` even though `runErr != nil`. Resume (`tractor run
--resume`) restores the supervisor binding from `checkpoint.sessions`, so the
same session is reclaimed — and every subsequent nudge now omits the briefing
(`renderNudge`, `:377`). That session never received the expanded `prompt`, the
run goal, the verdict contract, or its directory location; it only ever sees
`"Patrol nudge:\n{live,batch,count,tally}"`. Its verdicts are produced with no
instructions at all, yet they are delivered as real steering to walk nodes
(`deliverSupervisorSteer`, `:437`). The same applies with `session.Send`
failing on the Claude path: the user event is on disk, the message never
reached the harness, and the supervisor is marked briefed.

This directly contradicts Section 3.10 ("The first flush of a session prepends
the briefing"), conformance item 11.3 bullet 2 ("with the briefing prepended
exactly once per session"), and the design's own proof claim: "it never
continues an unbriefed supervisor silently."

**Reproduction B (marker outlives the session).** The marker is scoped to
`{logs_root}/supervisors/{id}/`, not to the binding. Section 3.10 requires:
"If no binding exists at resume (the crash beat that save, or the supervisor
never woke), that patrol is simply a first flush: fresh session, briefing
resent." Start a run with a supervisor that flushes at least once, then start a
second run over the same `--logs` root *without* `--resume`:
`cmd/tractor/root.go:150-155` leaves `bindings` nil, so
`prepareSupervisorBinding` (`harness/backend.go:243`) opens a brand-new
session — while `briefed` survives on disk from the previous run and suppresses
the briefing for the entire second run. The engine has no code path that ever
removes or invalidates the marker (`grep briefed` → only `:327` and `:492`).

**Impact.** Advisory machinery must never fail the run, and it does not — but a
silently unbriefed supervisor is worse than a supervisor that never woke: it
spends real turns and injects real steering into live walk turns with no
coaching brief behind it. The correct evidence is already available in-process:
suppress only when `runErr == nil` for a turn that carried the briefing, and
key the record to the binding (`Backend.Bindings()[node.ID]`) rather than to
the directory.

## Finding 2 — Supervision I/O failures are swallowed with no record anywhere, contrary to the design's "record and absorb" decision (issue)

`engine/supervisor.go:229`, `:295-311`, `:427`.

- `appendAttempt` discards every append error: `_ = runtime.append(digest)`
  (`:229`). Section 3.10 requires "Appends and batch rotation must not lose or
  duplicate a line under concurrency"; a digest lost to `ENOSPC`/`EACCES`
  leaves no trace in `timeline.jsonl`, in the inbox, or in the run result. The
  supervisor's subsequent tally is silently short.
- `flush` returns bare on five distinct failures — segment allocation (`:295`),
  inbox rotation (`:299`), the `SupervisorFlushed` append (`:303`), turn
  construction (`:310`), and the stopping race (`:322`) — emitting nothing.
- `recordVerdict` discards its timeline append too (`:427`).

The design decision reads "Record **and** absorb supervision-side I/O and turn
failures"; only turn failures are recorded (as `SupervisorVerdict`
`verdict="error"`). Absorption is implemented, recording is not. The rotation
path is the sharpest case: `rotate()` (`:246-262`) has already renamed
`inbox.jsonl` to `inbox.{batch}.jsonl` and bumped `nextBatch` before
`digestTally` runs, so a failure at `:299` or `:303` orphans a permanent batch
file that no nudge will ever name and no event will ever mention — the
external observer's audit trail (Section 3.10, "the observer's audit of what
supervision saw") has a hole with no marker in it.

## Finding 3 — The supervision paths the design's own proof claims target are untested and unproven (issue)

`engine/supervisor_test.go` (327 lines) covers: live steer + record, quiet
scope, non-overlapping flushes, lossless rotation, multi-level verdict/coaching,
malformed-steer degradation. It does not cover:

- **Resume** — no test constructs a `ResumeRunner` with a supervisor. Design
  proof claims 7 and 8 ("A resumed run preserves supervisor session binding,
  unflushed backlog, and monotonic batch numbering"; "A resume from the
  briefing-delivery window either finds a completed briefing turn or observably
  resends the same idempotent briefing") and conformance item 11.3 bullet 4 are
  unexercised. Finding 1 lives precisely here.
- **Stop / Finalize** — `stopAndWait`'s interrupt-and-await loop (`:135-165`),
  the only place `InterruptAll` is driven for supervisors, has no test.
  `supervisorBackend.InterruptAll()` in the test double is a no-op, so the
  stopping races at `:169`, `:294` and `:321` are never taken. Proof claim 9
  and 11.3 bullet 5 are unproven.
- **`SupervisorFlushed` field shape** — `assertSupervisorTimeline` only checks
  `SupervisorVerdict`. Section 10's `batch`/`count` contract (absent `batch`
  and `count == 0` on an empty inbox) is asserted nowhere.
- **Out-of-scope attempts append nothing** — 11.3 bullet 1's negative case.
- **CLI / live** — no supervised pipeline exists under `examples/`, no
  supervisor test in `cmd/tractor/root_test.go`, and
  `ephemeral/projects/tractor/` contains no supervised run artifacts. Design
  sequence steps 5 and 6 are outstanding.

Sequencing caveat: the design puts live proof at step 5 and the code is at
step 4, so this is a completeness gap rather than a regression. It is reported
because the critical defect above sits inside the specific coverage hole.

## Finding 4 — A single oversized digest line permanently drops one batch from the nudge stream (nitpick)

`engine/supervisor.go:271-283`. `digestTally` reads the rotated batch with a
default `bufio.Scanner`, whose token limit is 64 KiB. `attemptDigest.Notes`
carries `outcome.Notes` verbatim — free-form model text with no schema bound
(`engine/codergen.go:198`, `"Your account of this stage."`) — and
`attemptDigest.Message` carries an unbounded error message. One line past the
limit makes `rotate()` return `bufio.Scanner: token too long`; `flush` returns
at `:299` with the batch already renamed and `nextBatch` already advanced, so
that batch is never named in any nudge and no `SupervisorFlushed` event
records it. Use `scanner.Buffer` with a generous cap, or count lines without
materialising them.

## Finding 5 — The binding-open checkpoint re-save fires for every backend session, not only supervisor sessions (nitpick)

`engine/supervisor.go:455-471` installs the callback unconditionally, and
`harness/backend.go:271-289` invokes it from `publishBinding` for *all* new
bindings — every codergen thread, every `none`-fidelity visit, and every
parallel branch session. Section 3.10 asks for the re-save only because "a
flush runs concurrently with the walk, [so] the ordinary per-execution snapshot
cannot make that first binding durable"; walk turns are already covered by the
ordinary save. The extra writes are harmless (the callback re-saves
`r.lastCheckpoint` with a refreshed `sessions` map, and lock ordering is sound
— `bindingLock → checkpointMu → b.mu`, with no inverse path), but a fan-out
over N branches now performs N additional atomic checkpoint replacements on the
walk's hot path. Gating on `harness.ThreadBinding` keys that name supervisor
nodes would match the spec and remove the write amplification.

---

## Outcome

`material findings remain`
