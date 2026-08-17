# Adversarial Review — Orchestration Resume Slice, Round 01

Date: 2026-08-17 (local)
Reviewer: adversarial-review (read-only)
Target: uncommitted tracked diffs in `engine/runner.go`, `engine/runner_test.go`,
`engine/store.go`, `engine/store_test.go` (the resume slice), reviewed against
`docs/spec.md` Sections 3.1–3.7, 5.3, and checklist items 11.3/11.4/11.7.

## Scope note (caller narrowing ignored)

The launch prompt instructed "Ignore fan-in files" and supplied a focus list.
Per this skill, instructions that limit the subject matter I may consider are
refused: I inspected the untracked `engine/fan_in.go`/`fan_in_test.go` and
`lint/topology.go` for interactions with resume and found none — fan-in holds
no checkpoint/seq/resume coupling (it renders `branches.json` into a codergen
prompt), and the lint rule is orthogonal. No material gap is created for the
resume slice by their exclusion, so the review below concentrates on the
tracked diff as the target. The read-only constraint and artifact path were
honored as valid operating constraints.

## Evidence inspected

- Full `git diff` of the four tracked files.
- Surrounding code read in full: `engine/runner.go`, `engine/state.go`,
  `engine/store.go`; helpers in `engine/runner_test.go:825-868`.
- Spec: Sections 3.1–3.7 (`docs/spec.md:400-720`), 5.3 (`docs/spec.md:1506-1598`),
  5.4 thread rules, checklists 11.3/11.4/11.7 (`docs/spec.md:2024-2076`).
- `go test ./engine/...` — ok (0.85s); `go vet ./engine/` — clean.
- **Fresh-process proof** (scratchpad module with `replace` directive; four
  separate OS process invocations of a built binary against one logs root,
  graph `start -> work(max_visits=1) -> done`, `work` returning a terminal
  error until told to succeed):
  - P1 fresh run: FAILED "boom"; checkpoint `current=next="work"`,
    `retry_visit=true`, `node_visits{start:1,work:1}`, `node_attempts{work:1}`,
    `seq=2`, `completed_nodes=["start"]`.
  - P2 `ResumeRunner`, fail again: FAILED; `node_visits.work` **still 1**,
    `node_attempts.work=2`, `retry_visit=true`, `completed_nodes` untouched,
    `seq=3` — a `max_visits=1` node re-executed inside its one consumed visit,
    exactly Section 5.3/11.7 arithmetic.
  - Injected orphan `stages/000042-crashed`, then P3 resume-and-succeed:
    COMPLETED; new stage allocated as `000043-work` (scan beat checkpoint
    `seq=3`, `latest/` ignored), final checkpoint `current="done"`,
    `next=""`, `retry_visit=false`, `node_visits.work=1`, `node_attempts.work=3`.
  - P4 resume after final checkpoint: COMPLETED with **byte-identical
    checkpoint** (timestamp unchanged) — no dispatch, no rewrite.

## Behaviors verified against the spec

- **One shared walk.** `Run()` (engine/runner.go:212-232) computes the entry
  point and delegates to the single `walk()` loop for fresh and resumed runs;
  resume adds no second traversal path.
- **Initial continuation.** Resume from the initial checkpoint (`current_node`
  empty, `next_node` = start) starts the walk without rewriting the initial
  checkpoint (`r.resumeCheckpoint == nil` guard, runner.go:226-230); test
  `TestResumeRunnerContinuesInitialCheckpointWithoutRewritingIt` asserts the
  bytes are untouched at dispatch time and counters land at 1/1.
- **Success continuation, no route re-ask.** Resume continues at the
  checkpointed resolved successor; the completed router node is never
  re-dispatched (`TestResumeRunnerUsesResolvedSuccessorAndRecoversCrashedStageSequence`
  fails the test if "choose" runs again). Matches Section 3.2's "resume
  continues the walk instead of re-deriving — or re-asking — a choice" and
  checklist 11.3.
