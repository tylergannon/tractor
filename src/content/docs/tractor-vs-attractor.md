---
title: Tractor vs. upstream Attractor
description: Tractor keeps Attractor's declarative graph and orchestration intent, but replaces several foundational contracts to fit real coding-agent harnesses.
eyebrow: Specification comparison
order: 2
sourceLabel: Upstream strongdm/attractor at fb57a55
sourceUrl: https://github.com/strongdm/attractor/blob/fb57a55ed97372a27ac90102f436947e29f48426/attractor-spec.md
---

Tractor is a Go implementation of its own Attractor variant. It shares upstream's core idea—describe an agent workflow as a directed graph, execute it deterministically, checkpoint progress, and keep the model behind a backend—but it is **not a drop-in implementation of upstream's DOT specification**.

This comparison uses [`strongdm/attractor`](https://github.com/strongdm/attractor) at commit [`fb57a55`](https://github.com/strongdm/attractor/commit/fb57a55ed97372a27ac90102f436947e29f48426) and Tractor's normative [`docs/spec.md`](https://github.com/tylergannon/tractor/blob/main/docs/spec.md), checked August 18, 2026.

## The short version

Upstream Attractor is a broad, presentation-neutral NLSpec with a DOT DSL and multiple extension seams. Tractor narrows that surface around typed data, existing coding-agent harnesses, inspectable run evidence, safe parallel Git work, and live steering.

| Concern           | Upstream strongdm/attractor                                                              | Tractor                                                                                                         |
| ----------------- | ---------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Pipeline syntax   | A constrained Graphviz DOT language                                                      | Closed typed JSON; YAML decodes through the same schema                                                         |
| Graph model       | Separate nodes and edges; shape can infer handler type                                   | Flat discriminated node union; each node owns its outgoing routes                                               |
| Routing           | Weighted and conditional edges interpreted by an algorithm                               | Agent chooser selects a prose-labelled target; tools route by exit code                                         |
| Built-in nodes    | Start, exit, codergen, wait-for-human, conditional, parallel, fan-in, tool, manager loop | Codergen, tool, parallel, fan-in, and supervisor; terminal states are pseudo-targets                            |
| Extensibility     | Custom handlers, lint rules, AST transforms, stylesheets, hooks                          | Closed graph language; extensions live in authored nodes or build-time code                                     |
| LLM layer         | Abstract `CodergenBackend`; implementations may call APIs or agents                      | Harness-backed sessions for Codex, Claude, and Antigravity/Gemini                                               |
| Human interaction | Dedicated interviewer interface and wait-for-human handler                               | An authoring pattern using agent contact, blocking tools, or external steering                                  |
| Parallel work     | Concurrent branches with merged context                                                  | Isolated-by-default or shared workspaces, per-branch Codergen settings, declared artifacts, and an agent fan-in |
| Supervision       | A manager-loop handler inside the walk                                                   | Supervisor nodes patrol declared scopes outside the walk and steer active turns                                 |
| Runtime control   | Events, cancellation, optional HTTP server, tool hooks                                   | Events/run logs, Unix control socket, detached MCP runs, steering, and native compaction                        |

## What Tractor preserves

The two specifications agree on the important shape of the problem:

- Pipelines are declarative directed graphs rather than imperative orchestration scripts.
- The engine owns traversal, retries, checkpointing, and deterministic lifecycle transitions.
- LLM work enters through a backend boundary rather than leaking into graph mechanics.
- Context fidelity is explicit because later stages need a defined relationship to earlier work.
- Parallel branches converge through a fan-in stage.
- Validation happens before execution, and runs produce observable events and artifacts.

That shared lineage is why Attractor terminology still fits. The differences below are deliberate changes to the contract, not incidental Go implementation details.

## Typed data replaces DOT

Upstream chooses DOT because workflows are graphs and Graphviz offers mature rendering and editing tools. Its DSL includes attribute blocks, subgraphs, defaults, chained edges, classes, and a model stylesheet.

Tractor chooses a closed typed document instead:

```json
{
  "goal": "Ship a verified change",
  "start": "work",
  "nodes": [
    {
      "id": "work",
      "type": "codergen",
      "prompt": "Implement $goal.",
      "edges": [{ "to": "success" }]
    }
  ]
}
```

Every node has a required `type`, only fields for that type are allowed, and routing sits on the node that makes the choice. JSON is canonical; YAML is a more humane spelling of the same schema. Tractor gives up DOT's native visualization and stylesheet conveniences in exchange for generated schemas, editor completion, unambiguous decoding, and fewer runtime transforms.

## Routing moves to the decision-maker

Upstream uses an edge-selection algorithm: evaluate conditions against context, prefer labelled edges matching an outcome, then use weight and lexical order as fallbacks. It includes a condition expression language for machine evaluation.

Tractor makes two narrower routing contracts:

1. A model-backed chooser receives the currently offered successors as a strict output schema and selects one using prose `condition` text.
2. A tool node follows `on_success` or `on_error` directly from its exit code.

The engine never parses a model edge condition as an expression. This puts semantic judgment with the agent that just did the work, keeps mechanical checks mechanical, and removes a second expression language from the pipeline format.

## A closed node vocabulary replaces plugin points

Upstream explicitly specifies custom handlers, custom lints, AST transforms, graph composition, model stylesheets, and tool-call hooks. It is designed as a broad orchestration platform.

Tractor applies a stricter cost razor. The graph admits five node types and no arbitrary attributes. New behavior normally belongs in:

- a `codergen` prompt using tools available through its harness;
- a `tool` command or authored program;
- a separate child pipeline launched by an ordinary node; or
- build-time code that emits a valid Tractor graph.

This is less extensible by configuration and more predictable at runtime. Unknown syntax fails early instead of becoming an undocumented extension point.

## Existing harnesses become the execution substrate

Upstream intentionally leaves `CodergenBackend` open: an implementation can call an LLM API, spawn a CLI agent, or provide its own coding loop.

Tractor specifies the harness-backed path in detail. Its adapters continue native Codex, Claude, or Antigravity sessions; preserve harness event semantics in run-log segments; validate structured outputs; interrupt active turns for steering; and compact conversations through the harness's native mechanism.

The backend receives resolved turns—not graph objects—and returns semantic outcomes. Provider protocol details stay below the neutral engine boundary.

## Parallelism is Git-aware

Both specifications fan out work and converge at a fan-in. Tractor adds explicit workspace and artifact semantics because concurrent coding agents editing one directory is not always a useful abstraction.

Legacy string branches reference authored branch roots and can walk several nodes before joining. Structured branches instead synthesize one ordinary Codergen node per branch. Each structured branch inherits the parallel node's Codergen configuration, can selectively override fields such as provider or model, and must declare the files or directories it produces.

With the isolated default, Tractor:

- freezes one parent repository state;
- creates an engine-owned isolated Git worktree;
- runs the branch with that worktree as its workspace;
- for structured branches, collects each declared artifact into durable fan-out stage evidence before worktree cleanup;
- records branch paths, outcomes, stage directories, and run-log segments in `branches.json`, plus workspace and artifact metadata for structured branches; and
- gives the fan-in agent the resulting branch evidence and collected artifact paths to inspect and consolidate.

An author can explicitly select a shared workspace instead. All branches then run in the caller's directory, declared artifacts remain at their live paths, and Tractor adds no write-conflict protection. Shared mode is for coordinated branches with intentionally distinct or managed outputs, not an accidental escape from isolation.

Legacy branch node sets must remain disjoint until the join. A failed or interrupted fan-out replays as one unit rather than pretending partially completed branches form a coherent checkpoint.

## Human interaction becomes a pattern

Upstream has a first-class interviewer interface, question and answer models, timeout policies, and built-in console, callback, auto-approve, and queue implementations.

Tractor does not define a human node. Authors choose the primitive that matches the decision:

- a codergen node can contact a person through Slack, email, or another harness tool and interpret the response;
- a tool node can run a deterministic program that blocks for an answer; or
- an external operator can watch events and steer an active agent turn.

This avoids standardizing a second interaction protocol while being explicit about the tradeoff: external delivery is not transactional, and retries can duplicate a contact attempt.

## Supervision moves outside the walk

Upstream's manager loop is a handler that executes a child graph repeatedly and uses an LLM to decide whether to stop. Tractor instead adds `supervisor` nodes that never participate in traversal.

A supervisor declares which nodes it watches. While any are active, the engine periodically builds a digest from their events and asks the supervisor for one of two verdicts: `ok`, or `steer` with a target and message. This makes supervision longitudinal and advisory; it can correct live work without becoming another success gate.

## Tractor adds an operational control plane

Upstream describes an event stream, cooperative cancellation, optional HTTP endpoints, and frontend separation. Tractor commits to concrete local control surfaces:

- append-only run-log segments and durable stage evidence;
- uniform checkpoints with native session bindings;
- a Unix control socket for steering and compaction;
- detached runs registered under local state so MCP restarts do not own their lifetime; and
- deferred Codex MCP tools for validation, start, status, steering, stop, and schema discovery.

The result is intentionally local-first and inspectable. A frontend can still consume the evidence, but the core proof is on disk and the running agent can be controlled directly.

## Which specification should you use?

Use upstream Attractor as the reference if you want DOT authoring, its expression language, pluggable handlers and transforms, a standardized interviewer, or a general platform contract independent of a particular coding-agent runtime.

Use Tractor's specification if you want a ready Go implementation centered on Codex/Claude/Antigravity sessions, typed JSON or YAML, agent-owned routing, isolated Git branch work, live steering, in-graph supervision, and Codex plugin operation.

For Tractor itself, [`docs/spec.md`](https://github.com/tylergannon/tractor/blob/main/docs/spec.md) is the sole normative authority. When the implementation, examples, generated schema, or this site disagree with it, the specification wins.
