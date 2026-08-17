# Adversarial Review — Cobra CLI slice (round 01)

Date: 2026-08-17 01:15 (local)
Target: uncommitted `cmd/tractor/` (main.go, root.go, root_test.go) and the
`go.mod` change (cobra promoted from indirect to direct) on branch
`codex/orchestration`.

## Authority and scope

- `docs/spec.md` Sections 2, 3.1, 5.3, 7, 8, 11.1–11.12 (read in full for the
  named sections; adjacent sections consulted as needed: 4.2, 5.6, 12.1).
- `ephemeral/projects/tractor/orchestration-plan.md`, slice 13: Cobra `tractor`
  binary with `run`, `validate`, `print-schema`; file-or-`--json` input with no
  input guessing; flags for logs root, workspace, resume; harness-backend
  wiring; signal-based stop.
- Supporting packages read: `engine/runner.go`, `engine/codergen.go`,
  `lint/lint.go`, `lint/analysis.go`, `lint/rules.go`, `harness/backend.go`.

**Narrowing note (required by the review skill):** the launch prompt instructed
me not to treat the unregistered `parallel` handler as a defect. I did not
accept that as a scope restriction; I evaluated it independently. Conclusion:
the plan's slice ordering (slice 13 "grows as handlers land", slice 10 pending)
documents this as intentional staging, and lint's `type_known` treats built-in
types as always known, so a parallel pipeline validates but fails at dispatch
with a terminal "unknown handler type: parallel" — the documented interim
behavior. I report only its one visible artifact as a nitpick (finding 3) and
otherwise reviewed without narrowing.

## Evidence inspected

- `go build ./...`, `go vet ./cmd/tractor` — clean.
- `go test ./... -count=1` — all packages pass, including
  `cmd/tractor` (validate source-exclusivity table, lint failure, schema
  byte-equality, fresh run + resume against real backend wiring, failed-run
  error propagation, missing-logs/missing-checkpoint errors).
