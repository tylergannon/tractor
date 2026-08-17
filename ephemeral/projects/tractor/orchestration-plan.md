# Orchestration implementation plan

Authority: `docs/spec.md` (typed-JSON pipelines, chooser routing). Existing
lower layer: the `harness` package — `CodergenBackend`, `HarnessBackend`,
Claude/Codex adapters, exact-schema result validation, categorized errors,
run-log segments. The adapters are proven; the backend needs a narrow
reconciliation with the newly advanced spec before the orchestration layer
can rely on it. This plan then builds that layer and the `tractor` CLI. It
deliberately builds nothing speculative: no SSE streams, no TCP control
listener, no extra fidelity modes, no AST transforms, no custom frontends.

## Decisions

- **Small orchestration package family, engine-first layering.** The graph model,
  parser, validator, engine, handlers, and run directory live in fresh
  packages that depend on `harness` only through `CodergenBackend` and the
  `Outcome`/`Error`/`ContentPart` types already defined there. Changes to
  `harness` are limited to current-spec discrepancies demonstrated by tests.
- **Cobra CLI** (explicit user requirement; the sibling attractor CLI's
  hand-rolled flag parsing is a lesson, not a template). One `tractor`
  binary with `run`, `validate`, and `print-schema` subcommands. `run` and
  `validate` accept either one file argument or an explicit `--json` string;
  there is no input-guessing layer.
- **Generated schema and structural validation.** The graph structs and their
  discriminated node union are the single admission model.
  `tylergannon/go-gen-jsonschema` deterministically generates the published
  schema, structural validator, and decoder support. Parsing adds only a
  narrow duplicate-member preflight, then generated validation, ordinary
  decoding, and defaults resolution. Generated artifacts are committed and
  protected by a drift check.
- **Chooser routing exactly as specified.** The engine never reads
  `condition` text; choice schemas are built by the codergen handler; the
  parallel handler is the single exception to next-must-be-offered.
- **Simulation mode stays.** A nil backend runs the graph with simulated
  outcomes (spec 4.5), which is what makes engine tests hermetic and cheap.

## Runtime decomposition

Packages, in dependency order: graph model + parser; lint/validate;
run directory + checkpoint; engine (loop, budgets, retries, stop signal);
handlers (start/exit, codergen, tool, parallel, fan-in, registry); CLI.
Handlers depend on the engine's scope types, not on the loop; the loop
depends on the registry interface, not on concrete handlers. State is typed
(`EngineState`, `Checkpoint`) — no context bags. Lesson applied from the
sibling implementation: keep validation a flat registry of small rule
functions producing `Diagnostic` values, one rule per spec ID, so rules are
individually testable and the table in spec 7.2 maps one-to-one to code.

## Slices

Each slice is independently reviewable, lands with its own tests, and
leaves the tree green.

1. **Backend reconciliation.** Bring the existing `HarnessBackend` into the
   copied spec where it drifted: stale-workdir rebinding for replayed fan-out,
   run-log sequence recovery, and concurrent-log pointer behavior. Preserve
   the already-proven adapter boundary and re-run its focused conformance.
2. **Graph model and generated parser.** Node union, edges, defaults
   resolution (six-field whitelist), durations, duplicate-member and
   unknown-field rejection. Fixtures cover every parse error class in
   spec 11.1.
3. **Published JSON Schema generation.** `go:generate` production and
   validation through `tylergannon/go-gen-jsonschema`; agreement and drift
   tests against the slice-2 fixtures.
4. **Validation and lint.** `Diagnostic`, severity model, the full spec 7.2
   rule table (structural rules first, parallel/thread rules included),
   `validate`/`validate_or_raise` equivalents, custom-rule registration.
5. **Run directory and checkpoint.** `logs_root` layout, atomic
   `checkpoint.json` writes, `seq` recovery scan, `timeline.jsonl` event
   sink, stage-dir creation, `stages/latest` repointing.
6. **Engine core loop.** Offered-successor computation, visit budgets,
   dispatch, outcome/next resolution, success and failure checkpoints,
   `retry_visit` semantics, stop signal, `RunResult`.
7. **Retry and backoff.** Categorized-error retry loop, jittered backoff,
   attempt counters, `error.json` per failed attempt.
8. **Start/exit, tool handler, registry.** Exit-code routing, `on_fail`
   rules, `tool.log`, timeout-as-interrupt, unknown-type terminal error.
9. **Codergen and fan-in handlers.** `$goal` expansion, choice-schema
   construction, `prompt.md`/`response.md`, fully resolved `CodergenTurn`
   into the existing backend; fan-in reads `branches.json` and renders
   branch evidence. Simulation mode included.
10. **Parallel handler.** Workspace freeze, engine-owned worktrees,
   bounded-concurrency branch walks sharing counters and `seq`,
   `branches.json`, `worktrees.jsonl`, counter snapshot/rollback,
   atomic-step failure semantics, Finalize cleanup.
11. **Resume.** Load checkpoint, restore counters/sessions/`seq`,
    `retry_visit` continuation, backend reconstruction from bindings.
12. **Steering surface.** Unix-socket `POST /steer`, manifest
    advertisement, engine-side parallel rejection, `steering.jsonl` audit.
13. **CLI.** Cobra `tractor` with `run` (file and `--json` input),
    `validate`, and `print-schema` subcommands; flags for logs root, workspace,
    resume; wiring of the harness backend and signal-based stop.
14. **Definition-of-done sweep and live proof.** Walk spec 11.1–11.14
    checklists against the suite; then the live proof below.

Dependencies: slice 1 can proceed alongside 2→3→4; 5 needs 2; 6 needs 4+5;
7–9 need 6; 10 needs 8+9 and the reconciled backend; 11–12 need 6 (12 also
9); 13 needs 4+6 and grows as handlers land; 14 is last.

## Validation strategy

- Hermetic unit tests per slice; engine and handler tests run against
  scripted backends and simulation mode, never live models.
- The spec 11.11 parity matrix becomes a table-driven integration test over
  fixture pipelines; spec 11.12's smoke test runs with a scripted backend
  in CI and with a real harness in the live proof.
- Parser/schema agreement tests keep the generated JSON Schema honest.
- Existing hooks (lefthook: build, tests, linters) gate every slice.
- After each coherent implementation segment (one or several adjacent
  slices), Fable performs an open-ended review for spec completeness,
  actual behavior, idiomatic Go, and over-engineering. Material findings
  route back to implementation and the same reviewer checks the repair.
- The final review is unconditional and includes the implementation and the
  proof artifacts, not just test output.

## Live proof (final)

Using the real `tractor` CLI with both real adapters and credentials, in a
scratch git workspace:

1. `tractor run <file.json>` on a pipeline containing an agent-chosen loop
   with `max_visits` and a tool node — verify from `checkpoint.json`,
   `timeline.jsonl`, and stage outcomes that the loop genuinely revisited a
   node and terminated by budget or by chooser.
2. The same pipeline body passed through `--json` — identical
   behavior, proving both input paths.
3. A fan-out/fan-in pipeline — verify branches ran in separate worktrees
   (`branches.json`, `worktrees.jsonl`), converged, and the fan-in's
   recorded outcome cites branch evidence.
4. A composed run exercises both harnesses, reused and compacted sessions,
   live steering, interruption and resume from a fresh process, including
   branch turns bound to worktree paths. Existing adapter conformance is
   re-run and the backend/engine evidence is inspected against Sections
   11.13–11.14.

Evidence (commands, run-directory excerpts) is captured in an ephemeral
proof note. Done means all four runs pass inspection, not merely that
tests are green.
