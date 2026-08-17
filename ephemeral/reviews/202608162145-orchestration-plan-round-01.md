# Adversarial review: orchestration plan, round 1

## Review target

`ephemeral/projects/tractor/orchestration-plan.md`, reviewed as the proposed path from the current repository to a complete implementation of `docs/spec.md` and the user's requested live `tractor` CLI proof.

## Evidence inspected

- The complete `docs/spec.md`, including the JSON document contract, execution semantics, parallel/worktree rules, observability surfaces, Sections 11.1-11.14, and the harness-backed backend contract.
- The complete proposed orchestration plan.
- Current `harness/contract.go`, `harness/backend.go`, `harness/backend_test.go`, `cmd/agent/main.go`, `go.mod`, and `lefthook.yml`.
- The relevant schema/parser and CLI patterns in `~/src/attractor`, including its repository instructions, `graph/graph.go`, `graph/schema.go`, `graph/runtime.go`, contracts package, and command entrypoints.
- The user's thread requirements: a high-level no-code plan in 5-15 reviewable slices, Cobra, actual `tylergannon/go-gen-jsonschema`, implementation/review loops using Codex and Fable, no cargo-culting or over-engineering, and real file/inline, loop, and fan-out/fan-in CLI proof.

## Findings

### 1. Critical — the plan freezes a backend that contradicts the newly authoritative spec

The plan declares the harness layer finished and says that nothing in `harness` changes (`orchestration-plan.md:3-17`), then routes codergen, resume, and fan-out work through that backend (`orchestration-plan.md:77-91`). That is false against the current spec and code. A reusable thread arriving in a different worktree must treat the old binding as stale and open a replacement session (`docs/spec.md:2449-2466`, `docs/spec.md:2521-2539`), which is what replaying a parallel step requires. The current backend instead returns `"logical thread cannot change workdir"` (`harness/backend.go:149-159`), and its test explicitly expects that terminal failure (`harness/backend_test.go:80-83`). The current constructor also starts the run-log sequence at zero rather than recovering above existing segments, despite the reconstruction requirement (`docs/spec.md:2382-2387`, `docs/spec.md:2536-2539`), and `current.jsonl` retargets to the one surviving branch as soon as concurrency drops to one (`harness/backend.go:306-322`) instead of remaining pinned to the index until the live count reaches zero (`docs/spec.md:2682-2693`). These are not theoretical lower-layer nits: a loop that re-executes fan-out in fresh worktrees will fail at session binding, and reconstructed runs can collide with existing segment names. Add an early reconciliation slice that updates the backend and its conformance proof to the copied spec; the orchestration layer cannot safely build on an asserted-finished contract that it does not have.

### 2. Issue — the proposed handwritten parser duplicates the requested generated validation path

The plan proposes a token-level decoder because it claims generated code cannot enforce the structural contract, while describing only “`go-gen-jsonschema`-style” tooling and incorrectly citing `harness/codex/schema` as the pattern (`orchestration-plan.md:24-33`). That directory uses Atombender in the opposite direction (schema-to-Go protocol types); it is not the user's requested `tylergannon/go-gen-jsonschema` struct-to-schema and validation pattern. The sibling's relevant pattern is its graph model: the actual generator produces deterministic `Schema()` and `ValidateJSON()` behavior from structs and a discriminated interface, while parsing adds only the narrow duplicate-member prepass before generated validation and ordinary decoding (`~/src/attractor/graph/graph.go:9-12`, `~/src/attractor/graph/schema.go:56-71`, `~/src/attractor/graph/runtime.go:67-79`). A second hand-maintained structural decoder creates two independent admissions models and makes the parser/schema “agreement tests” compensate for avoidable drift. Commit explicitly to the actual generator for schema and structural validation, retaining custom parsing only for the one property the generated validator does not cover (duplicate member names) and for defaults resolution after decode.

### 3. Issue — “independently reviewable” slices are not an implementation-validation loop

The plan says each slice is reviewable, but its validation strategy contains only tests, scripted backends, fixture parity, and hooks (`orchestration-plan.md:52-55`, `orchestration-plan.md:99-107`). The user explicitly required Codex-led implementation with Fable adversarial review and an open-ended implement-to-validate loop that checks completeness, actual behavior, and over-engineering, routing material findings back to implementation. Without named subjective review gates, the plan permits all thirteen slices to land green while no reviewer examines whether abstractions are necessary or whether the implementation actually follows the spec. Add a lightweight Fable review after each coherent implementation segment (several adjacent slices may be grouped), with material findings repaired and re-reviewed; retain a final unconditional implementation/proof review after the live runs. This is process-level and should stay high-level rather than expanding each slice into a requirements document.

### 4. Issue — the final audit explicitly stops before the spec's integration requirements

The final sweep is limited to Sections 11.1-11.11 (`orchestration-plan.md:92-93`). Although the validation prose mentions the 11.12 smoke test, it never audits 11.13-11.14, and the final live proof covers one loop plus one fan-out/fan-in (`orchestration-plan.md:109-126`) rather than Section 11.14's composed integration proof across both harnesses, session reuse/compaction, steering, interruption, fresh-process checkpoint reconstruction, and branch worktree workdirs (`docs/spec.md:2392-2396`). Existing adapter evidence may be reused for 11.13, but the copied spec is the authority and the completion audit must account for all four subsections. Change the final sweep to 11.1-11.14, explicitly re-run or validate the existing adapter certifications against the current spec, and add the missing composed backend/engine integration proof rather than treating the narrower three CLI demonstrations as proof of the entire specification.

## Outcome

**material findings remain**
