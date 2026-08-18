# Adversarial review — spec migration and supervisors design, round 05

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (44 lines, md5
`00e7b833214758cae284e361e231fc2f`), together with the companion artifact it
references at line 32, `ephemeral/projects/tractor/upstream-spec-audit.md`
(59 lines, md5 `449ab707b045ac81d4f7375f8480674a`).

Authorities, unchanged from rounds 01–04:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`
  (extracted via `git show origin/main:docs/spec.md` from
  `/Users/tyler/src/attractor`, 2532 lines), the document the design pins as
  normative and will copy byte-for-byte into this repository;
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `lefthook.yml`;
- `ephemeral/worklog/202608171707-spec-supervisors.md`, including the standing
  correction that designs stay skeletal and the decisions pinning authority,
  refusing compatibility layers, recording upstream defects without repairing
  the authority, and the new decision extending the backend seam with a
  binding-open callback;
- the current implementation.

The launch prompt supplied only the target, the read-only boundary, and the
artifact path. It did not limit defects, files, or subject matter, predict
findings, declare safe areas, or request a verdict, so nothing was refused.
Scope covers the whole current design, not only the round-04 deltas.

## Round-04 findings status

All three round-04 findings are addressed:

1. The Critical from the team's own upstream audit now has decisions: line 17
   extends the backend with "a binding-open callback that lets the engine
   checkpoint before dispatch", and line 18 records the briefing as idempotent
   at-least-once input across crashes. Both are mirrored in the worklog.
2. The missing independent-validation phase is now sequence step 6, naming a
   fresh validation agent, both CLIs, and both harnesses — matching
   `skills/orchestrate-attractor-loops/SKILL.md:35-40`.
3. Verdict degradation is now assigned: line 19 keeps schema conversion in the
   backend and puts the malformed-`steer`-to-`ok` degradation in the engine,
   matching §3.10 and the `SupervisorVerdict` event at spec:2011.

No earlier finding regressed. This round's findings are new and arise from the
round-04 fixes themselves.

## Evidence inspected

- Spec §3.10 (patrol tick and nudge at 995–1010; briefing contents and the
  once-per-session rule; sessions-and-resume at 1085–1098; multi-level verdict
  digests), §12.1 `CodergenBackend` interface block (1272–1360), §12.4 run-log
  segments, §5.3 checkpoint `sessions`, §10 `SupervisorVerdict` (2011), and the
  acceptance checklist items at 2218 and 2220.
- `harness/backend.go`: struct and lock layout `:27-44`; `prepareBinding`
  `:157-195` (per-key binding lock, `CreateSession`, `b.threads` publish);
  `Bindings` `:246-252`; `startTurnLog` `:283-320`; `finishTurnLog` `:323-…`;
  `swapCurrent` `:361-…`.
- `engine/store.go` (atomic checkpoint save, timeline and steering appends,
  stage allocation and seq recovery), `engine/runner.go`, `engine/control.go`,
  `engine/parallel.go`, `graph/parse.go`, `graph/graph.go`, `lint/rules.go`,
  `cmd/agent/main.go`.
- `README.md:1-10` for where Tractor currently documents its JSON/YAML surface.
- Prior rounds 01–04 under `ephemeral/reviews/`.

## Findings

### 1. issue — the chosen briefing evidence produces at-most-once, the opposite of the stated policy

Line 18 commits to "idempotent, at-least-once input across crashes: durable
run-log evidence decides whether resume resends it, accepting the irreducible
duplicate-delivery window". The accepted risk is therefore *duplicate*
briefings. But the run-log evidence that exists in this codebase is written
*before* the turn is dispatched: `harness/backend.go:283-320` opens
`events/{seq}-{node}.jsonl` and appends the `events/index.jsonl` entry inside
`startTurnLog`, under `b.mu`, before the adapter is ever asked to run the turn.
A crash in the delivery window therefore leaves exactly the same durable
run-log evidence as a successful delivery.

If "run-log evidence" means the segment or its index entry — the only run-log
facts the engine can consult without parsing segment contents — resume concludes
the briefing was sent and does not resend it. That is at-most-once, and it
reproduces precisely the hole the audit rated Critical
(`upstream-spec-audit.md:9`): a supervisor with a reclaimed binding that never
received its brief, running blind for the rest of the run, with §3.10's own
fallback (spec 1094–1097) unavailable because a binding does exist.

Only a post-delivery record — a completed-turn or response event *inside* the
segment — distinguishes the two states, and even then the safe direction under
an at-least-once policy is to resend whenever the evidence is inconclusive. The
design does not say which record counts or which way ambiguity resolves, and its
one-line statement currently points at the wrong artifact.

Impact: the decision reads as resolved while the mechanism it names cannot
deliver the semantics it promises; an implementer following it literally ships
the Critical the audit was written to prevent.

### 2. issue — two new deviations from the pinned authority have no recorded home, re-creating the contradiction this migration exists to remove

Line 8 states the purpose plainly: replace "Tractor's contradictory local
`docs/spec.md`" with the pinned upstream document, with YAML called out as the
one "explicit product decision outside that normative copy" — and sequence step
1 gives that decision a home ("record YAML as a Tractor extension in the
README"). The worklog decision is the same: replace byte-for-byte, do not edit
extensions into the authority.

Lines 17 and 18 then introduce two further, deliberate divergences from that
same authority:

- a binding-open callback on `CodergenBackend`, whose interface block at spec
  1272–1360 lists exactly `run`, `run_supervisor`, `steer`, `interrupt_all`,
  `bindings`;
- at-least-once briefing delivery, against a document that says the briefing is
  prepended once per session (§3.10) and carries it into the normative
  acceptance checklist at spec:2218 ("with the briefing prepended exactly once
  per session").

After step 1 lands, this repository will ship a normative `docs/spec.md` whose
checklist asserts behaviour the implementation deliberately does not have, with
nothing in the tree recording why. Neither the decisions nor the sequence
assigns these deviations a location — README, a deviations note, or the audit
artifact — and step 1 mentions only YAML. Line 32 covers upstream *defects*
reported outward, not Tractor-side departures reported inward.

Impact: the migration removes one spec-versus-implementation contradiction and
silently introduces two of the same class, in the highest-risk area of the
change, defeating a stated goal of the work.

### 3. nitpick — the new resume behaviour carries no observable proof claim

`skills/orchestrate-attractor-loops/SKILL.md:15` requires observable proof
claims. Claim 39 covers binding, backlog, batch numbering, and save
serialization on resume, all of which predate line 18. Nothing claims the
briefing behaviour the design just decided — that a crash in the delivery window
results in a supervisor that is briefed rather than silently unbriefed, and that
a duplicate brief is harmless. Since finding 1 shows the two outcomes are
distinguishable only by which record is consulted, this is the one behaviour
most worth pinning to an observable claim, and it is the cheapest fix in the
artifact.

## Outcome

material findings remain
