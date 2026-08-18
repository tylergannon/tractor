# Upstream Attractor spec audit

Audited source: `tylergannon/attractor` `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`, read in full.

## Critical

### Supervisor session durability cannot meet the stated contract

Sections 3.10 and 12.1 require the engine to re-save the checkpoint when a supervisor binding opens, but `CodergenBackend` exposes only blocking `run_supervisor` and snapshot `bindings`; it has no binding-open notification or engine-owned open operation. The same section asks for the briefing exactly once per session, which is not crash-safe: a crash can occur after the binding checkpoint but before or during prompt delivery.

Suggested resolution: add a binding-open callback or event and persisted supervisor initialization state. Specify the briefing as idempotent at-least-once delivery unless an adapter supplies durable delivery acknowledgement.

## Issues

### The normative supervisor verdict schema is rejected by Codex strict output

Section 3.10 deliberately requires only `verdict`, but Codex's native strict
output API requires every declared root property to appear in `required`.
Passing the normative schema through unchanged makes every Codex supervisor
turn fail before dispatch.

Suggested resolution: permit harness adapters to translate optional root
properties into required nullable native fields, provided they remove native
null placeholders and validate the result against the unchanged normative
schema before returning it.

### Invalid chooser results are persisted as successful attempts

Section 3.5 writes `outcome.json` and updates `stages/latest` before Section 3.2 validates the chosen successor. A missing or invalid choice can therefore look successful in artifacts and supervisor digests before the run fails.

Suggested resolution: resolve and validate the successor before writing the outcome, latest pointer, or digest; write an error artifact for routing-validation failures.

### `start` can bypass branch-entry and fan-in-entry rules

`start_target` permits any walk node, while `branch_entry` and `fan_in_entry` inspect authored incoming routes only. A graph can therefore start inside a branch or at a fan-in that has no branch evidence.

Suggested resolution: treat `start` as a synthetic incoming route for these lints, or forbid branch-scoped and fan-in nodes as start targets.

### Parallel fan-in designation is circular and underspecified

The parallel node has no `fan_in` field, yet runtime refers to its designated fan-in and branch node-sets are defined relative to it. The specification does not define the topology algorithm for cycles, alternate paths, or multiple candidate joins.

Suggested resolution: add an explicit `fan_in` field, or normatively define the unique-first-fan-in traversal algorithm.

### Mandatory public artifacts lack wire schemas

`BranchResult` and `manifest.json` are described only in prose. Timeline events use constructor-like notation without a JSON discriminator, common fields, timestamps, or ordering contract, although conformance requires inspecting them.

Suggested resolution: define minimal open JSON object schemas for these artifacts and events.

### The initial checkpoint contradicts `current_node`

`current_node` is defined as the required ID of the last executed node, but the initial checkpoint exists before any node has executed.

Suggested resolution: make it optional for the initial checkpoint, or explicitly define the pending start node as its initial meaning.

### Scope-allocation failure assumes a stage directory exists

`make_execution_scope` may fail while creating the stage directory, but the retry path then requires `error.json` and an attempt digest at that directory.

Suggested resolution: distinguish pre-stage allocation failures and permit them to have no stage artifact or digest.

## Nits

- Section 3.4 ends with an orphaned sentence fragment about a designated target and terminal Error; it appears displaced from the Tool Handler routing text.
- `$goal` expansion is described as codergen-handler behavior even though supervisor prompts also require it; define one engine-level expansion operation.
- Supervisor errors are recorded, but the verdict digest and timeline event do not clearly require their category and message.
- Appendix A implies `next` appears only with multiple offered successors, while tool and parallel outcomes always provide it; say that it is required for multiple and permitted otherwise.

## Deliberate tradeoffs, not defects

Strict JSON, prose conditions chosen by agents, git-required wait-for-all parallelism, advisory non-fatal supervisors, coarse steering statuses, and the documented fan-in/branch-root visit-budget oddities are internally explicit choices.