- Ran the built binary:
  - `validate --json` on an invalid graph → all error diagnostics rendered,
    exit 1.
  - `run` on a tool pipeline → `COMPLETED`, workspace side effect present,
    `checkpoint.json`/`stages/{seq}-{node}`/`stages/latest` correct (5.3, 5.6).
  - `run --resume` on the completed run → `COMPLETED` again (final checkpoint
    `current_node=done`, `next_node=""` honored).
  - SIGINT during a `sleep 30` tool node → prompt stop ("tool command stopped
    by operator"), exit 1, failure checkpoint naming the node as
    `current_node`/`next_node` (3.1, 5.3); subsequent `run --resume` completed.
  - `run` with missing workdir → clear error; `print-schema` output is valid
    JSON and byte-identical to `graph.Graph{}.Schema()`.
- go.mod: the only change moves `spf13/cobra` from the indirect block to direct
  requires at the already-resolved version. Correct and minimal.

Scale check against the plan: the slice is appropriately small. No config
files, no env plumbing, no output formats, no speculative flags; the flag set
is exactly the plan's list. Signal handling uses `signal.NotifyContext` plus a
small stop goroutine with a `done` guard — proportionate, not
belt-and-suspenders. Code is idiomatic Cobra (RunE, SilenceErrors/Usage,
errors printed once in main).

## Findings

### 1. issue — `validate` and `run` silently discard warning/info diagnostics

`cmd/tractor/root.go:42` and `root.go:123` both call
`cliValidator().ValidateOrError(...)` and throw away the returned
`[]lint.Diagnostic`. When there are no error-severity diagnostics, warnings
never reach the user in any form.

Reproduction: `tractor validate --json '{"nodes":[{"id":"start","type":"start","edges":[{"to":"work"}]},{"id":"work","type":"codergen","edges":[{"to":"done"}]},{"id":"done","type":"exit"}]}'`
prints only `valid --json`, although lint produces the `prompt_on_llm_nodes`
WARNING (verified in `lint/rules.go`); the same holds for the
budget-placement warnings `fan_in_max_visits` and `branch_root_max_visits`.

Spec impact: Section 3.1 phase 2 — "Run lint rules … **Warn on suspicious
patterns**"; Section 7.1 defines WARNING as "pipeline will execute but
behavior may be unexpected"; Section 11.2 requires the warnings to fire. The
lint layer implements all of this, but the CLI — the only user surface this
slice delivers — makes warnings unobservable, so a `validate` subcommand whose
job is surfacing diagnostics reports a warning-bearing pipeline
indistinguishably from a clean one. Fix is small: print non-error diagnostics
to stderr before the `valid` line (and before `run` executes).

### 2. issue — `resolveHarness` re-implements engine provider detection and has already drifted

`cmd/tractor/root.go:215-230` copies the model-prefix switch from the
unexported `engine.detectProvider` (`engine/codergen.go:146-158`) but omits
the `gemini` branch: the engine resolves `gemini-*` models to provider
`"gemini"`, the CLI's copy resolves them to `""`.

`lint.HarnessResolver`'s contract (`lint/lint.go:34-36`) is explicit: "It must
use the same routing as execution." Today the divergence is latent — neither
provider has a route in `harness.DefaultProviderRoutes()`, so a shared-thread
`thread_harness_consistent` check fails either way, just with a different
message (`provider ""` vs `provider "gemini"`) — but two hand-maintained
copies of the routing table's front half are a guaranteed drift point: the
moment detection or routes change in the engine/harness layer, lint-time and
run-time answers diverge silently, which is precisely what the contract
exists to prevent. Fix: export the detection (e.g. from `engine` or
`harness`) and have both the codergen handler and the CLI resolver call it.

### 3. nitpick — `parallel.fan_in` registration is unreachable wiring

`root.go:154` registers the fan-in handler while `parallel` is (intentionally)
unregistered. Lint guarantees a `parallel.fan_in` node exists only as the
convergence of a parallel node's branches (`fan_in_single_parallel`,
`fan_in_entry`, Section 7.2), and branches are only walked by the parallel
handler — so this handler can never execute until slice 10 lands. Harmless
and coherent with the staging; noted so the pairing isn't mistaken for a
working feature.

### 4. nitpick — manual argument-count check duplicates a Cobra facility

`loadPipeline` (`root.go:89-91`) hand-checks `len(args) > 1`;
`cobra.MaximumNArgs(1)` on the `validate`/`run` commands expresses the same
constraint declaratively and yields Cobra's standard usage error. The mutual
exclusivity and at-least-one checks legitimately stay custom (they involve the
`--json` flag), so this is style only.

### 5. nitpick — workdir is inspected before the pipeline is validated

`runPipeline` resolves and stats the workdir (`root.go:114-121`) before
running lint (`root.go:123`), so a run with both a bad workdir and an invalid
pipeline reports only the workdir error. Spec 3.1 orders Validate before
Initialize; strictly the workdir stat is CLI argument checking, not
Initialize, so this is a reporting-order preference, not a violation.

## Verdict

The slice does what the plan's slice 13 asked, is proportionate (no
over-engineering, no unrequested features found), and demonstrably works
end-to-end including interrupt and resume. Findings 1 and 2 are real but
small-fix issues; the rest are nitpicks.

**Outcome: material findings remain** (findings 1 and 2).

---

# Round 02 — re-review after repairs

Date: 2026-08-17 (local), same target: `cmd/tractor/` and `go.mod`. The real
parallel handler (`engine/parallel.go`) landed concurrently; engine changes
were considered only where they bear on the CLI slice.

## Repair verification

