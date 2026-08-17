# Adversarial Review — Fan-In Slice, Round 01

Date: 2026-08-16 (local)
Branch: `codex/orchestration` @ 30d647b + untracked working-tree files

## Review Target

Uncommitted fan-in slice in the working tree:

- `engine/fan_in.go` — `BranchResult` strict decode, `FanInHandler`
- `engine/fan_in_test.go`
- `lint/topology.go` — `ParallelForFanIn`
- `lint/topology_test.go`

The "narrow codergen refactor" (`CodergenHandler.executeTurn` extraction) is already
committed in 30d647b (`engine/codergen.go`); the working tree contains only the four
untracked files above.

Authoritative sources: `docs/spec.md` §4.5 (Codergen Handler), §4.8 (Parallel
Handler), §4.9 (Fan-In Handler), §5.6 (Run Directory Structure), §7.2 (Lint Rules),
§11.6 (Definition of Done — Node Handlers).

### Note on caller narrowing

The launch prompt narrowed the review ("Review only the uncommitted fan-in slice",
"Ignore resume edits if they appear", a fixed focus list). Per this skill, such
narrowing does not limit the defects or subject matter considered; I derived scope
from the spec and current artifacts. In practice the slice named is the entire
uncommitted change, no resume edits exist in the tree, and no material defects
outside the named focus areas surfaced. The narrowing therefore did not change the
result, but it was not honored as a constraint.

## Evidence Inspected

- Full read of the four new files, plus committed context: `engine/codergen.go`,
  `engine/runner.go` (registry, handler call path), `lint/analysis.go`,
  `lint/rules.go`, `harness/contract.go` (`Outcome`, `Error`, `Valid()`),
  `graph/graph.go` (`FanInNode`, `LLMNodeFields.PromptValue`).
- Spec sections 4.5, 4.8, 4.9, 5.6, 7.2, 11.6 in full.
- Proof runs:
  - `go test ./...` — all packages pass; `go vet ./engine ./lint` clean.
  - `go test ./engine -run 'TestFanIn' -count=1 -v` — all fan-in tests pass.
  - A temporary proof test (removed after the run) demonstrating Finding 1:
    marshaling a `BranchResult` with nil `Path`/`StageDirs`/`Segments` produces
    `"path":null,"stage_dirs":null,"segments":null`, which the type's own
    `UnmarshalJSON` rejects with `branch result is missing a required field`.

## Spec-Conformance Checks That Passed

- **Owning-parallel lookup** (§4.9, §7.2 `fan_in_single_parallel`):
  `lint.ParallelForFanIn` reuses the shared `parallelBlocks()` convergence
  analysis rather than re-deriving graph topology; exactly-one enforcement is
  correct, and the handler converts lookup failure to a terminal Error before
  touching the backend (proven by `TestFanInHandlerRejectsAmbiguousOwnerBeforeBackend`).
- **Evidence location** (§4.9, §5.6): `parent(scope.StageDir)/latest/{owner.ID}/branches.json`
  matches `stages_latest` exactly, including the sibling-under-`stages/` resolution.
- **Strict evidence** (§4.8 BranchResult shape): closed decode
  (`DisallowUnknownFields` + trailing-value rejection), all seven fields required,
  exactly-one-of outcome/error enforced including explicit `null` rejection,
  `harness.Error.Valid()` applied, duplicate `branch_id` and empty array rejected,
  and evidence branch IDs checked against the owning parallel's edge targets
  (subset check only — correct, since §4.8 says a fan-out legitimately SHRINKS when
  a branch root is at `max_visits`, so requiring completeness would be wrong).
  All failures occur before `prompt.md` is written and before the backend runs
  (asserted by tests).