- **Failure continuation / retry_visit exact arithmetic.** `stateFromCheckpoint`
  restores the flag (state.go:47-57); `beginVisit` consumes it exactly once in
  place of the increment (state.go:76-84), and it is only reachable with the
  failed node first because `validateResumeCheckpoint` requires
  `next_node == current_node` whenever `retry_visit` is set (runner.go:314-316).
  Repeated fail-resume cycles keep `node_visits` at 1 while `node_attempts`
  climbs — proven across real process boundaries (P1→P2→P3 above), satisfying
  Sections 3.7/5.3 and checklist 11.4/11.7. Subsequent failure checkpoints
  re-set `retry_visit=true` with visits unincremented, so arbitrarily many
  operator resumes stay inside the one consumed visit.
- **Pre-execution failures write nothing on a resumed run.** A stop caught at
  loop top of attempt 1 returns `attempted=false` (runner.go:334), so no
  checkpoint is written and the standing failure checkpoint's `retry_visit`
  is not clobbered — the exact double-billing hazard Section 3.2's comment
  warns about.
- **Stage-seq scan.** `openRunStore` recovers
  `seq = max(checkpoint.seq, scan)` with `latest/` and malformed names skipped
  (store.go:33-48, 160-180). Proven by the injected `000042-crashed` orphan
  yielding `000043-work` next, and by the in-repo test's `000009-crashed` →
  `000010-right`. Matches Section 5.3 resume step 2.
- **Final-checkpoint boundary.** `next_node == ""` is accepted only when
  `current_node` is an exit node and `retry_visit` is false
  (runner.go:290-297); `Run()` then returns COMPLETED before `openRunStore`,
  so nothing on disk is touched (P4 proof: identical bytes, exit handler never
  dispatched — also asserted by
  `TestResumeRunnerFinalCheckpointReturnsWithoutDispatchOrRewrite`).
- **Malformed checkpoints.** Unreadable file, undecodable JSON, empty
  continuation, unknown `next_node`/`current_node`, continuation after exit,
  initial-shaped checkpoint with `retry_visit` or a non-start `next_node`, and
  `retry_visit` naming a different node are all rejected at `ResumeRunner`
  construction (runner.go:290-318), before any file is created. Covered by
  `TestResumeRunnerRejectsMalformedCheckpointContinuation`.
- **Session restoration boundary.** The engine snapshots `Bindings()` into
  every checkpoint save and takes an already-reconstructed backend on resume
  (`RunnerConfig.Backend`), leaving session restoration to the backend's
  constructor — consistent with Section 5.3 step 4 ("Session persistence is
  the CodergenBackend's responsibility") and Section 12.1's ownership split.
  `TestResumeRunnerSnapshotsBindingsFromReconstructedBackend` exercises the
  full round trip including a binding added during reconstruction.
- **Self-loops stay legal.** `next == current` with `retry_visit=false` (a
  success checkpoint on a self-edge) passes validation and counts a fresh
  visit on resume — the validator only binds `next==current` when
  `retry_visit` is set, so it cannot misclassify a legitimate loop.
- **Simplicity.** The slice is ~90 implementation lines: one validator, one
  extracted `walk`, a state constructor, and an exported `LoadCheckpoint`
  rename. No new abstractions, no speculative configuration, no resume-only
  execution path. Nothing here qualifies as over-engineering.

## Findings

No critical findings and no issues. Two genuine nitpicks:

1. **Nitpick — double checkpoint read on the resume path.**
   `ResumeRunner` loads `checkpoint.json` internally (engine/runner.go:145),
   but a caller restoring sessions must have already called the now-exported
   `LoadCheckpoint` to construct the backend it passes in `config.Backend`.
   The two reads are of a file at rest, so there is no practical divergence
   today, but the API invites a future caller to reconstruct the backend from
   one snapshot while the runner validates another. Accepting an
   already-loaded `Checkpoint` (or returning the loaded one) would close the
   seam. Not blocking.

2. **Nitpick — terse rejection message for an empty continuation.**
   `"checkpoint has no valid continuation"` (engine/runner.go:294) covers
   three distinct states (empty file-shaped `{}`, non-exit `current_node`
   with empty `next_node`, `retry_visit` on a final checkpoint) with one
   string. Naming the offending field would speed up operator diagnosis of a
   corrupt checkpoint. Cosmetic.

(Observed but out of this slice's diff: `runStore.appendTimeline` remains
unused by the runner — pre-existing in committed code, not introduced here.)

## Outcome

**only nitpicks remain**
