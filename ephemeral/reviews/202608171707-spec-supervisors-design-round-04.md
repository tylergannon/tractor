# Adversarial review — spec migration and supervisors design, round 04

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (41 lines, md5
`164c8f71d2b6a7f607e1c451ffcea446`), together with the companion artifact it
references at line 29, `ephemeral/projects/tractor/upstream-spec-audit.md`
(59 lines).

Authorities used, unchanged from rounds 01–03:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`
  (`git show origin/main:docs/spec.md` from `/Users/tyler/src/attractor`,
  2532 lines), the document the design pins as normative;
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `lefthook.yml`;
- the standing project correction in
  `ephemeral/worklog/202608171707-spec-supervisors.md` — designs stay skeletal
  (decisions, ownership, sequencing; no code or pseudo-code) — and its decisions
  pinning authority, keeping YAML as a Tractor extension, refusing compatibility
  layers, replacing the local `docs/spec.md` byte-for-byte, and recording
  upstream defects without repairing the authority;
- the current implementation (see evidence below).

The launch prompt supplied only the target, the read-only boundary, and the
artifact path. It did not narrow defects, files, or subject matter, predict
findings, declare safe areas, or request a verdict, so no instruction was
refused. Scope was derived from the authorities above and covers the whole
current design, not only the deltas since round 03.

## Round-03 findings status

All four round-03 findings are addressed in the current artifact:

1. Supervision-side I/O and turn failure absorption — line 13.
2. Run-log allocator ownership without a hidden backend fallback — line 16
   (`internal/runlog`, engine and standalone `agent` CLI as the two callers).
3. Unknown node types as parse errors rather than a catch-all — line 9.
4. Concurrency checks positioned as required checks rather than substitutes for
   live proof — line 41.

None regressed, and none of this round's findings restate them.

## Evidence inspected

- Upstream spec §3.10 (lines 960–1120: patrol tick, nudge composition, briefing
  contents and once-per-session rule, verdict contract and its engine-side
  degradation, sessions-and-resume paragraph at 1085–1098, multi-level verdict
  digests), §3.9 external steering, §5.3 checkpoint `sessions`, §12.1
  `CodergenBackend` interface block (1272–1360: `run`, `run_supervisor`, `steer`,
  `interrupt_all`, `bindings`; `SupervisorTurn`; conversion rules), §12.4 run-log
  segments, §10 timeline events (`SupervisorVerdict` at 2011), and the
  acceptance checklist items at 2218–2220.
- Implementation: `engine/runner.go`, `engine/control.go`, `engine/store.go`,
  `engine/parallel.go`, `harness/backend.go`, `graph/graph.go`, `graph/parse.go`,
  `graph/internal/schemafix/main.go`, `lint/rules.go`, `lint/lint.go`,
  `cmd/agent/main.go`.
- Precedent design `ephemeral/projects/tractor/design.md` (sequence step 4 and
  its Done section) for how this repository has previously terminated a design.
- Prior rounds `ephemeral/reviews/202608171707-spec-supervisors-design-round-0{1,2,3}.md`.

## Findings

### 1. issue — the team's own Critical upstream defect has no decision in the design

`ephemeral/projects/tractor/upstream-spec-audit.md:7-11` classifies "Supervisor
session durability cannot meet the stated contract" as Critical: the spec's
`CodergenBackend` (spec 1272–1360) exposes only a blocking `run_supervisor` and
a snapshot `bindings`, with no binding-open notification and no engine-owned
open operation, while §3.10 (spec 1085–1092) requires that "session open
triggers a checkpoint re-save … serialized with ordinary saves" precisely
because "the ordinary per-execution snapshot cannot make that first binding
durable".

The design does not resolve what Tractor builds in the face of that. Line 17
asserts supervisor sessions are "checkpointed when first bound", and proof
claim 36 asserts a resumed run "serializes the binding-triggered checkpoint save
with walk saves" — but with the pinned interface the engine cannot observe the
bind until the blocking turn returns, which is after the window the spec is
trying to cover. Exactly two outcomes are available, and each is a design
decision that is currently unrecorded:

- extend the backend seam (a callback, an engine-owned open, or a bind channel)
  — a deviation from the pinned authority, of the same kind the design *does*
  record for run-log allocation at line 15 ("Move run-log allocation to the
  engine role"); or
- accept a weaker guarantee (re-save after the turn returns), which makes
  claim 36 unprovable as written for the crash window it names.

The same paragraph's second half is a genuine spec hole and is likewise
unaddressed: §3.10 resends the briefing only when *no* binding exists at resume
(spec 1094–1097), so a crash after the binding save but before or during prompt
delivery yields a reclaimed session that never receives its briefing, and the
engine deliberately does not track brief delivery separately. The worklog policy
is to record upstream defects rather than repair the authority — which is
correct — but that policy converts a self-classified Critical into shipped
Tractor behaviour, and the design records neither the acceptance nor the
mitigation.

Impact: the artifact that decides what gets built is silent on the one defect
the project itself rated Critical, and one proof claim asserts a property whose
mechanism does not exist in the pinned interface. Implementers will either
invent a seam extension unreviewed or ship a claim they cannot demonstrate.

### 2. issue — the sequence drops the independent-validation phase the repository requires

`skills/orchestrate-attractor-loops/SKILL.md:35-40` makes independent validation
a distinct, mandatory phase: "Give a fresh validation agent the built artifact
and user-level task, not the intended answer or internal implementation notes",
"Require non-trivial use through the real entry point and every required
provider or mode", and "Passing tests never overrides failed live proof". It is
separate from the review loop at SKILL.md:20-24, and the repository's own prior
design ends with it (`ephemeral/projects/tractor/design.md:31` — "then have a
fresh agent use the real CLI for non-trivial work through both harnesses" — and
its Done section naming "fresh independent use" as acceptance proof).

Design line 27, the terminal step, is "Prove parsing/routing and supervision
through the real `tractor` CLI, then run full hygiene and independent consensus
review." That covers hygiene and review; it schedules no fresh-agent validation
phase and names no provider/mode coverage for it. Proof claim 39 ("Existing
Codex and Claude harness entry points still perform non-trivial workspace work
through the real CLI") states the property but is a claim, not a step, and the
sequence assigns no owner or point at which it is exercised by someone who did
not build the thing.

Impact: the work as sequenced terminates at self-run proof plus review, one
phase short of the repository's acceptance bar, and the omission is invisible
because a similarly-worded step ("independent consensus review") occupies the
final slot.

### 3. nitpick — verdict degradation is unassigned between backend and engine

§3.10 is explicit that the verdict's conditional rules "are enforced by the
engine after decoding, not by JSON Schema conditionals", and that "a malformed
`steer` (no `target`, out-of-scope `target`, blank `message`) degrades to `ok`
and is recorded as such" — corroborated by the `SupervisorVerdict` event at spec
2011, where "a malformed steer records the `ok` it degraded to". §12.1's
conversion step instead turns a non-conforming adapter object into a terminal
Error.

Design line 17 assigns "verdict conversion" to the backend (matching §12.1's
word) and line 11 gives the supervision service "verdict delivery"; neither
names the engine-side degradation. If an implementer folds the conditional rules
into conversion, a malformed steer records `error` instead of the `ok` it should
degrade to. One clause fixes it, and it is a genuine nit rather than an issue
because the design as written does not actually place the rule in the backend.

## Outcome

material findings remain