**Round-01 finding 1 (warnings discarded) — fixed and verified.**
`validateAndReport` (`root.go:238-252`) runs lint once, prints every
non-error diagnostic to stderr as `<severity> <rule>: <message>`, and returns
the `ValidationError` when error-severity diagnostics exist. Both `validate`
(`root.go:47`) and `run` (`root.go:128`) route through it. Verified live:
`validate --json` on a prompt-less codergen node prints
`warning prompt_on_llm_nodes: ...` to stderr while stdout stays exactly
`valid --json` (exit 0); `run` on a pipeline with `max_visits` on a branch
root prints `warning branch_root_max_visits: ...` before execution. Focused
tests cover both paths (`TestValidateSurfacesWarningsAndPreservesSuccessOutput`,
`TestRunSurfacesWarningsBeforeExecution`).

**Round-01 finding 2 (duplicated, drifted provider detection) — fixed and
verified.** `engine.DetectProvider` is exported (`engine/codergen.go:147`) and
is the single implementation: the codergen handler calls it (`:133`) and the
CLI's `resolveHarness` calls it (`root.go:229`). The gemini drift is gone —
`resolveHarness("", "gemini-2.5-pro")` now fails with `provider "gemini"`,
matching execution. The CLI configures concrete system defaults
(`gpt-5.6-sol` / `high`, `root.go:21-24`) into `CodergenConfig`
(`root.go:157-161`), provider left unset so execution auto-detects
(spec 8.2), and `resolveHarness` applies the same empty-model default before
detection (`root.go:226-228`), keeping lint routing consistent with
execution across the provider-set/model-empty and both-empty cases.
`TestResolveHarnessUsesExecutionProviderDetectionAndSystemModel` pins all of
this, including the default constants.

**Round-01 nitpick 3 (unreachable fan-in registration) — resolved by the
landed parallel handler.** The runner registers `parallel` internally
(`engine/runner.go:245`), so the CLI's `parallel.fan_in` registration is now
live wiring. Proven end-to-end with the real binary in a scratch git repo: a
fan-out/fan-in pipeline ran both tool branches in isolated worktrees
(`stages/000003-l`, `000004-r`), wrote `branches.json` with BranchResult
evidence into the parallel's stage dir, rendered that evidence into the
fan-in's `prompt.md`, and dispatched a real codex turn (interrupted by me at
25s; the failure checkpoint landed and worktrees were left in place, per
spec 3.1 failure semantics). In a non-git workdir the run fails cleanly with
"parallel execution requires a git worktree" after printing warnings.

## Evidence

- `go build ./...`, `go vet ./cmd/tractor` — clean; `go test ./... -count=1`
  — all packages pass.
- Live binary: warning surfacing on validate and run (above); parallel
  fan-out/fan-in run in a git workdir reaching a real backend turn; non-git
  workdir failure mode; earlier round-01 checks (linear run, resume, SIGINT)
  unchanged by inspection of the diff-relevant paths.
- `go.mod` unchanged since round 01 (cobra direct; correct).

## Findings

### 1. nitpick — `TestRunSurfacesWarningsBeforeExecution` is hermetic by accident

The test (`root_test.go:134-151`) runs a parallel/fan-in pipeline with a
`t.TempDir()` workdir and asserts only `pipeline failed:`. It passes quickly
because the tempdir is not a git repository, so the parallel handler fails
before the fan-in's real codex turn ("parallel execution requires a git
worktree" — verified live). Nothing in the test documents that dependence;
if the workdir ever became a git repo (or the engine relaxed the
requirement), the test would attempt a real LLM call. Asserting the specific
failure message, or using a pipeline whose failure doesn't depend on the
git requirement, would pin the intent.

### 2. nitpick — carried from round 01, unchanged

Manual arg-count check in `loadPipeline` (`root.go:94`) vs
`cobra.MaximumNArgs(1)`; workdir stat before lint (`root.go:119` vs `:128`)
so a doubly-bad invocation reports only the workdir error. Both remain style
preferences, not defects.

## Verdict

Both material round-01 findings are properly repaired with focused tests, the
staging nit is resolved by the landed parallel handler and demonstrated live
end-to-end, and no new material problems were introduced by the repairs. The
system-default constants are spec-sanctioned (8.2) and single-sourced.

**Outcome: only nitpicks remain.**
