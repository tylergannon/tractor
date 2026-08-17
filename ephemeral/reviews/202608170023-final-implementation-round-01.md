# Adversarial Review — Final Implementation and Proof (Round 01)

**Target:** the complete Tractor implementation on branch `codex/orchestration`
at commit `58759b7` ("Add timeline observability and live steering"), working
tree clean of tracked modifications, reviewed against `docs/spec.md` (2812
lines, normative) and repository instructions, including the live proof under
`ephemeral/projects/tractor/orchestration-live`.

**Caller scope note.** The launch prompt lists "authoritative user acceptance
concerns" (over-engineering, belt-and-suspenders, unrequested features,
future-proofing, abstraction, idiomatic Go, functionality). Per this skill,
that list was treated as emphasis, not as a limit: the review covered spec
conformance, correctness, races, and completeness across the whole repository
regardless of that list.

## Evidence inspected

- **Specification:** `docs/spec.md` read in full — lifecycle, walk loop,
  retry/backoff, checkpointing, parallel fan-out/fan-in, worktree inventory,
  control socket/steering, timeline events, run-log segments, HarnessBackend
  contract, all 28 lint rules, Definition of Done checklists.
- **Implementation read in full:** `engine/` (runner, state, store, control,
  codergen, fan_in, tool, parallel, git_workspace), `harness/` (backend,
  contract, validate, result), `harness/claude/adapter.go`,
  `harness/codex/adapter.go`, `lint/` (lint, rules), `graph/` (graph, parse),
  `cmd/tractor/root.go`, `cmd/agent/main.go`; supporting files
  (`lint/analysis.go`, adapters' events/rpc/native, `cmd/harness-conformance`)
  inspected as needed.
- **Execution:** `go build ./...`, `go vet ./...` clean; `go test ./...` all
  packages pass. Built the real binaries and ran them:
  - `tractor print-schema` and `tractor validate` (both file and `--json`,
    including a rejection of an unknown top-level member);
  - `tractor run` on a real start→tool→exit pipeline: COMPLETED, with correct
    `checkpoint.json`, `timeline.jsonl` (PipelineStarted → StageStarted/
    Completed → CheckpointSaved → PipelineCompleted), `stages/`, `events/`,
    and `manifest.json`;
  - `agent` against the **real Claude harness** (claude 2.1.233) and the
    **real Codex harness** (codex-cli 0.147.0): both returned schema-valid
    structured outcomes with native session IDs and wrote run-log segments.
- **Live proof (`orchestration-live/run-logs-58759b7`):** full composed run
  demonstrating real steering (steering.jsonl audit entry + `STEER-58759B7`
  landed in `workspace/steered-live.txt`), real interruption
  (`PipelineFailed: turn was interrupted`) followed by `--resume` to
  `PipelineCompleted` (`interrupt-resumed.txt`, `interrupt_stage` attempts=2),
  real two-branch parallel fan-out in git worktrees (`worktrees.jsonl`
  inventory; worktrees swept after completion), `branches.json` evidence
  consumed by the fan-in, a review loop (`review` visits=2), and a
  manifest-recorded control socket (temp-dir fallback correctly used for the
  >100-char logs path, as the spec's manifest `control_socket` field allows).
- **Prior review artifacts:** all 20 rounds listed; the two latest read in
  full. The round-02 material finding (worktree inventoried only after
  `git worktree add`, so a stop race could orphan an unswept worktree) is
  **fixed**: `engine/git_workspace.go:117-125` now appends the inventory entry
  before invoking `git worktree add`.

Conformance verified in detail and found correct includes: strict JSON parsing
(duplicate members, nulls, unknown fields, single value) at
`graph/parse.go`; checkpoint semantics including `retry_visit` and
pre-execution failures writing nothing; backoff `200ms*2^(n-1)` capped 60s
with jitter; offered-successor visit-budget exclusion; parallel counter
snapshot/rollback with the parallel node's own-counter exception;
`branches.json` written before branch-error propagation; choice-schema shape
(notes-only vs next-enum); fidelity/thread-key rules including the none-key
disjoint namespace; harness-change-before-stale-workdir ordering; steering
cardinality and engine-level parallel rejection (409); run-log segment
allocation with `current.jsonl` pinning; all 28 lint rules including the two
warnings; timeline event vocabulary complete per §10.

## Findings

### 1. Issue — over-engineering: `BranchResult` ships a hand-written strict wire codec for the engine's own file

`engine/fan_in.go:30-114` (plus `decodeStrictJSON`, lines 196-209) implements
~85 lines of custom `MarshalJSON`/`UnmarshalJSON`: a pointer-per-field wire
struct, required-field presence checks, explicit `null` rejection for
`outcome`/`error`, an exactly-one-of invariant, nested strict decoding of
`Outcome` and `Error`, and trailing-value detection — all to read back
`branches.json`, a file the same engine wrote with `writeJSON` moments (or one
resume) earlier in `engine/parallel.go:78`. The spec's pseudocode is plain
`read_json` (`docs/spec.md:1388`); nothing in §4.8/§4.9 asks for a closed,
validated evidence schema on read. The defended-against states are already
impossible upstream: every construction site initializes the slice fields
(`engine/parallel.go:132-139,168-173`), making the `MarshalJSON` normalization
dead; and the exactly-one-of case a reader could actually produce (a branch
that never executed a node) is prevented by the `fan_in_entry` lint rule
(`lint/rules.go:298-312`), which forbids any non-branch edge into the fan-in.
Impact: this is precisely the belt-and-suspenders category the user asked to
have removed — real maintenance surface (three parallel JSON-decoding helpers
now exist across `engine`, `harness`, `graph`) defending a self-authored
artifact, with the residual crash-truncation risk already caught by an
ordinary `json.Unmarshal` error. A plain decode plus the existing
duplicate-`branch_id` and known-branch checks in
`engine/fan_in.go:141-149,163-183` would satisfy the spec.

No other findings. Specifically checked and cleared: the control-socket
temp-dir fallback (justified by the macOS `sun_path` limit and surfaced via
the spec's manifest `control_socket` field, exercised by the live proof); the
lint `Rule`/`Options` extension surface (mandated at `docs/spec.md:1839`);
`cmd/harness-conformance` (required by §11.13/§11.14); the adapters'
`nativeSession`/`sessionFactory` seams (minimal and test-consumed); and the
substring-based native-error categorization in both adapters (the spec leaves
native-error mapping to the adapter, retryable markers are checked before
terminal ones).

## Outcome

material findings remain