- **Prompt semantics** (§4.9): authored prompt, else the exact default
  `"Evaluate the results of the parallel branches."`; no label fallback (that is
  §4.5's rule, and §4.9 replaces the prompt-build step); render is per branch
  `ID / notes / worktree path`, exactly the three fields §4.9 names.
- **Exact turn reuse** (§4.9 "continue exactly as CodergenHandler", §11.6):
  delegation to the committed `CodergenHandler.executeTurn` with the fan-in's own
  `LLMNodeFields`; `TestFanInHandlerLoadsEvidenceAndDelegatesExactTurn` pins the
  full turn (model/provider/reasoning/fidelity/thread/timeout/workdir), the exact
  choice schema JSON including route descriptions, `prompt.md`, and `response.md`.
  `scope.Workdir` stays the main workspace, per §4.9.
- **Simplicity/duplication**: no duplicated codergen logic — schema, resolution,
  simulation, and response writing all live once in `codergen.go`. `topology.go`
  is 24 lines over the existing analysis.

## Findings

### 1. [issue] `BranchResult` cannot round-trip its own zero-value slices — producer/consumer trap

`engine/fan_in.go:23-26` declares `Path`, `StageDirs`, `Segments` as `[]string`
with no `MarshalJSON`; `engine/fan_in.go:45` requires all of them present and
non-null on decode. Go marshals a nil slice as `null`, and JSON `null` into a
`*[]string` wire field leaves the pointer nil, so the type's own marshal output
is rejected by its own unmarshal.

Reproduction (run during this review; test passed compile, failed at the decode):

```go
raw, _ := json.Marshal([]BranchResult{{BranchID: "branch-a",
    Outcome: &harness.Outcome{Notes: "ok"}, Workdir: "/wt/a"}})
// wire: [{"branch_id":"branch-a","outcome":{"notes":"ok"},"notes":"",
//         "path":null,"workdir":"/wt/a","stage_dirs":null,"segments":null}]
json.Unmarshal(raw, &decoded)
// => "branch result is missing a required field"
```

Impact: the §4.8 parallel handler (the producer, not yet written) will naturally
build `BranchResult` values whose slice fields start nil and write them with
`json.Marshal` — producing `branches.json` that this fan-in rejects at runtime as
terminal. The strictness contract currently lives only on the decode side of a
type both sides will share. Secondary defect: the error message reports an
explicitly-`null` field as "missing a required field", which will misdirect
whoever debugs it.

Fix direction (for the implementer, not applied here): give `BranchResult` a
`MarshalJSON` that normalizes nil slices to `[]` (or constructor discipline the
producer can't skip), and distinguish `null` from absent in the decode error.

### 2. [nitpick] Two definitions of "owning parallel" now exist

`lint/topology.go:15` counts an owner only when `block.converged &&
block.candidate == fanInID`, while the lint rule it cites
(`lint/rules.go:211` `fanInSingleParallel`) counts by `block.candidate` alone.
On a lint-clean graph they coincide (a non-converged block trips
`parallel_fan_in` or `edge_target_exists` first), so this is not reachable as a
bug, but the runtime "found %d" message implies the same count as the lint rule
and can differ from it on unlinted graphs (e.g. candidate set, not converged →
lint says 1, runtime says 0). Aligning both on one predicate (or having the rule
and the lookup share a helper) would remove the drift risk.

### 3. [nitpick] Whitespace-only fan-in prompt skips the default, and the empty-check duplicates `PromptValue`

`engine/fan_in.go:135-141`: `Prompt: {Present: true, Value: "  "}` is not `""`,
so the model receives whitespace plus the branch render instead of the §4.9
default ("IF prompt is empty"). Codergen's committed `PromptValue`
(`graph/graph.go:155`) has the same literal-`""` semantics, so this is at least
consistent — but the four lines in `Execute` re-implement exactly
`join.PromptValue("")` and could collapse to it.

### 4. [nitpick] `$goal` expansion scope in the fan-in prompt is one of two defensible spec readings

`engine/fan_in.go:142-143` expands `$goal` in the authored prompt only, then
appends the branch render. A strict reading of §4.9's "continue exactly as
CodergenHandler with this prompt" would run §4.5's `expand_variables` over the
combined text, branch notes included. The implemented choice is the safer one
(branch notes are evidence and should not be macro-expanded) and §4.9's own
pseudocode enumerates the continuation as "choice schema, backend turn, response
file" — but a one-line comment recording the deliberate divergence would prevent
a future "fix" in the wrong direction.

## Not Findings (checked and dismissed)

- Fan-in handler is not registered in `NewRegistry` — consistent with the slice:
  codergen is equally unregistered; the default registry carries only non-LLM
  handlers, and LLM handlers need `CodergenConfig` from the composition root.
- Subset-only branch-ID validation (no completeness check) — correct per §4.8's
  shrinking fan-out semantics; see conformance list above.
- Reading via `stages/latest/{node_id}` after a *failed* later parallel attempt —
  not reachable: `latest` repoints only on success (§5.6) and the engine reaches
  the fan-in only after a successful parallel execution.
- `Outcome` round-trip through the strict wire decode — `notes` has no
  `omitempty`, so a producer-marshaled outcome always carries it; `next` is
  correctly optional.

## Outcome

**material findings remain** — one issue-level finding (the `BranchResult`
marshal/unmarshal asymmetry, Finding 1); the remainder are nitpicks.

---

## Round 01a — Adjudication of Finding 1 Fix (2026-08-16, same day)

Re-review scope: Finding 1 only, per the caller's request.

Change inspected: `engine/fan_in.go:29-43` adds `MarshalJSON` on a **value**
receiver — so it applies both to `BranchResult` values and pointers, including
elements of `[]BranchResult` — normalizing nil `Path`/`StageDirs`/`Segments` to
`[]string{}` via the method-stripping `type branchResult BranchResult` alias
before delegating to `json.Marshal`. `UnmarshalJSON` is unchanged (correctly:
externally-authored `null` evidence should still be rejected).

Test inspected: `TestBranchResultRoundTripNormalizesNilSlices`
(`engine/fan_in_test.go:70`) marshals a `BranchResult` with all three slices
nil, asserts the wire form contains `"path":[]`, `"stage_dirs":[]`,
`"segments":[]`, then round-trips through the strict decoder and asserts
non-nil empty slices — exactly the reproduction from Finding 1, now inverted
into a regression test.

Proof: `go test ./engine -run 'TestBranchResult|TestFanIn' -count=1` — all pass.

Verdict on Finding 1: **fixed**. The producer/consumer trap is closed at the
type level; a future parallel handler using `json.Marshal` on `BranchResult`
values (or slices of them) can no longer emit evidence the fan-in rejects.

The secondary point — the decode error reporting an explicit `null` as
"missing a required field" — was not addressed. It is no longer reachable from
engine-produced evidence and is downgraded to a nitpick alongside Findings 2-4.

## Revised Outcome (Round 01a)

**only nitpicks remain** — Finding 1 is fixed; Findings 2-4 and the
null-vs-missing error-message wording stand as nitpicks.
