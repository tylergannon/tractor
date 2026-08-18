# Attractor archive inventory

This is an inventory, not a roadmap. It records smart ideas in the archived
`tylergannon/attractor` repository that Tractor has not adopted. Inclusion here
does not imply that Tractor should implement an idea or preserve its original
shape.

The comparison uses Attractor's final default-branch revision,
[`0aca8b748e6ecc23446fc690d2b66690b77fe0d3`](https://github.com/tylergannon/attractor/tree/0aca8b748e6ecc23446fc690d2b66690b77fe0d3),
and Tractor at `37df42b07c8f3db41b176d51c243646be79eaa1f`. Attractor's
unmerged branches remain available in the archive but are outside this
inventory's implementation claims.

## What Tractor actually loses

Not every missing mechanism makes Tractor worse. The north star deliberately
removes routing policy from the engine and applies a cost razor to every field,
status, and endpoint. The useful comparison is therefore the concrete cost of
an omission, not the archive's feature count.

| Archived idea | Cost to Tractor today | Assessment |
| --- | --- | --- |
| [Stable model aliases](https://github.com/tylergannon/tractor/issues/11) | Pipeline authors must pin provider-native names everywhere, so coordinated upgrades are noisy and defaults can drift across entry points. | Clear small gap; preserve raw model IDs as an escape hatch. |
| [Sortable run IDs](https://github.com/tylergannon/tractor/issues/12) | Manifest IDs, directory names, CLI output, and MCP state use separate identity or ordering conventions, making runs harder to correlate by eye and by tooling. | Clear small gap; one opaque sortable ID should travel end to end. |
| [Usage and quota telemetry](https://github.com/tylergannon/tractor/issues/13) | Tractor records native events but cannot answer basic typed questions about token burn, context pressure, or quota reset windows. That blocks budgets and the north star's reserved usage-aware routing work. | Clear gap; normalize the stable common fields while retaining raw provider observations. |
| [Fully generated Codex protocol](https://github.com/tylergannon/tractor/issues/10) | Handwritten JSON-RPC envelopes and response shapes can silently drift from Codex app-server, leave capabilities undiscoverable, and turn protocol changes into runtime failures. | Clear correctness and maintenance gap; the complete checked-in protocol surface should be generated. |
| Structured command context | Tool nodes must use ambient environment or files for richer inputs and outputs, weakening validation, provenance, and composition. | Real but narrower gap; worth considering without creating a second workflow data plane. |
| One external-contract registry | Reviewers cannot enumerate and compile every CLI, MCP, graph, and harness receipt boundary from one place, so strictness can diverge between entry points. | Real auditability gap; useful if it remains generated from owners rather than duplicating schemas. |
| Focused authoring and harness docs | New users must reconstruct graph-language and provider-operation rules from the large spec, examples, and implementation. | Real discoverability gap with little runtime risk. |
| Explicit interrupt and close outcomes | Callers have less precise evidence that work stopped, output flushed, or a session was already absent. | Real lifecycle-observability gap, but lower priority than making the existing wire contract generated. |
| Author-configurable retry policy | Workflows cannot tune delay caps or jitter for expensive and rate-limited providers. | Situational gap; extra knobs must earn their cost and should not expose implementation accidents. |
| Settings groups | Repeated node-local policy can drift across large graphs. | A convenience gap, but the north star's cost razor favors explicit fields until repetition is demonstrated in real pipelines. |
| Rich outcome-directed routing and goal gates | None relative to the chosen model: adding them would let the engine interpret agent verdicts and recreate routing policy outside node occupants. | Deliberate exclusion; Tractor is better off without it. |
| Engine-level human and child-manager nodes | None for current scope: ordinary codergen/tool nodes and CLI-invoking nodes already express these patterns without new protocols. | Deliberate exclusion; reconsider only with a scenario ordinary nodes cannot express. |
| Native tool-call hooks | None at the Tractor layer: harnesses already own native tool policy, and another interception surface would duplicate it. | Deliberate exclusion. |
| Alternative parallel join policies | Tractor cannot race branches and accept the first success, but doing so creates cancellation, evidence, and workspace-ownership semantics absent from the normative convergence model. | A possible future mode, not a present deficit. |

The first four are the obvious near-term candidates. The middle group names
specific ways Tractor is worse off but still has to clear the cost razor. The
last group is retained as design history, not as a recommendation to reverse
ratified decisions.

## Working or substantially working ideas

### A richer outcome and routing data plane

Attractor lets a task return context updates, a preferred edge label, and an
ordered set of suggested node IDs. Its routing package evaluates conditions
first, then preferred labels, suggestions, edge weights, and finally target ID
as a deterministic tie-break. The sequential runner persists those decisions
and carries context into later nodes.

Tractor's harness outcome is deliberately smaller (`next` and `notes`), and its
edges have only `to` and `condition`. The Attractor design is attractive when a
workflow needs structured inter-node data without encoding every decision in a
prompt or adding a bespoke node.

Sources: [outcome and condition model](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/pipeline/conditions/eval.go),
[edge selection](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/pipeline/edgeselect/select.go),
and [runner result model](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/pipeline/runner/runner.go).

### Composable settings groups

Attractor groups settings into typed families such as model, retry, failure,
execution, session, parallel, human, manager, and tool hooks. A settings group
can target several nodes, later groups have higher priority, and node-local
values win field by field. The resolver and applicability checks are tested.

Tractor has a compact file-level `defaults` object for six common LLM fields,
plus node-local values. Settings groups would provide a clean way to express
cross-cutting policy for selected nodes without duplicating it or growing the
global defaults surface.

Sources: [typed settings](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/graph/graph.go)
and [resolution and validation](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/graph/runtime.go).

### Goal gates and explicit recovery targets

Attractor can mark completed nodes as required goal gates. Before exiting, the
runner checks that each gate succeeded; an unsatisfied gate can route back to a
node-specific repair target, with a fallback recovery target available in the
graph model.

Tractor can build review-and-repair loops with ordinary edges, but it has no
declarative assertion that a particular verification node must have succeeded
before the pipeline terminates. Goal gates make that invariant visible and
machine-checkable.

Sources: [goal and failure settings](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/graph/graph.go)
and [exit gate enforcement](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/pipeline/runner/runner.go#L965).

### First-class command-node context

Attractor command nodes accept an explicit shell and environment bindings with
`{{context.key}}` interpolation. The engine reserves `GOAL_FILE`, captures
stdout and stderr, records timing and exit status, and publishes successful
stdout back into workflow context.

Tractor's tool node is intentionally a smaller shell-command primitive. The
Attractor version is a useful reference if tool nodes ever need a structured
and auditable data boundary instead of relying on ambient environment state or
filesystem conventions.

Source: [command execution](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/pipeline/runner/runner.go#L299).

### Normalized usage and quota telemetry

Attractor's provider-neutral result includes input, output, cached, and total
tokens, context-window utilization, cost, and subscription-window snapshots.
The Codex client reads both five-hour and weekly rate-limit windows, while a
normalized event model can stream usage alongside assistant, thinking, tool,
warning, and error events.

Tractor preserves native harness events in JSONL but does not expose a typed
usage summary or quota-window model in its public harness outcome. The
Attractor contract is a strong basis for budgets, dashboards, and scheduling
without making the engine depend on Codex wire types.

Sources: [provider-neutral backend contract](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/harness_backend.go)
and [Codex usage decoding](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/codexapp/client.go#L602).

### A broad generated Codex protocol snapshot

Attractor checks in the generated Codex app-server JSON Schemas and Go types
for the complete protocol surface. Tractor generates and retains only the
request model it currently needs, then uses small hand-written JSON-RPC types
for the rest.

The original Tractor design explicitly chose narrow request generation and a
handwritten observed response surface. That choice leaves Tractor worse off:
wire drift is caught at runtime, protocol coverage is difficult to audit, and
new app-server capabilities remain invisible until someone manually models
them. The complete checked-in app-server schema and every Go wire type should
be generated, with handwritten code limited to behavior rather than protocol
shape.

Sources: [schema generator](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/codexapp/schema/generate.go)
and [generated types](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/codexapp/schema/types_gen.go).

### One registry for strict external contracts

Attractor centralizes the JSON Schemas for CLI and MCP receipt boundaries and
has a recursive linter for its required strict-output subset. Tractor validates
its graph and harness result schemas and uses typed MCP tool definitions, but
does not have one registry covering all external request documents.

The registry makes boundary review straightforward: every accepted document
shape can be enumerated, compiled, and checked for strictness in one place.

Source: [external contract registry](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/contracts/contracts.go).

### Sortable run IDs

Attractor uses lowercase ULIDs, preserving randomness while making creation
time recoverable and lexical order chronological. Tractor's manifest ID is a
cryptographically random hexadecimal value and its run-directory allocator
uses a separate timestamp convention.

A single sortable identifier would make logs easier to scan and correlate
across the CLI, MCP server, and filesystem.

Source: [ULID allocation](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/cmd/attractor/main.go#L589).

### Stable model aliases

Attractor owns a small resolver that maps friendly names to provider and model
IDs while still allowing raw model IDs. Tractor accepts provider and model
values directly and supplies implementation defaults, but has no user-facing
alias registry.

Aliases can keep workflow files readable and allow a coordinated model upgrade.
They also create a maintenance obligation, so the small isolated resolver is a
good pattern if Tractor adopts them. Without one, every authored graph becomes
its own model-version policy and repository-wide upgrades require noisy edits.

Source: [model alias resolver](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/internal/modelalias/modelalias.go).

### Focused authoring and harness documentation

Attractor includes a graph-authoring skill with separate language, examples,
and operations references, provider-specific harness notes, and a Codex
environment setup file that installs Lefthook automatically. Tractor has a
strong orchestration skill and runnable examples, but not the same compact
authoring reference or provider-operations notes.

This is low-risk material to borrow: it improves discovery and reproducibility
without changing runtime semantics.

Sources: [authoring skill](https://github.com/tylergannon/attractor/tree/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/skills/attractor),
[harness notes](https://github.com/tylergannon/attractor/tree/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/docs/harnesses),
and [Codex environment setup](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/.codex/environments/environment.toml).

## Good designs that the archive itself did not finish

These are worth retaining as design references, but should not be mistaken for
working Attractor features.

### Human and child-workflow manager nodes

The typed graph defines a human-choice node and a manager that can start,
observe, steer, and wait on a child graph. The sequential runner does not
execute either node. Tractor has neither concept as a first-class node.

The appealing part is the vocabulary: human decisions and hierarchical
orchestration become explicit graph semantics instead of hidden behavior in an
LLM prompt.

### Configurable retry timing and partial success

The graph model exposes initial delay, backoff factor, maximum delay, jitter,
and `allow_partial`. The runner explicitly rejects those settings. Tractor has
an internal capped exponential backoff with jitter, but authors can configure
only the retry count.

The design offers useful control for rate-limited or expensive providers, even
though neither repository currently exposes it as a complete author-facing
feature.

### Native tool-call pre/post hooks

Attractor models synchronous pre-call gates and post-call observers for native
harness tools, but the runner rejects them because the adapter capability was
never implemented. Tractor also has no native tool interception layer.

This remains a clean idea for policy checks, audit capture, or narrowly scoped
guardrails at the point where an agent is about to act.

### Explicit interrupt and close outcomes

Attractor's backend contract distinguishes acknowledged, not-running,
timed-out, and failed interruption or cleanup. Its first Codex adapter returns
`unsupported` for interrupt and close, so the lifecycle was not completed.
Tractor supports adapter interruption but has no equally rich public close
contract.

The status model is valuable because it prevents a caller from confusing a
request to stop work with proof that work actually stopped and output flushed.

### Alternative parallel join policies

Attractor's graph vocabulary includes `wait_all` and `first_success`, but its
sequential runner does not execute parallel nodes. Tractor implements real
fan-out/fan-in with bounded concurrency and isolated Git worktrees, but always
uses its normative convergence behavior rather than an author-selected join
policy.

`first_success` could be useful for redundant search or racing equivalent
strategies, provided cancellation and artifact ownership were specified as
carefully as Tractor's current parallel path.

## Things that are already in Tractor

The archive also contains many good choices that are not gaps: generated closed
JSON Schema, duplicate-key rejection, semantic linting, provider-neutral
harness boundaries, Codex app-server integration, MCP via `mcp-go`, Go
formatting and modernization tools, external steering, and checkpoint writing.
They are excluded from the inventory above because Tractor already has them,
often in a more complete form.
