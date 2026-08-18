# Adversarial review — spec migration and in-graph supervisors (round 06)

## Review target

The current working tree of `github.com/tylergannon/tractor` at
`/Users/tyler/src/.worktrees/tractor/spec-supervisors`: the spec migration and
in-graph supervision work, its documentation, and its preserved proof. Scope was
derived from the authoritative sources below, not from the launch prompt. The
launch prompt supplied only operating constraints (read-only except the artifact,
artifact path) and imposed no narrowing of subject matter, so nothing was ignored.

## Authoritative sources

- `docs/spec.md` (byte-identical copy of upstream `attractor` `origin/main`
  @ `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`) — §3.10 supervision
  (`:985-1112`), §5.6 layout (`:1831-1834`), §10 event contracts (`:2007-2011`),
  §11.14 checklist (`:2218-2219`), §12.4 run-log pinning (`:2457-2472`).
- `README.md`, `ephemeral/projects/tractor/upstream-spec-audit.md`.
- Prior rounds 01–05 under `ephemeral/reviews/`.

## Evidence inspected

- `engine/supervisor.go` in full, with attention to what changed since round 05:
  `supervisorRuntime.deliveredThrough`/`nextBatch` (`:85-86`), `rotate` (`:294`),
  `pendingOffer` (`:316`), `mergeDigestTally` (`:338`), `acknowledge` (`:349`),
  `flush` (`:395-457`), `renderNudge` (`:495-527`), `supervisorBatchesAfter`
  (`:638`), `recoverSupervisorDelivery` (`:659`), recovery/clamp in
  `newSupervisionService` (`:142-158`).
- `engine/supervisor_test.go` watermark coverage (`:480-520`, `:600-640`),
  `engine/runner.go`, `harness/backend.go`, `lint/rules.go`,
  `harness/codex/schema_compat.go`.
- `README.md` (now five documented deviations) and
  `ephemeral/projects/tractor/upstream-spec-audit.md`.
- Preserved proof under `ephemeral/projects/tractor/spec-supervisors-proof/`:
  `REPORT.md`, and for each scenario the `pipeline.json`, `timeline.jsonl`,
  `events/*.jsonl` segments, `events/index.jsonl`, inbox batches, `briefed.json`,
  `delivered.json`, `errors.jsonl`, checkpoints, and workspace results;
  `ephemeral/projects/tractor/conformance/*.json` (8 scenarios each).
- File mtimes across the proof tree and `engine/supervisor.go`.
- Re-ran: `go build ./...` (ok), `go vet ./...` (ok), `go test ./...` (all
  packages ok), `go test -race ./engine/... ./harness/...` (ok).

Round-05 finding 2 (proof recorded outputs but not inputs) is resolved: every
scenario now preserves its `pipeline.json` and its run-log segments and index.
Round-04 findings 1–4 remain resolved.

## Findings

### 1. `delivered.json` is the engine tracking what the supervisor has read — the one thing §3.10 says it must not do (issue)

`docs/spec.md:1013-1021` is explicit and normative: "**Delivery is judgment, not
bookkeeping.** The engine maintains the files and the patrol clock; the
supervisor's session remembers what it has read, **and the engine does not track
that separately**. Resume needs no extra machinery… A missed or twice-read review
must never be the reason a run failed."

The new mechanism is precisely that separate tracking and precisely that extra
machinery: `supervisorDeliveryRecord`/`deliveredThrough` (`engine/supervisor.go:85-86`),
`recoverSupervisorDelivery` (`:659`), the atomic
`.delivered.json.tmp` → `delivered.json` write in `acknowledge` (`:349-365`), and
the resume-time clamp at `:146-151`. It also introduces an on-disk artifact that
the §5.6 supervisor directory layout does not list (`docs/spec.md:1831-1834`
enumerates only `inbox.jsonl` and `inbox.{batch}.jsonl`).

