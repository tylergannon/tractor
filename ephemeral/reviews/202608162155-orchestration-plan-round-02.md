# Adversarial review: orchestration plan, round 2

## Review target

Revised `ephemeral/projects/tractor/orchestration-plan.md`, checked against `docs/spec.md`, the current repository, the user's authoritative requirements, and every round-1 finding.

## Evidence inspected

- The complete revised plan.
- The complete `docs/spec.md`, with particular re-checks of strict JSON parsing, generated-schema expectations, parallel worktree/session behavior, run-log reconstruction, Sections 11.1-11.14, and the final integration proof.
- Current `harness/backend.go`, `harness/backend_test.go`, `harness/contract.go`, `cmd/agent/main.go`, `go.mod`, and `lefthook.yml`.
- The relevant `tylergannon/go-gen-jsonschema` model/schema/decoder pattern and command structure in `~/src/attractor`.
- Round 1 at `ephemeral/reviews/202608162145-orchestration-plan-round-01.md` and the user's requirements for Cobra, 5-15 high-level reviewable slices, no code, Fable review loops, minimal design, and real file/inline/loop/fan-out/fan-in proof.

## Round-1 resolution

All four material findings are resolved:

- Slice 1 now reconciles the backend's stale-workdir binding, segment recovery, and concurrent pointer behavior before parallel orchestration depends on it (`orchestration-plan.md:3-18`, `orchestration-plan.md:54-57`).
- The graph structs and actual `tylergannon/go-gen-jsonschema` generator are now the single structural admission model, with only duplicate-member detection outside generated validation (`orchestration-plan.md:24-30`, `orchestration-plan.md:58-64`).
- Coherent implementation segments now enter open-ended Fable review, with material findings routed back to implementation and re-reviewed (`orchestration-plan.md:109-114`).
- The completion sweep now covers Sections 11.1-11.14, and the live proof adds both harnesses, reuse/compaction, steering, interruption, fresh-process resume, and branch-worktree bindings (`orchestration-plan.md:93-94`, `orchestration-plan.md:130-134`).

## Findings

### 1. Nitpick — the closing sentence still counts the old number of live runs

The live-proof list now has four numbered runs (`orchestration-plan.md:121-134`), but the closing criterion still says “all three runs” (`orchestration-plan.md:136-138`). Change “three” to “four” so the acceptance sentence cannot be read as excluding the composed 11.13-11.14 proof.

No material flaw remains. The plan stays high-level, contains no implementation code, uses fourteen independently reviewable slices, commits to Cobra and the requested generator, avoids copying the sibling design, and ends in actual CLI proof rather than test-only certification.

## Outcome

**only nitpicks remain**