This was added to answer round-05 finding 1. On re-reading the source sentence I
judge that finding to have over-read it: "the first patrol that finds life flushes
the whole backlog, pre-crash lines included" describes unflushed `inbox.jsonl`
lines, not previously rotated batches, and the same paragraph pre-authorizes the
loss it was worried about ("a missed … review must never be the reason a run
failed"). The spec's answer to a withheld batch is the supervisor's own judgment
over permanent batch files, not engine bookkeeping. My round-05 finding was the
weaker reading; this round I am correcting it rather than defending it.

The behavior the watermark produces is safe, and `README.md` documents it as the
fifth deviation. But `ephemeral/projects/tractor/upstream-spec-audit.md` — the
project's own register of spec tensions, which does carry the binding-callback and
Codex verdict-schema entries — has no entry for it (`grep -i 'delivered\|watermark'`
returns nothing). A deviation from an explicit normative sentence recorded in the
README but absent from the audit is the case the audit exists to catch.

Impact: knowing conformance divergence from a directly-quotable spec sentence,
plus an unregistered artifact in the mandated layout, plus a gap in the project's
own deviation register.

### 2. The nudge now carries a batch path, tally, and non-zero count on patrols where no batch was rotated (issue)

`docs/spec.md:996-1000` defines the nudge as the in-scope snapshot entries "plus
the batch path and a per-node, per-disposition tally **when a batch was rotated**"
(singular batch path).

`flush` calls `pendingOffer` unconditionally (`engine/supervisor.go:419`), which
returns every batch above the watermark regardless of whether this patrol rotated
anything (`:316-335`). `renderNudge` then sets `Batch` to the newest pending batch
and, when more than one is pending, adds a `batches` array (`:504-510`), with
`count` and `tally` merged across all of them (`:326-332`, `:338-347`).

So a patrol that rotates nothing (empty inbox, all activity mid-turn) can still
emit `batch`, `tally`, and `count > 0` — the exact case the spec pairs with an
absent batch. The paired timeline event correctly reports the rotation
(`SupervisorFlushed.count = rotatedCount`, `:411`, matching `docs/spec.md:2007-2009`),
which means the §10 record and the message the supervisor actually saw now disagree
about `count` and about whether a batch existed. An observer reconstructing
supervision from `timeline.jsonl` — the §10 use case — reads the wrong thing.

Secondary exposure on the same path: if `delivered.json` is unreadable or
corrupt, `newSupervisionService` resets `deliveredThrough` to `0` (`:147-151`) and
the next nudge enumerates the entire batch history in `batches`, unbounded.

### 3. The resume, hierarchy, and stop proofs were produced by a binary that predates the code they exercise (issue)

`engine/supervisor.go` was last modified at 19:33:54; `README.md` at 19:33:52.
The live artifacts for three of the five scenarios predate that:
`resume/timeline.jsonl`, `hierarchy/timeline.jsonl`, `stop/timeline.jsonl`, and
both `conformance/*.json` are all stamped 19:19:14. Only `live-steering/` was
re-run afterwards (19:35), and it is the only scenario with a `delivered.json`.
The `events/` segments added to the older scenarios (19:27-19:28) were copied out
of the same pre-change run.

This matters most for `resume`, the scenario the change directly rewrites.
`resume/timeline.jsonl:7,19` shows `inbox.000001.jsonl` flushed pre-crash and
`inbox.000002.jsonl` post-resume, with no re-offer of batch 1 — which is the *old*
behavior. Under the current code `recoverSupervisorDelivery` finds no
`delivered.json`, sets `deliveredThrough = 0`, and the first post-resume patrol
re-offers `inbox.000001.jsonl` alongside `000002`. The preserved evidence
therefore documents behavior the shipped binary no longer has.

`REPORT.md` states "All evidence was produced by binaries built from this worktree
on 2026-08-17" and describes the resume backlog behavior in the present tense,
without disclosing the split build. Its only support for the new mechanism is
"Unit and race tests additionally … prove that every unacknowledged batch plus new
inbox backlog is re-offered before `delivered.json` advances" — which is true
(`engine/supervisor_test.go:480-520`, `:600-640`) but is unit coverage presented
inside a live-proof report. The §11.14 resume/briefing claims (`docs/spec.md:2218`)
are consequently unproven live against current code.

### 4. `flush` discards the rotate/tally error whenever a batch file was created (nitpick)

```go
batch, rotatedCount, _, err := runtime.rotate()
if err != nil {
    if batch == "" { runtime.recordError("rotate_inbox", err, ""); return }
}
```
(`engine/supervisor.go:405-410`) — when the rename succeeded but `digestTally`
failed, `err` is silently dropped and `rotatedCount` may be a partial count that
is then published as `SupervisorFlushed.count`. In practice `pendingOffer` re-reads
the same file and records a `tally_batch` error for it, so the diagnostic is not
wholly lost; the published count can still be short. Recording the error at the
point it occurs, or recomputing the count, would remove the ambiguity.

### 5. A run-log segment and index line are allocated before every abort path in `flush` (nitpick)

`s.runner.runLogs.Allocate` runs first (`engine/supervisor.go:399`), before the
rotate, timeline-append, pending-discovery, and render steps that each `return`
early (`:407`, `:417`, `:421`, `:432`). Any of those leaves an indexed segment in
`events/index.jsonl` that no turn ever wrote to. Carried unchanged from round 05;
the preserved proof shows no orphaned segments, so this remains latent.

Also carried from earlier rounds: `harness/codex/schema_compat.go` normalizes only
root-level properties, so a nested object with a partial `required` list would
still be rejected by Codex 0.147.0. No current schema exercises this.

## Outcome

material findings remain
