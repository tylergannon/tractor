<!--
  This is the normative specification for this project.

  It is derived from the upstream StrongDM Attractor specification
  (docs/attractor-spec.md, kept as a pristine read-only reference) with the
  minimum changes required by the project north star
  (ephemeral/projects/spec-rebuild/north-star.md). Every divergence from
  upstream must be derivable from the north star; anything else is drift.
-->

# Attractor Specification

A pipeline runner that uses directed graphs (defined in a typed JSON schema) to orchestrate multi-stage AI workflows. Each node in the graph is a task (LLM call, tool check, parallel fan-out, etc.) and each node's edges define the flow onward from it.

---

## Table of Contents

1. [Overview and Goals](#1-overview-and-goals)
2. [JSON Pipeline Schema](#2-json-pipeline-schema)
3. [Pipeline Execution Engine](#3-pipeline-execution-engine)
4. [Node Handlers](#4-node-handlers)
5. [State](#5-state)
6. [Human-in-the-Loop (Authoring Pattern)](#6-human-in-the-loop-authoring-pattern)
7. [Validation and Linting](#7-validation-and-linting)
8. [Model Selection](#8-model-selection)
9. [Extensibility](#9-extensibility)
10. [Observability and Events](#10-observability-and-events)
11. [Definition of Done](#11-definition-of-done)
12. [Harness-Backed Backends](#12-harness-backed-backends)

---

## 1. Overview and Goals

### 1.1 Problem Statement

AI-powered software workflows -- code generation, code review, testing, deployment planning -- often require multiple LLM calls chained together with conditional logic, human approvals, and parallel execution. Without a structured orchestration layer, developers either write fragile imperative scripts or build ad-hoc state machines that are difficult to visualize, version, or debug.

Attractor solves this by letting pipeline authors define multi-stage AI workflows as directed graphs in a typed JSON schema. The graph is the workflow: nodes are tasks, each node carries its own outgoing edges, and fields configure behavior. The result is a declarative, version-controllable pipeline definition that an execution engine can walk while recording every step and every choice.

### 1.2 Why a Typed JSON Schema

JSON is chosen as the pipeline definition format for several reasons:

- **Typed and machine-checkable.** JSON's native types (strings, numbers, booleans, objects, arrays) map directly onto the schema's field types, and the whole definition can be validated structurally before a single semantic lint runs. A published JSON Schema gives editors completion and inline validation for free.
- **Edges belong to their origin node.** Each node object carries its own `edges` array, so a node's routing -- where it can go and the conditions that drive the choice -- reads as one unit, both while authoring and while reviewing. There is no separate edge section to cross-reference.
- **Universally toolable.** Every language, editor, and diff tool handles JSON. Pipelines are generated and consumed by programs (including coding agents) at least as often as by hands.
- **Constrained extensibility.** Unknown fields are errors, which catches typos; deliberate extension has a typed home (the node `custom` object, Section 2.5) rather than a loose attribute bag.

The upstream Attractor specification defined its pipelines in Graphviz
DOT. This specification replaces that surface: graph structure is the
same (directed graph, one start, one exit), but the concrete syntax,
shape-based node typing, subgraph-scoped defaults, and edge labels are
gone. Visualization is an exporter's job -- the JSON model converts to
DOT mechanically for rendering when a picture is wanted.

### 1.3 Design Principles

**Declarative pipelines.** The pipeline file declares what the workflow looks like and what each stage should do. The execution engine decides how and when to run each stage. Pipeline authors do not write control flow; they declare graph structure.

**Pluggable handlers.** Each node type (LLM call, tool, parallel fan-out) is backed by a handler that implements a common interface. New node types are added by registering new handlers. The execution engine does not know about handler internals.

**Checkpoint and resume.** After each top-level node completes, the execution engine saves a serializable checkpoint. If the process crashes, execution resumes from the last checkpoint.

**Human-in-the-loop.** Human participation is an authoring pattern, not an engine protocol: a node that needs a person contacts them with its own tools (or blocks in a tool command) and routes on their answer, and an external operator supervises any run through its events and steering surfaces (Section 6). This supports approval gates, code review, and manual override without a design-time interview protocol.

**Every routing decision has a chooser.** When a node's outgoing edges are alternative successors, the choice of which edge to follow is made by an intelligence positioned at that node -- the LLM agent that just executed the stage (possibly relaying a human it interviewed, Section 6), or a command's exit code -- never by the engine evaluating expressions. (The one structural exception is the parallel node's fan-out, whose edges are concurrent branches rather than alternatives: all of them execute, and no choice exists, Section 4.8.) The engine offers the successors; the occupant chooses. This deliberately trades upstream's expression-based deterministic routing for observable judgment: routing policy lives in prompts, and every choice is recorded in the run directory -- the engine writes each execution's Outcome, `next` and notes together, to its stage directory (Appendix C). Edge `condition` fields are the routing conditions presented to the chooser (Section 4.5) -- prose for an intelligence to read, never strings for the engine to match.

### 1.4 Layering and LLM Backends

Attractor defines the orchestration layer: graph definition, traversal, state management, and extensibility. It does NOT require any specific LLM integration. The codergen handler (Section 4.5) needs a way to call an LLM and get a response -- how you provide that is up to you.

The codergen handler takes a backend that conforms to the `CodergenBackend`
interface (Section 4.5). What that backend does internally is entirely up to the
implementor -- drive an existing coding-agent harness (Claude Code, Codex,
Gemini CLI), spawn CLI agents in subprocesses, run agents in tmux panes with a
manager attaching to them, call an LLM API directly, or anything else. The
pipeline definition (the JSON file) does not change regardless of backend choice.
That said, existing harnesses already provide a hardened agent loop, tool
execution, and durable session storage, so wrapping one is usually the
objectively better choice today; see Harness-Backed Backends (Section 12) for
guidance on implementing such wrappers.

Attractor pipelines are driven by an event stream (Section 10). TUI, web, and IDE frontends consume events and operate the run through its steering surface (Sections 3.9, 6). The pipeline engine is headless; the presentation layer is separate.

---

## 2. JSON Pipeline Schema

### 2.1 Document Shape

A pipeline is one JSON document: a single object carrying the pipeline's
metadata, file-level defaults, and a flat array of nodes. **Edges belong
to their origin node**: each node object carries its own `edges` array,
so a node's routing -- where it can go, and the condition that selects
each target -- reads as one unit. There is no separate edge list.

```json
{
  "name": "branch",
  "goal": "Implement and validate a feature",
  "defaults": { "timeout": "900s" },
  "nodes": [
    {
      "id": "implement",
      "type": "codergen",
      "prompt": "Implement the plan. Choose: validate, or replan.",
      "max_visits": 5,
      "edges": [
        { "to": "run_tests", "condition": "Validate my changes" },
        { "to": "plan", "condition": "The plan proved unworkable" }
      ]
    }
  ]
}
```

The document is strict JSON (RFC 8259, UTF-8): no comments, no trailing
commas. Annotation belongs in `label` and `prompt` fields; pipelines
that want richer authoring affordances are generated.

### 2.2 Value Types

| Type     | JSON representation             | Examples                             |
|----------|---------------------------------|--------------------------------------|
| String   | JSON string                     | `"Hello world"`, `"line1\nline2"`    |
| Integer  | JSON number without fraction    | `42`, `-1`, `0`                      |
| Float    | JSON number                     | `0.5`, `-3.14`                       |
| Boolean  | JSON boolean                    | `true`, `false`                      |
| Duration | JSON string: integer + unit     | `"900s"`, `"15m"`, `"2h"`, `"250ms"`, `"1d"` |

`null` is not a value: an unset field is an absent field. A field set to
`null` is a parse error.

### 2.3 Top-Level Fields

| Field      | Type       | Required | Description |
|------------|------------|----------|-------------|
| `name`     | String     | no       | Display name for the pipeline (UI, telemetry). |
| `goal`     | String     | no       | Human-readable goal. Exposed as `$goal` in prompt templates via `ExecutionScope.goal` (Section 4.1). |
| `defaults` | Object     | no       | File-level node defaults (Section 2.7). |
| `nodes`    | Node array | yes      | The graph. Exactly one `start` and one `exit` node (Section 7.2). |

### 2.4 Nodes: a Discriminated Union

A node's `type` field is required and selects both its handler
(Section 4.2) and its schema: each type admits only its own fields, so a
`tool` node cannot carry a `prompt` and a `codergen` node cannot carry a
`tool_command` -- structural validation catches the mismatch before any
lint runs. There is no default node type and no shape-based inference;
every node names what it is.

Fields common to every node type:

| Field         | Type       | Default   | Description |
|---------------|------------|-----------|-------------|
| `id`          | String     | required  | Node identity. Must match `[A-Za-z_][A-Za-z0-9_]*` and be unique in the file; edges and `on_fail` reference it. |
| `type`        | String     | required  | Discriminator: a built-in type (Section 2.5) or a registered custom type (Section 9). An unregistered value is a lint ERROR (`type_known`) and a terminal resolution Error (Section 4.2). |
| `label`       | String     | node ID   | Display name shown in UI, prompts, and telemetry. |
| `edges`       | Edge array | `[]`      | This node's outgoing edges (Section 2.6). Empty on the exit node. |
| `max_visits`  | Integer    | unset     | Visit budget: how many times the walk may dispatch this node in one run (a visit spans its retry attempts and any failure-resume, Section 3.4). An exhausted node is no longer offered as a successor (Section 3.4). Unset means unlimited. |

`max_visits` is common because the engine budgets visits to every
target. `max_retries` is not: it lives only on the node types whose
handler can return a *retryable* Error (Section 3.5) -- `codergen`,
`parallel.fan_in`, and custom types. The other built-ins produce only
terminal or interrupted Errors, so the field would be inert
configuration there.

### 2.5 Node Types and Their Fields

| Type              | Description |
|-------------------|-------------|
| `start`           | Pipeline entry point. No-op handler. Exactly one per pipeline; exactly one outgoing edge (Section 3.2). No type-specific fields. |
| `exit`            | Pipeline exit point. No-op handler. Exactly one per pipeline; no outgoing edges. No type-specific fields. |
| `codergen`        | LLM task (code generation, analysis, planning). |
| `parallel`        | Parallel fan-out. Executes multiple branches concurrently (Section 4.8). |
| `parallel.fan_in` | Parallel fan-in. Consolidates branch results and chooses what follows (Section 4.9). |
| `tool`            | External tool execution (shell command). Routes on exit code (Section 4.7). |

**`codergen` and `parallel.fan_in` fields** (both run LLM turns; the
fan-in's turn evaluates branch evidence):

| Field              | Type     | Default       | Description |
|--------------------|----------|---------------|-------------|
| `prompt`           | String   | `""`          | Primary instruction for the stage. Supports `$goal` variable expansion. Falls back to `label` if empty. |
| `max_retries`      | Integer  | inherited     | Additional attempts when a turn fails with a retryable error (Section 3.5). If omitted, inherits `defaults.max_retries`. `max_retries=3` means up to 4 total attempts. Never applies to routing decisions. |
| `fidelity`         | String   | inherited     | Context fidelity mode for this node's LLM session. See Section 5.4. |
| `thread_id`        | String   | node ID       | Thread key for LLM session reuse under session-reusing fidelity modes. Unset means the node owns its own thread (Section 5.4). |
| `timeout`          | Duration | unset         | Maximum duration of one turn (Section 12.2, note 9). Expiry interrupts the attempt (Section 3.7). Leave it unset or generous on nodes that wait for a person (Section 4.6). |
| `llm_model`        | String   | inherited     | LLM model identifier (Section 8). |
| `llm_provider`     | String   | auto-detected | LLM provider key. Auto-detected from model if unset. |
| `reasoning_effort` | String   | inherited     | LLM reasoning effort: `low`, `medium`, `high` (Section 8; recommended system default `high`). |

**`tool` fields:**

| Field          | Type     | Default  | Description |
|----------------|----------|----------|-------------|
| `tool_command` | String   | required | Shell command to execute (Section 4.7). |
| `on_fail`      | String   | `""`     | ID of the successor to follow on a nonzero exit code (Section 4.7). |
| `timeout`      | Duration | unset    | Maximum duration of the command (Section 4.7). |

**`parallel` fields:**

| Field          | Type    | Default | Description |
|----------------|---------|---------|-------------|
| `max_parallel` | Integer | `4`     | Maximum branches walked concurrently (Section 4.8). Parallel nodes have no `timeout` of their own. |

**Custom types** (Sections 4.10, 9) are registered by string. A node
whose `type` is not a built-in validates against the generic shape: the
common fields of Section 2.4, plus `max_retries` (a custom handler may
return retryable Errors), plus one extension field --

| Field    | Type   | Default | Description |
|----------|--------|---------|-------------|
| `custom` | Object | `{}`    | Opaque to the engine: the typed home for custom-handler configuration. The engine carries it to the handler unread. |

-- and its `type` must be registered at run time: an unregistered value
never falls through to another handler (Section 4.2).

The external field name is `type`. Implementations may use an internal
field name such as `node_type` to avoid reserved-word conflicts, but the
externally visible behavior must remain identical.

Upstream's shape-driven node typing is gone with the DOT syntax, and
three upstream node types are intentionally omitted with it: the
conditional routing point driven by edge condition expressions
(`diamond`), the in-graph supervisor loop (`house`), and the
Interviewer-backed human gate (`hexagon`/`wait.human`). Routing
decisions belong to choosers (Section 3.3), supervision belongs to
external operators using the observation and steering surfaces
(Section 3.9), and human interaction is an authoring pattern over
existing node types (Sections 4.6, 6).

### 2.6 Edge Fields

| Field       | Type   | Required | Description |
|-------------|--------|----------|-------------|
| `to`        | String | yes      | Target node ID. Must name a node in this file (Section 7.2). |
| `condition` | String | at branch points | The routing condition presented to the chooser (Sections 3.3, 4.5, 4.6): prose stating when this edge is the right choice. REQUIRED -- present and non-empty after trimming whitespace -- on every edge of a choice-schema branch point: a `codergen` or `parallel.fan_in` node with more than one edge (lint ERROR `edge_condition_missing`, Section 7.2). Optional elsewhere, as documentation. The engine never reads or matches condition text. |

The `condition` is for the model, not the machine. Upstream defines
edge-level `condition` as an evaluated guard expression, plus `weight`,
`fidelity`, `thread_id`, and `loop_restart`; this specification omits
all of them. Routing is chooser-based (Section 3.3), so edges carry no
guard expressions or priorities, and session policy is node-scoped
(Section 5.4).

### 2.7 Defaults and Resolution

The top-level `defaults` object holds file-level defaults for node
fields. Resolution for every defaultable field is per field and stops at
the first value found:

1. The field on the node itself.
2. The same field in `defaults`, if that node's type has the field.
3. Otherwise the system default from the tables above.

`defaults` admits exactly six fields -- the ones whose node tables say
"inherited" plus the two global execution knobs:

`max_retries`, `fidelity`, `timeout`, `llm_model`, `llm_provider`,
`reasoning_effort`

Anything else in `defaults` is a parse error. Identity, typing, routing,
and each node's own work are always explicit at the node: `id`, `type`,
`edges`, `on_fail`, `prompt`, `tool_command`, `thread_id`,
`max_parallel`, `max_visits`, and `custom` cannot be defaulted, so a
reader of a node sees everything that decides where it routes and what
it does. A default for a field a node type lacks (e.g. `max_retries`
for a `tool` node, Section 2.5) simply does not apply to nodes of that
type.

Requiredness is structural and checked before default resolution:
a required field (`tool_command` on a `tool` node) must appear inline on
the node, and `defaults` can never satisfy it -- which keeps the
published JSON Schema (Section 2.8) and the parser in exact agreement.

There is no scoping below the file. Upstream's subgraph-scoped default
blocks (and the CSS-like class derivation on top of them) are gone with
DOT, and the prior JSON draft's settings groups are not adopted: a group
of nodes that shares a thread or a model names it on each node, where a
reader sees it, rather than inheriting it from an enclosing block.

### 2.8 Structural Constraints

Violations of document structure are **parse errors** (the run never
starts, Section 3.1); graph-shape problems found after parsing are
**lints** (Section 7):

- The file is one JSON object with the top-level fields of Section 2.3.
- **Duplicate member names within any JSON object are a parse error**,
  at every level, checked before schema and default processing. RFC 8259
  leaves duplicate handling implementation-defined (last-wins,
  first-wins, or error); a strict pipeline format cannot let two
  conforming JSON libraries read two different workflows from one file.
- Every entry in `nodes` is an object with a valid, unique `id` and a
  `type`. Duplicate IDs are a parse error.
- Every field has the type its table declares; `null` appears nowhere.
- **Unknown fields are a parse error**, at every level -- top-level,
  node, and edge -- including a field that exists on some other node
  type but not this one (`prompt` on a `tool` node). This catches
  typos (`max_visit` for `max_visits`) that a loose format would
  silently swallow. Deliberate extension data lives in a custom-type
  node's `custom` object, which the engine never inspects.

Implementations SHOULD publish this schema as a JSON Schema document
(the node union expressed with `type` as the discriminator) so editors
can validate and complete pipeline files as they are written.

### 2.9 Examples

**Simple linear workflow:**

```json
{
  "name": "simple",
  "goal": "Run tests and report",
  "nodes": [
    { "id": "start", "type": "start", "edges": [{ "to": "run_tests" }] },
    { "id": "run_tests", "type": "codergen",
      "prompt": "Run the test suite and report results",
      "edges": [{ "to": "report" }] },
    { "id": "report", "type": "codergen",
      "prompt": "Summarize the test results",
      "edges": [{ "to": "exit" }] },
    { "id": "exit", "type": "exit" }
  ]
}
```

**A loop with an agent chooser, a mechanical check, and a visit budget:**

```json
{
  "name": "branch",
  "goal": "Implement and validate a feature",
  "defaults": { "timeout": "900s" },
  "nodes": [
    { "id": "start", "type": "start", "edges": [{ "to": "plan" }] },
    { "id": "plan", "type": "codergen",
      "prompt": "Plan the implementation",
      "edges": [{ "to": "implement" }] },
    { "id": "implement", "type": "codergen",
      "prompt": "Implement the plan. When you finish, choose: proceed to validation, or return to planning if the plan proved unworkable.",
      "max_visits": 5,
      "edges": [
        { "to": "run_tests", "condition": "Validate my changes" },
        { "to": "plan", "condition": "The plan proved unworkable; replan" }
      ] },
    { "id": "run_tests", "type": "tool",
      "tool_command": "./run_tests.sh", "on_fail": "implement",
      "edges": [
        { "to": "exit", "condition": "Tests pass" },
        { "to": "implement", "condition": "Tests failed" }
      ] },
    { "id": "exit", "type": "exit" }
  ]
}
```

`implement` has two edges, so the agent chooses one when its turn ends --
each edge's `condition` is the text it chooses by. `run_tests` routes
mechanically: exit code 0 follows the normal edge to `exit`; nonzero
follows `on_fail` back to `implement` (its `condition` strings are
documentation only -- a tool node consults the exit code, not a model).
The `max_visits` budget bounds the loop: once `implement` has run five
times it is no longer offered, so a sixth visit cannot be chosen and the
run fails instead of looping forever (Section 3.4).

**Human approval (authoring pattern, Sections 4.6, 6):**

```json
{
  "name": "review",
  "nodes": [
    { "id": "start", "type": "start", "edges": [{ "to": "review_gate" }] },
    { "id": "review_gate", "type": "codergen",
      "prompt": "Ask Tyler on Slack whether these changes ship. Poll for his reply with exponential backoff. Route on his answer.",
      "edges": [
        { "to": "ship_it", "condition": "Approved" },
        { "to": "fixes", "condition": "Fixes requested" }
      ] },
    { "id": "ship_it", "type": "codergen", "prompt": "Ship the changes",
      "edges": [{ "to": "exit" }] },
    { "id": "fixes", "type": "codergen", "prompt": "Apply the requested fixes",
      "edges": [{ "to": "review_gate" }] },
    { "id": "exit", "type": "exit" }
  ]
}
```

`review_gate` is an ordinary codergen node: the agent conducts the
interview with its own tools and the human's decision arrives through
the same choice schema as every other routing decision. A deterministic
variant is a `tool` node whose command blocks until a response arrives
and exits 0 or nonzero (Section 4.6).

## 3. Pipeline Execution Engine

### 3.1 Run Lifecycle

The execution lifecycle proceeds through five phases:

```
PARSE -> VALIDATE -> INITIALIZE -> EXECUTE -> FINALIZE
```

1. **Parse:** Read the pipeline JSON and produce an in-memory Graph model (nodes with their edges and fields), applying file-level defaults (Section 2.7) and rejecting structural violations (Section 2.8).
2. **Validate:** Run lint rules (Section 7). Reject invalid graphs. Warn on suspicious patterns.
3. **Initialize:** Create the run directory, the initial engine state (Section 5.1), and the initial checkpoint.
4. **Execute:** Traverse the graph from the start node, executing handlers and following chosen successors (Section 3.3).
5. **Finalize:** On completion, write the final checkpoint (`current_node` is the terminal node, `next_node` is empty, Section 5.3) and clean up resources, including any remaining branch worktrees (Section 4.8). On failure, write the failure checkpoint naming the failed node as `next_node` (Section 3.7) and leave branch worktrees **in place** -- a resumed fan-in needs the evidence they hold (Section 4.9). When an operator stops the run, the engine sets the run's `StopSignal` (Section 4.1) and calls the backend's `interrupt_all()` (Section 12.1); every in-flight handler returns an interrupted Error promptly, and the failure checkpoint is written through the ordinary failure path (Section 3.2) -- the engine also observes the signal itself between executions and between retry attempts (Sections 3.2, 3.5), so a stop that lands while no handler is in flight still ends the run at the next dispatch point (writing no new checkpoint there -- the last one already stands correct, Section 3.7). Only after any failure checkpoint lands are sessions closed and files released.

### 3.2 Core Execution Loop

The following pseudocode defines the execution engine's traversal algorithm. This is the heart of the system.

```
FUNCTION run(graph, config):
    state = new EngineState()      -- typed engine bookkeeping (Section 5.1):
                                   -- completed_nodes, node_visits,
                                   -- node_attempts, last_stage,
                                   -- last_response, seq
    stop = new StopSignal()        -- one-shot operator-stop signal
                                   -- (Section 4.1); config wires it to the
                                   -- process's stop surface

    current_node = find_start_node(graph)
        -- The unique node with type="start" (lint-guaranteed, Section 7.2)
        -- Raises error if not found

    WHILE true:
        node = graph.nodes[current_node.id]

        -- Step 1: Check for terminal node, then for an operator stop
        IF is_terminal(node):
            RETURN RunResult(status=COMPLETED)
        IF stop.is_set():
            -- a stop that landed while no handler was in flight: write
            -- NOTHING. The last checkpoint already names this node as
            -- next_node with the correct retry_visit -- a fresh
            -- checkpoint here would clobber a pending retry_visit from
            -- a failure checkpoint the run just resumed from,
            -- double-billing that node's visit (Section 3.7)
            RETURN RunResult(status=FAILED,
                             failure_reason="stopped by operator")

        -- Step 2: Compute the offered successors (Section 3.3)
        offered = offered_successors(node, graph, state.node_visits)
        IF offered is empty:
            -- pre-execution failure: nothing ran, so no new checkpoint;
            -- the last checkpoint already names this node (Section 3.7)
            RETURN RunResult(status=FAILED,
                failure_reason="every successor of " + node.id +
                               " has exhausted its visit budget")

        -- Step 3: Resolve the handler and execute the node; retryable
        --         turn errors are retried (Section 3.5); any surviving
        --         error fails the run
        handler, rerr = registry.resolve(node)   -- Section 4.2
        IF rerr is not NONE:
            -- pre-execution failure (unknown explicit type): nothing
            -- ran, so no new checkpoint (Sections 3.7, 4.2)
            RETURN RunResult(status=FAILED, failure_reason=rerr.message)
        state.node_visits[node.id] += 1
            -- skipped exactly once when resuming from a failure
            -- checkpoint with retry_visit set: that execution continues
            -- the already-counted visit (Section 5.3)
        outcome, error = execute_with_retry(node, handler, offered, graph,
                                            state, stop,
                                            workdir=run_workspace)
            -- run_workspace = config's workspace path; branch walks pass
            -- their worktree instead (Section 4.8)
        IF error is not NONE:
            save_failure_checkpoint(state, node.id, retry_visit=true)
                -- Section 3.7: next_node = the failed node,
                -- completed_nodes untouched, counters and sessions
                -- recording what actually happened; retry_visit because
                -- this node consumed its visit at Step 3 (Section 5.3)
            RETURN RunResult(status=FAILED, failure_reason=error.message)

        -- Step 4: Resolve the chosen successor from the Outcome
        IF outcome.next is set:
            next_id = outcome.next    -- supplied by the node's chooser; for
                                      -- a parallel node this is its fan-in
                                      -- (Section 4.8), valid despite not
                                      -- being an offered edge
            IF next_id NOT IN allowed_targets(node, offered, graph):
                -- allowed_targets = offered targets, plus the designated
                -- fan-in when node is a parallel node
                save_failure_checkpoint(state, node.id,
                                        retry_visit=true)
                RETURN RunResult(status=FAILED,
                    failure_reason="chooser named an unoffered successor")
        ELSE IF size(offered) == 1:
            next_id = offered[0].to   -- no decision existed
        ELSE:
            save_failure_checkpoint(state, node.id, retry_visit=true)
            RETURN RunResult(status=FAILED,
                failure_reason="handler supplied no choice among " +
                               size(offered) + " offered successors")

        -- Step 5: Record completion
        state.completed_nodes.append(node.id)
        state.last_stage = node.id
        state.last_response = truncate(outcome.notes, 200)

        -- Step 6: Save checkpoint, then advance
        save_checkpoint(create_checkpoint(state, node.id, next_id),
                        logs_root)
        current_node = graph.nodes[next_id]
```

`RunResult` is the run-level verdict: `COMPLETED` when the walk reaches the
exit node, `FAILED` with a reason otherwise. It is deliberately distinct from
the node-level Outcome (Section 5.2), which carries no success vocabulary --
a node's execution either produces an Outcome or an Error, and "how well it
went" is expressed by where the chooser routed and what its notes say.

Advance dispatches on the Outcome's `next`, never on the offered count alone:
a handler that made a choice is always honored (a parallel node with a single
offered branch still routes to its fan-in), and a handler that made none
falls through to the lone offered successor. The `parallel` handler is the
one exception to the `next`-must-be-offered rule: its `next` names the fan-in
node where the branches converge (Section 4.8), and the engine accepts it.

The engine checkpoints after every top-level execution, success or failure
-- one node visit, comprising all of its retry attempts (Section 3.5); no
checkpoint is written between attempts. A success
checkpoint is saved only after the routing question is answered: it carries
the resolved successor (`next_node`, Section 5.3), so resume continues the
walk instead of re-deriving -- or re-asking -- a choice. A failure
checkpoint names the failed node itself as `next_node` (Section 3.7), so
resume retries it. A crash between checkpoints re-runs only the genuinely
in-flight node's work -- including any retry attempts already spent, whose
stage directories and orphaned harness sessions survive as evidence but
carry no engine state forward (Section 5.3).

### 3.3 Successor Choice

Routing has no expression language, no priorities, and no fallback ladder.
When a node finishes, exactly one question is answered -- "which offered
successor comes next?" -- and it is answered by the node's chooser, never by
the engine.

**Offered successors.** Before executing a node, the engine computes the
offered set: the node's outgoing edges, minus any edge whose target has
exhausted its visit budget (Section 3.4).

```
FUNCTION offered_successors(node, graph, node_visits) -> List<Edge>:
    result = []
    FOR EACH edge IN node.edges:
        target = graph.nodes[edge.to]
        IF target.max_visits is set AND node_visits[target.id] >= target.max_visits:
            CONTINUE      -- exhausted; not offered
        result.append(edge)
    RETURN result
```

**Choice rules:**

- **One offered successor: no decision exists.** The engine follows it. No
  `next` is required -- the codergen choice schema contains no `next`
  property (Section 4.5) -- and a chooser that names the lone target anyway
  (a tool's exit-code route) is validated identically.
- **Multiple offered successors: the chooser decides.** The Outcome's `next`
  names one offered target ID, produced by whoever occupies the node:

| Node type          | Chooser        | Mechanism |
|--------------------|----------------|-----------|
| `codergen`         | the agent      | `next` in the choice schema, a string enum of offered target IDs whose property description maps each ID to its route meaning (Section 4.5) |
| `tool`             | the exit code  | zero follows the normal successor, nonzero follows `on_fail` (Section 4.7) |
| `parallel`         | nobody         | all offered edges execute as branches; `next` names the fan-in (Section 4.8) |
| `start`            | nobody         | must have exactly one outgoing edge (lint `start_single_outgoing`) |
| custom             | the handler    | returns `next` by whatever means it defines (Section 4.10) |

- **Validation.** The engine verifies `next` names an allowed successor --
  an offered target, or the designated fan-in when the node is a parallel
  node (Section 3.2); a violation fails the run. For codergen nodes the
  schema'd backend makes a violation unreachable -- the adapter only returns
  objects conforming to the choice schema (Section 12.2).
- **Conditions inform the chooser, never the engine.** An edge's `condition` is the
  routing condition presented to whichever intelligence chooses at that
  node (Section 4.5), and is required on every outgoing edge of a codergen
  branch point (lint ERROR `edge_condition_missing`, Section 7.2). Engine
  routing logic never reads or compares condition text.

Routing *policy* -- "loop until the tests pass", "escalate to a human after
two attempts" -- is expressed in node prompts and graph shape, not in an
engine-evaluated language. If a decision point genuinely needs judgment over
arbitrary state, the author places a cheap LLM node there: a one-line prompt
and a low reasoning effort make the conditional itself observable and
steerable like everything else.

### 3.4 Visit Budgets

`max_visits` is the loop-bounding primitive. It is a budget on the *target*
node: how many times the walk may dispatch that node in one run.

- The engine counts every visit of every node in `node_visits`
  (checkpointed, Section 5.3). A visit is one top-level dispatch by the
  walk; the handler executions inside it -- same-process retry attempts
  (Section 3.5) and post-failure resumed executions (`retry_visit`,
  Section 5.3) -- are counted in `node_attempts`, not `node_visits`.
  `max_visits` therefore bounds graph traversal, not operator recovery
  (Section 3.7).
- An edge whose target has exhausted its budget is excluded from every offered
  set (Section 3.3). The chooser literally cannot choose it: for codergen
  nodes it is absent from the schema enum.
- If exclusion leaves a node with an empty offered set, the run fails with an
  explicit reason. Authors who want a softer landing draw an edge to an
  escalation node (an agent that pages a human, a summarizing agent) so a
  path remains when the loop budget runs out.

The budget is engine bookkeeping only: handlers are not told the visit
number. An author who wants the agent to pace itself against the budget says
so in the prompt; a looping node's session already carries its own history
under the default `compacted` fidelity (Section 5.4).

### 3.5 Retry Logic

Retries exist for one thing: **turn errors categorized as retryable**
(Appendix D) -- provider outages, rate limits, network failures. A chooser's
routing decision is never retried, and there is no retry vocabulary in the
graph language beyond the budget itself. An agent that wants another attempt
at its task routes back to a node; that is a loop, governed by `max_visits`,
not a retry.

The budget resolves as: node `max_retries`, else `defaults.max_retries`
(Section 2.7), else 0. `max_retries` counts additional attempts: `max_retries=3` means up to
4 total attempts (`max_attempts = max_retries + 1`).

```
FUNCTION execute_with_retry(node, handler, offered, graph, state, stop,
                            workdir) -> (Outcome, Error):
    FOR attempt FROM 1 TO max_attempts(node, graph):
        IF stop.is_set():
            RETURN (NONE, Error("interrupted", "stopped by operator"))
        state.node_attempts[node.id] += 1
        scope = make_scope(node, state, stop, workdir)
            -- allocates the next run-wide sequence number, creates
            -- stages/{seq}-{node_id}/, and assembles the ExecutionScope
            -- (Section 4.1) from the supplied workdir, the new stage_dir,
            -- the graph goal, and the stop signal
        outcome, error = handler.execute(node, offered, scope, graph)
            -- unexpected handler exceptions are caught by the engine and
            -- wrapped as terminal Errors
        IF error is NONE:
            write_outcome(scope.stage_dir, outcome)   -- engine-written for
                -- EVERY handler (Appendix C), then stages/latest/{node_id}
                -- is repointed at this execution's directory
            RETURN (outcome, NONE)
        write_error(scope.stage_dir, error)   -- error.json: the failed
            -- attempt's category and message, so an observer browsing
            -- stages can tell a failed attempt from one killed mid-write
        IF error.category == "retryable" AND attempt < max_attempts:
            race(sleep(backoff_delay(attempt)), stop.wait())  -- Section 3.6;
            CONTINUE       -- a stop during backoff is caught at loop top
        RETURN (NONE, error)
```

A retried attempt is a fresh execution of the same node: same offered set,
same session semantics as any revisit under the node's fidelity mode
(Section 5.4) -- and a fresh stage directory, so failed attempts' artifacts
survive alongside the one that succeeded. Attempt counts are recorded per
node in the checkpoint.

### 3.6 Backoff

Delay before retry attempt `n` (1-indexed): `200ms * 2^(n-1)`, capped at
60 seconds, multiplied by a jitter factor drawn uniformly from [0.5, 1.5].
These are implementation configuration, not pipeline fields.

### 3.7 Run Failure

A run fails -- the engine stops and emits `PipelineFailed` (Section 10) --
when:

- an Error from a node's execution survives the retry budget, or is
  `terminal` or `interrupted` outright (Appendix D) -- including a tool
  node's exit-code route being budget-exhausted (Section 4.7);
- every successor of the current node has exhausted its visit budget
  (Section 3.4);
- a chooser names an unoffered successor, or supplies none when a decision
  was required (Section 3.3);
- a tool node exits nonzero with no `on_fail` declared (Section 4.7);
- a node's `type` resolves to no registered handler
  (Section 4.2) -- an unlinted graph's defect, caught before dispatch.

A failed run writes a **failure checkpoint**: the same Checkpoint shape
(Section 5.3) with `next_node` naming the failed node itself,
`completed_nodes` untouched, and the visit counters, attempt counters, and
session bindings recording what actually happened -- including a session the
failed node opened before dying, which is exactly what makes that session
continuable after resume. The rule is uniform: the engine checkpoints after
every top-level execution, success or failure (retry attempts inside a
visit checkpoint nothing, Section 3.2). The failures that write nothing
are the pre-execution cases -- an empty offered set, an unresolvable
handler (Section 4.2), and an operator stop
caught before dispatch (Section 3.2) -- where nothing ran and the last
checkpoint already names the stuck node with the correct `retry_visit`.
Resume from a failure checkpoint retries the failed node -- as a
continuation of its already-counted visit, since every written failure
checkpoint follows a consumed execution and therefore sets `retry_visit`
(Section 5.3) -- so failure-resume cannot blow a `max_visits` budget the
run had respected. The converse is deliberate: repeated fail-and-resume
cycles re-execute the node within that one consumed visit, so a node can
*execute* more times than `max_visits` across them. `max_visits` bounds
graph traversal (Section 3.4); resumption is an explicit operator recovery
action, bounded by the operator who keeps choosing it -- exactly as
re-running a failed run is unbounded.

There is no failure *routing*: no fail edges, no retry targets, no jump
tables. A path that should survive a bad outcome is drawn in the graph, where
the chooser can take it. Recovery from a failed run is resumption from the
checkpoint (Section 5.3) or a re-run; occasional re-runs are an accepted
alternative to recovery machinery (Section 12.2, note 11).

### 3.8 Concurrency Model

The graph traversal is single-threaded. Only one node executes at a time in the top-level graph. This simplifies reasoning about engine state.

Parallelism exists within the `parallel` handler, which walks multiple
branches concurrently (Section 4.8), each in its own engine-owned git
worktree. Branches exchange nothing while in flight; their products are
worktree effects, run-log segments, and final notes, consumed at the fan-in
through concrete `BranchResult` paths (Sections 4.9, 5.5). Branch walks do
not update `last_stage`/`last_response`, which track the top-level walk
only.

### 3.9 External Steering

External steering delivers guidance to work already in progress. A running
pipeline must be operable from outside the engine process, so that an external
operator -- in particular a coding agent -- can steer it.

**Transport.** The control surface is an HTTP server exposing a single endpoint:
`POST /steer`. This specification takes no position on what the server listens
on: the reference choice is a Unix domain socket at `{logs_root}/control.sock`
(access control is inherited from run-directory file permissions), while remote
operation requires a TCP listener, in which case access control is the
implementation's burden -- this specification defines none. Whatever the
listener, the run advertises the endpoint in the run manifest (Section 5.6) as
`control_socket`: a filesystem path for a Unix domain socket, or an HTTP URL
for a TCP listener.

```
POST /steer
Content-Type: application/json

[{"type": "text", "text": "<instruction text>"}]
```

```
ContentPart:
    type : "text"
    text : String
```

The request body is an array of content parts; the surface carries steering and
nothing else -- any other control operation, if one is ever added, gets its own
endpoint and its own manifest advertisement. The only content-part type defined
by this specification is `text`. The parts array is an explicit extension point:
implementers may define additional part types to carry media files or other data
formats. The `text` part and the semantics defined here remain mandatory;
extensions must not change them. An empty array is rejected.

The response carries its meaning in the status code alone; response bodies are
empty. An empty `200` response means the server found one live target and handed
the instruction to its adapter. Steering is fire-and-forget: `200` does not
confirm native delivery or assert that the agent followed the instruction. Any
other response means the instruction was not accepted for handoff; whether
specific 4xx/5xx codes distinguish invalid input, no active task, or an
ambiguous target is the implementer's choice. Callers treat connection failure
(absent socket, connection refused) as not accepted and should apply a
client-side timeout to the exchange.

Requirements:

- A steering request targets the single active steerable task of the named live
  run. If the run is not live, or there is no unambiguous live target, the
  request fails rather than being queued for a later node.
- The adapter attempts to deliver a steering instruction only to the running
  task's live session. If the task ends before the adapter can apply it, the
  instruction is a no-op. It is never queued for later work, rewritten into a
  future prompt, or converted into an edge, outcome, or context update. If the
  current top-level execution is a parallel (fan_out) step, the target is
  ambiguous and the **engine (control server) rejects the request itself,
  before backend handoff** -- it must not delegate this to the backend's
  live-count check (Section 12.1), which sees one live turn whenever
  branch turns merely happen not to overlap and would mis-deliver the
  instruction to an arbitrary branch. An accepted request therefore
  always has exactly one active top-level execution, which is also the
  stage directory its audit record lands in.
- Success means only that the instruction was handed to the adapter.
- A failed steering request does not by itself fail the workflow.
- Each accepted steering request is recorded by the engine in the active
  execution's stage directory (`steering.jsonl`, Section 5.6) for audit. A
  rejected request has no active execution to record into; its response
  code is the record, and implementations MAY additionally note it in
  `timeline.jsonl` (Section 10).

Observation by an external operator happens through surfaces this
specification already defines: the run directory (Section 5.6) -- notably
the run log's event segments and `current.jsonl` pointer (Section 12.4)
-- and the event stream (Section 10).

The control server MAY additionally offer two Server-Sent Events
streams, so a remote operator can follow the run without filesystem
access:

- `GET /events/lifecycle` -- the engine event stream (Section 10) as it
  is emitted: stages starting and ending, retries, parallel branches,
  checkpoints. Low volume; what a dashboard or supervisor subscribes
  to.
- `GET /events/detail` -- the entries of all live run-log segments,
  merged as they are appended; each entry already carries its
  originating node id. During a fan-out this is every branch segment
  (discovered through the segment index, Section 12.4).

Both are projections of streams the run already produces;
implementations may omit them, and no new manifest advertisement is
defined -- they live on the endpoint already advertised as
`control_socket`.

**Supervision is external.** This specification defines no in-graph
supervisor handler (upstream's `stack.manager_loop`). A supervisor is an
external operator like any other: a coding agent, another pipeline's
codergen node, or a human tails the child run's `timeline.jsonl` and run-log
segments (or subscribes to the SSE streams) and posts steering messages to
the child's `control_socket`. Everything a manager loop needs is already on
these surfaces.

---

## 4. Node Handlers

### 4.1 Handler Interface

Every node handler implements a common interface. The execution engine dispatches to the handler registered for the node's `type` (Section 4.2).

```
INTERFACE Handler:
    FUNCTION execute(node, offered, scope, graph)
        -> OneOf<Outcome, Error>

    -- Parameters:
    --   node    : The parsed Node with all its fields
    --   offered : The offered successor edges (Section 3.3); the handler's
    --             chooser must pick among them when there is more than one
    --   scope   : The ExecutionScope for this single execution (below)
    --   graph   : The full parsed Graph

    -- Returns:
    --   Outcome : The result of execution (see Section 5.2), or
    --   Error   : a categorized failure (Appendix D); the engine retries
    --             retryable Errors (Section 3.5) and fails the run on the
    --             rest. Unexpected exceptions are caught by the engine and
    --             wrapped as terminal Errors.

ExecutionScope:
    workdir   : Path        -- where this execution's workspace effects go:
                            -- the run's workspace at top level, the
                            -- branch's worktree inside a parallel branch
                            -- (Section 4.8). A tool command's cwd.
    stage_dir : Path        -- this execution's engine-created artifact
                            -- directory, stages/{seq}-{node_id}/
                            -- (Section 5.6); handlers write their
                            -- specialized files here, the engine writes
                            -- outcome.json here (Appendix C)
    goal      : String      -- the top-level goal field; the source
                            -- for $goal expansion (Section 4.5)
    stop      : StopSignal  -- the run's operator-stop signal (below)

StopSignal:
    FUNCTION is_set() -> Bool    -- has the engine requested a stop?
    FUNCTION wait()              -- blocks until set; for handlers that
                                 -- block on something other than a
                                 -- backend turn (subprocesses, custom
                                 -- handlers)
```

`StopSignal` is one-shot and engine-set: it is set once, when an operator
stops the run (Section 3.1), and never cleared. An in-flight `execute()`
MUST return `Error(interrupted)` promptly once the signal is set --
codergen handlers get this behavior for free because the engine also calls
the backend's `interrupt_all()`, which ends the live turn (Section 12.1);
handlers that block elsewhere observe the signal themselves via `is_set()`
polling or a race against `wait()`. The engine closes the remaining gaps
itself: it checks the signal before each dispatch and each retry attempt
and races backoff sleeps against it (Sections 3.2, 3.5), so a stop that
lands between turns -- including after `interrupt_all()` has already run
-- still ends the run without another node executing. The reference stop
surface is a process signal (SIGINT/SIGTERM) delivered to the engine
process; a remote stop endpoint is deliberately out of scope for this
version (the control socket carries steering and nothing else,
Section 3.9).

### 4.2 Handler Registry

The handler registry maps type strings to handler instances. Resolution
is a single lookup: every node names its `type` (Section 2.4), and an
unknown value is an error, never a fallthrough.

```
HandlerRegistry:
    handlers : Map<String, Handler>   -- type string -> handler instance

    FUNCTION register(type_string, handler):
        handlers[type_string] = handler
        -- Registering for an already-registered type replaces the previous handler

    FUNCTION resolve(node) -> OneOf<Handler, Error>:
        -- A typo ("my_custom_typ") or an unregistered custom type must
        -- not silently execute different work -- caught statically by
        -- lint ERROR type_known (Section 7.2), and terminally here if
        -- an unlinted graph reaches execution
        IF node.type IN handlers:
            RETURN handlers[node.type]
        RETURN Error("terminal", "unknown handler type: " + node.type)
```

### 4.3 Start Handler

A no-op handler for the pipeline entry point.

```
StartHandler:
    FUNCTION execute(node, offered, scope, graph):
        RETURN Outcome(notes="run started")
```

Every graph must have exactly one node with `type="start"`, and it must have exactly one outgoing edge -- the start node contains no chooser. The lint rules enforce both.

### 4.4 Exit Handler

A no-op handler for the pipeline exit point. Reaching it completes the run
(Section 3.2); the handler itself never executes.

Every graph must have exactly one node with `type="exit"`.

### 4.5 Codergen Handler (LLM Task)

The codergen handler is the default for all nodes that invoke an LLM. It reads the node's prompt, expands template variables, constructs the choice schema from the offered successors, calls the LLM backend (see Section 1.4 for backend options), writes the prompt and response to the logs directory, and returns the outcome.

```
CodergenHandler:
    backend : CodergenBackend | None
        -- The LLM execution backend. Any implementation of the
        -- CodergenBackend interface (Section 4.5). None = simulation mode.

    FUNCTION execute(node, offered, scope, graph)
        -> OneOf<Outcome, Error>:
        -- 1. Build prompt
        prompt = node.prompt
        IF prompt is empty:
            prompt = node.label
        prompt = expand_variables(prompt, scope.goal)

        -- 2. Write prompt to logs
        write_file(scope.stage_dir + "prompt.md", prompt)

        -- 3. Prepare a fully resolved turn
        turn = CodergenTurn(
            node_id=node.id,
            parts=[ContentPart(type="text", text=prompt)],
            output_schema=choice_schema(offered, graph),
            model=resolve_model(node, graph),
            provider=resolve_provider(node, graph),
            reasoning_effort=resolve_reasoning_effort(node, graph),
            fidelity=resolve_fidelity(node, graph),
            thread_key=resolve_thread_key(node, graph),
            workdir=scope.workdir,
            timeout=node.timeout
        )

        -- 4. Call LLM backend
        IF backend is not NONE:
            result = backend.run(turn)
            IF result is an Error:
                RETURN result       -- engine applies Section 3.5
            outcome = result
        ELSE:
            outcome = Outcome(
                next=(first offered target IF size(offered) > 1 ELSE unset),
                notes="[Simulated] Stage completed: " + node.id
            )

        -- 5. Write response; the engine writes outcome.json (Section 3.5)
        write_file(scope.stage_dir + "response.md",
                   yaml_frontmatter(outcome, except="notes") + outcome.notes)
        RETURN outcome
```

**Choice schema:** The output schema the backend enforces on the turn. Its
shape follows directly from the offered successors (Section 3.3). This is
the concrete, mandatory encoding -- adapters translate exactly this schema
(Section 12.2), and route meanings reach the model through the `next`
property's `description`, since JSON Schema enum values cannot carry
per-value descriptions:

```
FUNCTION choice_schema(offered, graph) -> JSON Schema:
    IF size(offered) <= 1:
        RETURN {
          "type": "object",
          "properties": {
            "notes": { "type": "string",
                       "description": "Your account of this stage." }
          },
          "required": ["notes"],
          "additionalProperties": false
        }
    RETURN {
      "type": "object",
      "properties": {
        "next": {
          "type": "string",
          "enum": [e.to FOR e IN offered],
          "description": describe_routes(offered, graph)
        },
        "notes": { "type": "string",
                   "description": "Your account of this stage." }
      },
      "required": ["next", "notes"],
      "additionalProperties": false
    }

FUNCTION describe_routes(offered, graph) -> String:
    -- "Choose the next stage." followed by one clause per offered edge,
    -- condition first, target ID as the answer key:
    --   "Choose the next stage. Tests failed -- fix and retry: implement;
    --    Tests passed: review"
    -- The condition is the edge's `condition` field. Lint guarantees it
    -- exists wherever this function runs: every outgoing edge of a
    -- codergen node with more than one outgoing edge must carry one
    -- (lint ERROR `edge_condition_missing`, Section 7.2). If an unlinted
    -- graph reaches here anyway, fall back to the target's label, then
    -- the target's ID.
```

**Variable expansion:** The only built-in template variable is `$goal`, which resolves to the top-level `goal` field. Variable expansion is simple string replacement, not a templating engine.

**Outcome file:** The engine -- not the handler -- writes `outcome.json` in the stage directory for every successful execution of every handler type (Section 3.5). It is an audit record for observers (Appendix C); the engine never reads it back.

**Response file:** The handler writes `response.md` with the Outcome fields
other than `notes` as YAML frontmatter and `notes` as the markdown body.

#### CodergenBackend Interface

```
CodergenTurn:
    node_id          : String
        -- correlation and run-log identity only; does not expose the graph
    parts            : List<ContentPart>
        -- non-empty ordered user-message content (Section 3.9)
    output_schema    : JSON Schema
        -- choice schema constructed by CodergenHandler (Section 4.5);
        -- graph-specific constraints are opaque to the backend
    model            : String
    provider         : String
    reasoning_effort : String
    fidelity         : FidelityMode
    thread_key       : String
        -- resolved logical reuse key for full/compacted; empty for none
    workdir          : Path
        -- the execution's working directory (ExecutionScope.workdir,
        -- Section 4.1): the run workspace, or a branch worktree during a
        -- parallel fan-out
    timeout          : Duration | unset

INTERFACE CodergenBackend:
    FUNCTION run(turn: CodergenTurn) -> OneOf<Outcome, Error>
    FUNCTION steer(parts: List<ContentPart>)
        -> "accepted" | "not_active" | "ambiguous_target"
    FUNCTION interrupt_all()
    FUNCTION bindings() -> Map<String, ThreadBinding>
```

The handler resolves all graph-authored values before calling the backend. The
backend receives no Node, Edge, Graph, or Context and does not construct or
interpret routing policy. It knows that the schema represents a successor
choice with notes but does not derive or interpret its graph-specific
constraints -- the enum's contents are opaque application data. A successful
harness-backed implementation converts the adapter's conforming object into the
native `Outcome` value represented by that schema.

`Outcome` is a semantic runtime value, not a JSON return contract.
Implementations SHOULD represent it using the language's natural first-class
form, such as a struct, class, record, or tuple. `Error` is categorized as
`retryable`, `terminal`, or `interrupted` (Appendix D). The handler returns a
backend Error to the engine unchanged; the engine retries retryable Errors and
fails the run on the rest (Sections 3.5, 3.7). Unexpected implementation
exceptions remain out of band and are caught as terminal Errors.

The recommended implementation is a harness-backed backend -- a thin
wrapper and routing layer over third-party coding agent harness
software; Section 12 is its full specification.

### 4.6 Human Interaction: A Pattern, Not a Node Type

There is no human-gate handler. Human participation is an authoring
pattern built from the primitives that already exist, chosen by how much
judgment versus determinism the decision needs:

- **Agent-conducted interview (judgment, N-way).** A codergen node whose
  prompt says to contact the person -- through whatever tools its harness
  has (a Slack MCP server, email, a CLI notifier) -- wait for their
  answer, and route on it through the ordinary choice schema
  (Section 4.5). The agent owns the waiting and the interpretation, and
  the whole wait happens *inside one backend turn* -- polling with
  exponential backoff via its shell, not engine turns or retry
  attempts. How long a harness can actually hold one turn open --
  process lifetime, inference cost of the polling loop, tool-session
  authentication -- is an implementation limit the backend contracts do
  not guarantee and authors must validate against their harness; where
  those limits bind, the deterministic block below or a custom durable
  gate is the robust choice. The exchange lands in the run log like any
  other turn. This is a *soft* gate: the schema constrains which target
  the agent names, not whether it reported the human faithfully.
- **Deterministic block (mechanical, two-way).** A `tool` node running a
  program that sends the message, blocks until a response arrives, and
  exits 0 or nonzero (Section 4.7). No model in the loop, exit-code
  routing -- deterministic in route selection, though not in external
  delivery (below).
- **Anything else.** A custom handler (Section 4.10) for implementations
  that want a bespoke gate -- a durable inbox, an authenticated answer
  channel, a delivery guarantee.

Two properties both patterns inherit from ordinary execution semantics,
to be stated rather than discovered:

- **The node `timeout`, if set, must cover the complete human wait** --
  it bounds the whole backend turn (or tool command), and expiry
  interrupts the attempt and fails the run (Sections 2.5, 3.7). Leave
  it unset or generous.
- **Contact is not transactional.** The engine guarantees neither
  delivery nor deduplication. An execution may be retried, crash-replayed,
  or resumed (Sections 3.5, 5.3), so an externally visible contact
  attempt may happen zero times (the agent failed before sending, or
  never sent), once, or several times -- and the engine supplies no
  cross-execution decision identity to key on: a re-execution of the
  same decision and a loop legitimately revisiting the node for a *new*
  decision look identical in `ExecutionScope`. No generic deduplication
  recipe exists within that scope. Authors whose workflow carries an
  external stable identity (a ticket number, a PR URL, a thread the
  channel itself threads) can correlate channel-specifically; that is
  mitigation, not a guarantee, and it is the author's. As evidence, the
  contact channel is the only record that spans every replay path;
  a reused session's memory (Section 5.4) and a workspace marker
  (Section 5.5) hold only at the top level where the workdir is stable,
  and nothing survives into a replayed fan-out's fresh worktrees and
  rebound sessions (Section 4.8). The run log is evidence, not a
  delivery ledger.

The external operator is the other half of the story: a supervisor --
human at a frontend, or a coding agent -- watches the event stream and
steers the run (Sections 3.9, 10). Section 6 develops the pattern;
upstream's `wait.human` node, Interviewer protocol, and Question/Answer
models are intentionally omitted.

### 4.7 Tool Handler

Executes an external shell command and routes on its exit code. This is the
one mechanical chooser: the only branching signal a command produces is its
exit status, so that is the only branching the tool node offers.

```
ToolHandler:
    FUNCTION execute(node, offered, scope, graph)
        -> OneOf<Outcome, Error>:
        command = node.tool_command
        IF command is empty:
            RETURN Error("terminal", "no tool_command on " + node.id)

        result = run_shell_command(command, cwd=scope.workdir,
                                   timeout=node.timeout, stop=scope.stop)
            -- the command runs in the execution's workdir; if the stop
            -- signal is set while it runs, the child process is stopped
            -- and an interrupted Error is returned (Section 4.1)
        write stdout and stderr to scope.stage_dir (tool.log)

        IF result.exit_code == 0:
            route = success_target(node)
                -- the outgoing-edge target that on_fail does not name;
                -- when on_fail names the only target, that target
        ELSE IF node.on_fail is set:
            route = node.on_fail
        ELSE:
            RETURN Error("terminal",
                         command + " exited " + result.exit_code)

        IF route NOT IN [e.to FOR e IN offered]:
            RETURN Error("terminal",
                         "exit-code route " + route +
                         " has exhausted its visit budget")
        RETURN Outcome(next=route,
                       notes="exit " + result.exit_code + ": " +
                             tail(result.stdout or result.stderr))
```

**Routing rules** (enforced by lint `tool_routing`):

- A tool node has one or two outgoing edges.
- `on_fail`, when set, MUST name an outgoing-edge target.
- With two edges, `on_fail` is required; the other target is the success
  route. Exit code 0 follows the success route; nonzero follows `on_fail`.
- With one edge, `on_fail` is normally absent and a nonzero exit is a
  terminal Error -- the command was an assertion. (`on_fail` naming the
  single target is permitted and makes the command advisory.)
- The exit code fully designates the route. A mechanical chooser cannot pick
  an alternative, so if the designated target has exhausted its visit budget
  (Section 3.4) the handler returns a terminal Error naming the budget.

A command that exceeds `timeout` is stopped and returns an interrupted Error
(Appendix D). Tool nodes never invoke an LLM and have no session; a
deterministic check costs zero tokens. Anything richer than exit-code
branching -- judging output text, weighing multiple signals -- is a job for a
codergen node, which has a shell of its own.

### 4.8 Parallel Handler

Fans out execution to multiple branches concurrently, waits for all of them
to converge on the designated fan-in node, then hands control to the fan-in.

```
ParallelHandler:
    FUNCTION execute(node, offered, scope, graph)
        -> OneOf<Outcome, Error>:
        -- 1. Every offered edge is a branch root. Offered, not
        --    outgoing: a branch root at its max_visits is excluded and
        --    the fan-out SHRINKS -- normative, and usually not what an
        --    author wants (lint WARNING branch_root_max_visits;
        --    bound loops at the parallel node instead)
        branches = offered
        fan_in = designated_fan_in(node, graph)
            -- the parallel.fan_in node on which all branch paths converge;
            -- existence and convergence are lint-enforced (Section 7.2)
        max_parallel = node.max_parallel        -- default 4 (Section 2.5)

        -- 2. Freeze one parent state, give every branch its own worktree
        snapshot = freeze_workspace(scope.workdir)
            -- one frozen parent state: HEAD, plus tracked modifications,
            -- plus untracked-but-not-ignored files, captured without
            -- mutating the user's branch or index (e.g. a temporary-index
            -- commit-tree). Ignored files are never captured. A workdir
            -- that is not a git repository is a terminal Error: the
            -- parallel handler requires a git workspace.
        results = []
        FOR EACH branch IN branches (up to max_parallel at a time):
            wt = create_worktree(snapshot, branch.to)
                -- engine-invoked git; an isolated worktree per branch,
                -- every branch starting from the same frozen state
            walk = walk_branch(branch.to, until=fan_in,
                               workdir=wt, stop=scope.stop)
                -- the Section 3.2 loop over the branch's nodes MINUS its
                -- bookkeeping: no checkpoint saves, no last_stage /
                -- last_response updates. Shares node_visits and the
                -- stage-dir sequence (allocated atomically, Section 3.5);
                -- every branch execution's scope carries workdir=wt and
                -- the run's stop signal. Stops when the walk reaches
                -- fan_in
            results.append(BranchResult(
                branch_id=branch.to,     -- the branch root's ID
                outcome_or_error=walk.result,
                notes=walk.final_notes,
                path=walk.node_ids,
                workdir=wt,
                stage_dirs=walk.stage_dirs,
                segments=walk.segment_paths))
                -- segment attribution: the engine notes the position of
                -- events/index.jsonl at fan-out start and credits a
                -- branch with the later index entries whose node_id lies
                -- on its walked path -- earlier fan-outs' segments for
                -- the same node IDs are before the mark and excluded

        -- 3. Write the branch evidence FIRST -- success or failure
        --    (Section 4.9); BranchResult carries outcome_or_error
        --    precisely so a failed fan-out's table exists for the
        --    observer and the resumed run's operator
        write_json(scope.stage_dir + "branches.json", results)

        -- 4. All branches must converge (wait_all)
        FOR EACH r IN results:
            IF r.outcome_or_error is an Error:
                RETURN r.outcome_or_error   -- a dead branch fails the run
                -- (3.7). walk_branch wraps EVERY branch failure as a
                -- terminal Error carrying the failure reason -- a
                -- branch-level run-failure (exhausted successors,
                -- unoffered choice) and equally a turn Error that
                -- survived its own node's retry budget -- EXCEPT that
                -- interrupted stays interrupted (Appendix D). A parallel
                -- execution therefore never returns a retryable Error:
                -- retries exist only inside branches, and the top-level
                -- retry loop (3.5) never re-runs a whole fan-out
        RETURN Outcome(next=fan_in.id,
                       notes=size(branches) + " branches converged")
```

Branches exchange nothing while in flight (Section 3.8): each works in its
own worktree, so concurrent agents never see each other's partial edits, and
each branch's product is a genuinely independent candidate. Worktrees are
engine-owned, created fresh for each execution of the parallel node at an
implementation-chosen location recorded in `branches.json`, and MUST
survive at least until the fan-in node completes. At creation the engine
also appends `{path, branch_id, ts}` to `{logs_root}/worktrees.jsonl` --
the durable inventory Finalize sweeps for cleanup, which unlike
`branches.json` (written only when every branch walk returns) also names
the worktrees of a fan-out that crashed or died before its walks
returned. Removal happens only in
a **completed** run's Finalize (Section 3.1); a failed run leaves all its
worktrees on disk, conservatively. The distinction: a failure at or after
the fan-in resumes at the fan-in, which needs the already-converged
candidates it is asked to evaluate (Section 4.9); a failure of the
parallel node itself resumes at the parallel node, which replays the
fan-out in fresh worktrees -- its old worktrees are retained as evidence
only, never re-entered.

`branches.json` is the fan-in's evidence, written to the parallel node's own
stage directory: one `BranchResult` per branch -- branch root ID, final
outcome or error, notes, the node path walked, the branch's **worktree
path**, its per-attempt stage directories, and its run-log segment paths.
Being a run-directory file, it survives a crash for free and doubles as an
observer surface (Section 5.6).

**The parallel step is atomic with respect to checkpointing -- and to
counters.** No checkpoint is written between fan-out and convergence; a
crash mid-fan-out resumes from the pre-parallel checkpoint and re-runs all
branches -- an accepted re-run (Section 12.2, note 11). The engine
snapshots the visit and attempt counters when it dispatches a parallel
node (immediately after the parallel node's own visit increment); if the
parallel node's execution returns an Error, the engine restores that
snapshot before writing the failure checkpoint -- except the parallel
node's **own** visit and attempt counters, which keep their true values
(they record reality; only the branch deltas that resume will redo are
rolled back). Resume then re-runs the whole step with the same budget
arithmetic a crash-resume gets -- branch work that will be redone is not
double-counted against `max_visits`. Stated as one policy: **a failed
parallel attempt is discarded as a unit.** Its branch counter deltas are
rolled back; its branch thread bindings may linger in `sessions` but are
stale by workdir and replaced with fresh sessions when the replayed
fan-out's new worktrees arrive (Section 12.1); its worktrees and stage
directories remain on disk as evidence (`worktrees.jsonl`,
`branches.json`) but carry no engine state forward. Branch progress is
never partially resumed.

**Branches are structurally independent.** A **branch node-set** is the set
of nodes reachable from a branch root along paths that do not pass through
the designated fan-in; this is the definition every branch-scoped rule
quantifies over (lints, thread boundaries, segment attribution). Branch
node-sets must be pairwise disjoint except for the shared fan-in, a
parallel node may not appear inside a branch, and every incoming edge of a
branch-interior node must originate inside the same parallel's branches
(branch roots additionally accept their parallel node) -- otherwise the
top-level walk could stroll into branch territory, reach the fan-in with
no fan-out having run, and rebind branch threads across the worktree
boundary (lints `branch_disjoint`, `no_nested_parallel`, `branch_entry`,
Section 7.2). Shared intermediate nodes would race on visit budgets and
double-execute; nested fan-out is out of scope for this version.

Upstream defines a `join_policy` attribute with a `first_success` mode.
It is intentionally omitted: it requires a per-branch success vocabulary this
specification does not have. Racing alternatives and picking a winner is the
fan-in agent's judgment call over converged branches.

With a harness-backed backend (Section 12): branches MUST NOT resolve to
the same thread key concurrently -- statically checkable because thread
resolution is static (lint ERROR `parallel_thread_disjoint`, Section 7.2)
-- and each branch turn writes its own run-log segment, discovered through
the segment index (Section 12.4).

### 4.9 Fan-In Handler

Consolidates the results of the converged branches. The fan-in is a codergen
node with the branch evidence appended to its prompt: an agent that reads
what the branches produced and chooses what follows.

```
FanInHandler:
    FUNCTION execute(node, offered, scope, graph)
        -> OneOf<Outcome, Error>:
        par = parallel_node_of(node, graph)
            -- the parallel node that designates this fan-in; exactly one
            -- exists (lint `fan_in_single_parallel`, Section 7.2)
        results = read_json(stages_latest(scope, par.id) + "branches.json")
            -- the BranchResults written by that parallel node's most
            -- recent execution (Section 4.8), located through the
            -- stages/latest/{node_id} pointer (Section 5.6).
            -- stages_latest resolves against the handler's own stage
            -- directory: parent(scope.stage_dir) + "/latest/" + node_id
            -- -- every stage dir is a sibling under stages/, so no
            -- logs_root is needed

        prompt = node.prompt
        IF prompt is empty:
            prompt = "Evaluate the results of the parallel branches."
        prompt = prompt + "\n\n" + render(results)
            -- per branch: its ID, its notes, and its worktree path

        -- continue exactly as CodergenHandler (Section 4.5) with this
        -- prompt: choice schema from offered, backend turn, response file.
        -- The fan-in runs in the run's main workspace (scope.workdir);
        -- the branch worktrees are still on disk at the paths the render
        -- names.
```

The rendered results orient the agent; its real evaluation happens through
its own tools -- reading and diffing the branch worktrees at their
`BranchResult` paths, running checks, and applying the chosen candidate (or
a synthesis) to the main workspace. Like any codergen node, the fan-in
routes by choosing among its own offered successors: proceed with the
merged result, loop back for another round, or escalate to a node that
brings in a human (Section 6).

### 4.10 Custom Handlers

New handler types are added by implementing the Handler interface and registering with the registry:

```
-- Define a custom handler
MyCustomHandler:
    FUNCTION execute(node, offered, scope, graph)
        -> OneOf<Outcome, Error>:
        -- Custom logic here
        RETURN Outcome(next=..., notes="...")

-- Register it
registry.register("my_custom_type", MyCustomHandler())

-- Reference in the pipeline JSON (Section 2.5: custom-type nodes carry
-- their configuration in the `custom` object)
{ "id": "my_node", "type": "my_custom_type",
  "custom": { "custom_attr": "value" },
  "edges": [{ "to": "next_stage" }] }
```

**Handler contract:**
- Handlers MUST be stateless or protect shared mutable state with synchronization.
- A handler whose node has more than one offered successor MUST supply `next` naming one of them (Section 3.3).
- Handler panics/exceptions MUST be caught by the engine and wrapped as terminal Errors (Section 3.5).
- A blocked or long-running `execute()` MUST return `Error(interrupted)` promptly once `scope.stop` is set (Section 4.1).
- Workspace effects go in `scope.workdir`; specialized artifacts go in `scope.stage_dir`. The engine writes `outcome.json`; handlers do not (Appendix C).
- Handlers SHOULD NOT embed provider-specific logic; LLM orchestration is delegated to the configured backend (Section 1.4).

---

## 5. State

### 5.1 Engine State

The engine's execution state is typed and engine-owned. There is no generic
key-value context: handlers receive an `ExecutionScope` (Section 4.1), not a
state bag, and nothing in the graph language reads engine state (see
Section 5.5 for how data actually moves between nodes).

```
EngineState:
    completed_nodes : List<String>          -- ordered audit trail (3.2)
    node_visits     : Map<String, Integer>  -- top-level dispatches per node (3.4)
    node_attempts   : Map<String, Integer>  -- retry attempts per node (3.5)
    last_stage      : String                -- last completed top-level stage
    last_response   : String                -- its truncated notes
    seq             : Integer               -- run-wide stage-dir sequence
                                            -- (Section 3.5); allocation is
                                            -- atomic across branch walks;
                                            -- checkpointed (Section 5.3)
```

`last_stage` and `last_response` exist because they cost nothing to maintain
and give observers a one-glance answer to "where is the run and what just
happened" -- they are checkpointed (Section 5.3) for exactly that reader.
They track the top-level walk only (Section 3.8); branch walks update
`node_visits`, `node_attempts`, and `seq` (all shared, synchronized) and
nothing else.

### 5.2 Outcome

The outcome is the result of a node's execution: the chooser's routing
decision and its account of the stage.

```
Outcome:
    next  : NodeId | unset   -- the chosen successor; required when the node
                             -- had more than one offered successor,
                             -- optional otherwise (Section 3.3)
    notes : String           -- the chooser's human-readable account of the
                             -- stage; the body of response.md and the
                             -- observer's primary narrative
```

There is no status field. A node's execution either produces an Outcome or a
categorized Error (Appendix D) -- failure is an error channel, not an agent
verdict. "How well the stage went" is expressed by where the chooser routed
and what its notes say; the run-level verdict is `RunResult`
(Section 3.2).

Upstream defines a five-value `StageStatus` (`SUCCESS`, `FAIL`,
`PARTIAL_SUCCESS`, `RETRY`, `SKIPPED`), plus `preferred_label`,
`suggested_next_ids`, and `context_updates`. All are intentionally omitted:
they exist to feed an engine-side routing evaluator this specification does
not have.

### 5.3 Checkpoint

A serializable snapshot of execution state, saved after every top-level
execution -- success or failure (branch nodes checkpoint nothing,
Section 4.8). Enables crash recovery and resume.

```
Checkpoint:
    timestamp       : Timestamp              -- when this checkpoint was created
    current_node    : String                  -- ID of the last executed node
    next_node       : String                  -- where resume continues: the
                                              -- resolved successor after a
                                              -- success, the failed node
                                              -- itself after a failure
                                              -- (Sections 3.2, 3.7)
    completed_nodes : List<String>            -- IDs of all completed nodes in order
    node_visits     : Map<String, Integer>    -- visit counters per node (Section 3.4)
    node_attempts   : Map<String, Integer>    -- retry-attempt counters per node (Section 3.5)
    seq             : Integer                 -- the stage-dir sequence counter
                                              -- (Sections 3.5, 5.1); restored on
                                              -- resume so a resumed run never
                                              -- reuses an existing stages/ name
    retry_visit     : Bool                    -- true on every failure checkpoint
                                              -- (a written one always follows a
                                              -- consumed execution): resume then
                                              -- CONTINUES that visit instead of
                                              -- counting a new one (below).
                                              -- False on initial, success, and
                                              -- final checkpoints
    last_stage      : String                  -- observer convenience (Section 5.1)
    last_response   : String                  -- observer convenience (Section 5.1)
    sessions        : Map<String, ThreadBinding>  -- thread key -> harness,
                                                  -- session ID, and workdir
                                                  -- (Section 12.1)
```

Serialized as JSON to `{logs_root}/checkpoint.json`, replaced atomically on
each save.

`next_node` exists because routing decisions live only in the chooser's
in-memory Outcome (the engine never reads `outcome.json` back, Appendix C).
A success checkpoint is written after the choice is resolved (Section 3.2),
so the walk's position survives a crash. A **failure checkpoint**
(Section 3.7) names the failed node as both `current_node` and `next_node`
-- the node the walk was on is the node resume retries -- and leaves
`completed_nodes` untouched; everything else -- counters, `sessions` --
records what actually happened (with the parallel counter-rollback
exception, Section 4.8), so a session the dying node opened is durably
bound. Every written failure checkpoint follows a consumed execution (the
pre-execution failures write nothing, Section 3.7), so it sets
`retry_visit`: the resumed execution is a **continuation of that consumed
visit**, exactly as a same-process retry attempt is (Section 3.5), and
the engine skips the visit increment for the first execution after such
a resume. Without this rule a `max_visits=1` node
that failed would be either unresumable (budget check blocks it) or able
to execute twice (no check); with it, failure-resume and in-process retry
have identical budget arithmetic. The **initial checkpoint** written at Initialize (Section 3.1) has
an empty `current_node`, the start node as `next_node`, zeroed counters,
and empty collections -- resume before any execution simply starts the
walk. The final checkpoint written at Finalize records the terminal node
as `current_node` with an empty `next_node`.

**Resume behavior:**

1. Load the checkpoint from `{logs_root}/checkpoint.json`.
2. Restore `last_stage`/`last_response` and the visit and attempt
   counters. Recover the stage-dir sequence counter as
   `seq = max(checkpoint.seq, highest sequence present under stages/)`,
   where `seq` always denotes the **last allocated** number (the next
   allocation is `recovered + 1`) and the scan ignores `stages/latest/`.
   The scan matters because stage directories are created *before*
   execution while checkpoints land after, so a crash mid-fan-out leaves
   completed branch directories the pre-parallel checkpoint has never
   heard of. This is the exact analogue of the backend's `events/`
   recovery (Section 12.1); with it, existing `stages/` directories are
   never reused. `completed_nodes` is an ordered audit trail, not a skip
   list -- loops legally revisit nodes.
3. Continue execution at `next_node`. After a success checkpoint that is
   the already-resolved successor -- the routing question was answered
   before the save; resume never re-derives or re-asks a choice. After a
   failure checkpoint it is the failed node, retried -- with the visit
   increment skipped once when `retry_visit` is set. If the crash happened
   mid-execution of `next_node`, its work is simply re-run -- the only
   re-run resume ever pays, and an accepted one (Section 12.2, note 11).
4. Restore the sessions map to the backend. Session persistence is the
   CodergenBackend's responsibility. First-party harnesses give you this free.

The engine snapshots the backend's thread bindings (`bindings()`, Section
12.1) into the `sessions` map at every checkpoint save. Bindings enter the
backend's table at session open, so an interrupted node's session is
checkpointed alongside its partial work. On resume the backend is
constructed with the restored map; the in-memory `live` list starts
empty -- a dead process has no live turns.

### 5.4 Context Fidelity

Context fidelity controls how much prior conversation and state is carried into the next node's LLM session. This is a core mechanism for managing context window usage across multi-stage pipelines.

```
FidelityMode ::= 'full'
               | 'compacted'
               | 'none'
```

| Mode        | Session              | Context Carried |
|-------------|----------------------|-----------------|
| `full`      | Reused (same thread) | Full conversation history; the harness compacts natively as needed when the context window fills |
| `compacted` | Reused (same thread) | Same history, compacted via the harness's native compaction before each revisit turn |
| `none`      | Fresh                | Nothing beyond the rendered prompt |

The meaning of compaction is delegated entirely to the harness (its native
`/compact` behavior); the engine produces no summary artifacts of its own.
Compact-then-prompt is one serialized operation: if compaction fails, the
revisit prompt is not sent (Section 12.1).
The upstream specification defines additional summary-carrying modes
(`truncate`, `compact`, `summary:low/medium/high`) that synthesize carryover
text into a fresh session. Those are extra work above and beyond the three
base modes, and how such summaries would be extracted from a harness session
is currently under-defined -- implementations MAY add them as needed; this
specification does not define them. A fidelity value the active
implementation does not support is a validation ERROR (`fidelity_valid`,
Section 7.2) and, if an unlinted graph reaches execution anyway, a terminal
Error from the backend (Section 12.1) -- an unrecognized mode never falls
through to accidental fresh-session semantics.

**Fidelity resolution precedence (highest to lowest):**

1. Target node `fidelity` field
2. `defaults.fidelity` (Section 2.7)
3. Default when unset: `compacted`

**Thread resolution (for session-reusing modes):**

When fidelity resolves to `full` or `compacted`, the engine determines a thread key for session reuse:

1. Target node `thread_id` -- explicit on the node; `thread_id` is not
   defaultable (Section 2.7)
2. Otherwise: the node's own ID

That is the whole rule. By default every node owns its own thread: revisits
of a node continue that node's session -- which is what makes the default
`compacted` fidelity matter for loops -- and distinct nodes (including
parallel branch roots) get distinct keys automatically. Sharing one
conversation across several nodes is opt-in via an explicit `thread_id`
named on each sharing node (Section 2.7). Resolution is fully
static: a node's thread key never depends on the path that reached it.
Explicit `thread_id` values must not collide with node IDs (lint
`thread_id_collision`, Section 7.2), so the implicit per-node threads and
the named shared threads occupy cleanly separate namespaces.

Unlike upstream, incoming edges do not override session policy. Authors needing
path-specific sessions should use distinct nodes.

Nodes that share the same thread key reuse the same LLM session. Nodes with different thread keys start fresh sessions.

### 5.5 Data Between Nodes

There is no engine-mediated application data plane -- no artifact store, no
context updates, no values threaded through outcomes. Data moves between
nodes on three surfaces that already exist:

1. **The workspace.** Top-level nodes share the run's working directory. An
   agent that writes code, documents, or intermediate results to the repo
   has published them to every later node. This is the primary channel, and
   it is durable, diffable, and versionable for free. (Parallel branches
   are the deliberate exception: each works in its own worktree, and its
   product reaches later nodes through the fan-in's `BranchResult` paths,
   Sections 4.8-4.9.)
2. **The session.** Nodes sharing a thread under `full` or `compacted`
   fidelity share conversation history (Section 5.4); a later node remembers
   what an earlier turn saw and did without any explicit hand-off.
3. **The run directory.** Prompts, responses, outcomes, and run-log segments
   (Sections 5.6, 12.4) are readable by any node whose agent cares to look,
   and by every external observer.

Upstream defines an in-engine artifact store with file-backing thresholds;
it is intentionally omitted -- nothing reads from it that the workspace does
not serve better.

### 5.6 Run Directory Structure

Each pipeline execution produces a directory tree for logging, checkpoints, and artifacts:

```
{logs_root}/
    checkpoint.json              -- Serialized checkpoint after each top-level execution (Section 5.3)
    manifest.json                -- Pipeline metadata (name, goal, start time) and control-surface advertisement (Section 3.9)
    timeline.jsonl               -- Engine event stream as JSONL (Section 10)
    worktrees.jsonl              -- Append-only worktree inventory, one line per branch worktree created; Finalize's cleanup sweep (Section 4.8)
    events/
        index.jsonl              -- Append-only segment index: one line per segment created (Section 12.4)
        {seq}-{node_id}.jsonl    -- Run-log segment per backend turn (Section 12.4)
    current.jsonl                -- Symlink to the in-progress segment; best-effort pointer to events/index.jsonl while more than one turn is live (Section 12.4)
    stages/
        {seq}-{node_id}/         -- One directory per handler execution, engine-created (Section 3.5); seq is run-wide, monotonic, checkpointed (Section 5.3), and zero-padded to six digits so directory listings sort in execution order. A loop revisit, retry, or resumed run never overwrites an earlier execution
            prompt.md            -- Rendered prompt sent to LLM
            response.md          -- LLM response text
            outcome.json         -- The execution's Outcome, engine-written on success (Appendix C)
            error.json           -- The attempt's Error category and message, engine-written on failure (Section 3.5)
            steering.jsonl       -- Steering audit records (Section 3.9)
            tool.log             -- Tool nodes: captured stdout/stderr (Section 4.7)
            branches.json        -- Parallel nodes: BranchResult evidence for the fan-in (Section 4.8)
        latest/
            {node_id}            -- Symlink to the node's most recent {seq}-{node_id}/, repointed by the engine after each successful execution (Section 3.5)
```

---

## 6. Human-in-the-Loop (Authoring Pattern)

Attractor has no human-interaction protocol. There is no gate node type,
no Interviewer interface, no Question/Answer model, and no frontend
contract to implement. Reaching a human is not solved at design time; it
is solved at authoring time and at operating time, by intelligences that
already have their own channels.

### 6.1 The Two Halves

**In the graph (authoring time).** A decision that needs a human is a
node like any other, built from existing primitives -- Section 4.6 gives
the ladder: a codergen node that conducts the interview with its own
tools and routes through the choice schema; a `tool` node running a
program that blocks deterministically and routes on its exit code; a
custom handler for anything bespoke. The node's edges carry the
decision's conditions (Sections 2.6, 4.5), exactly as at any other
branch point -- "Approved", "Fixes requested" -- so the same graph
reads the same way whether an agent, a program, or a person answers.

### 6.2 The Operator

**Outside the run (operating time).** The pipeline is built to be
operated: the event stream narrates it (`timeline.jsonl`, Section 10),
the run directory exposes every stage's evidence (Section 5.6), and the
steering endpoint accepts guidance into the live turn (Section 3.9). A
supervisor -- a person at a TUI, a Slack bot, or the coding agent that
north-star operation contemplates -- watches events and intervenes by
steering. Frontends are external observers with a POST verb, not
protocol implementations.

### 6.3 What Upstream Has That This Does Not

Upstream defines a `wait.human` node backed by an Interviewer protocol:
`ask(question) -> Answer`, Question/Option/Answer models, console and
callback implementations, accelerator-key parsing, timeout-with-default
selection, and (in any concurrent setting) serialization rules for
simultaneous asks, presentation-time timeout clocks, and abandoned-call
cleanup. All of it is intentionally omitted. It solves the
reach-the-human problem inside the engine, at design time, and every
piece of it is protocol surface with no consumer the authoring pattern
does not already serve: the blocking a gate provides is the `tool`
node's blocking; the choice a gate collects is the choice schema; the
frontend a gate needs is the operator that steering and events already
serve. Implementations MAY of course ship a console convenience that
watches events and answers a blocking tool via its own channel -- that
is an implementation's frontend, not this specification's contract.

## 7. Validation and Linting

### 7.1 Diagnostic Model

Validation produces a list of diagnostics, each with a severity level. The engine must refuse to execute a pipeline with error-severity diagnostics.

```
Diagnostic:
    rule     : String                    -- rule identifier (e.g., "start_node")
    severity : Severity                  -- ERROR, WARNING, or INFO
    message  : String                    -- human-readable description
    node_id  : String                    -- related node ID (optional)
    edge     : (String, String) or NONE  -- related edge as (from, to) (optional)
    fix      : String                    -- suggested fix (optional)

Severity:
    ERROR     -- pipeline will not execute
    WARNING   -- pipeline will execute but behavior may be unexpected
    INFO      -- informational note
```

### 7.2 Built-In Lint Rules

| Rule ID                  | Severity | Description |
|--------------------------|----------|-------------|
| `start_node`             | ERROR    | Pipeline must have exactly one node with `type="start"`. |
| `start_single_outgoing`  | ERROR    | The start node must have exactly one outgoing edge (Section 4.3). |
| `terminal_node`          | ERROR    | Pipeline must have exactly one node with `type="exit"`. |
| `reachability`           | ERROR    | All nodes must be reachable from the start node via BFS/DFS traversal. |
| `edge_target_exists`     | ERROR    | Every edge target must reference an existing node ID. |
| `start_no_incoming`      | ERROR    | The start node must have no incoming edges. |
| `exit_no_outgoing`       | ERROR    | The exit node must have no outgoing edges. |
| `dead_end`               | ERROR    | Every non-terminal node must have at least one outgoing edge. |
| `tool_routing`           | ERROR    | Tool nodes have one or two outgoing edges; `on_fail`, when set, must name an outgoing-edge target; with two edges, `on_fail` is required (Section 4.7). |
| `parallel_fan_in`        | ERROR    | Every branch path from a parallel node must converge on a single `parallel.fan_in` node without passing through the exit node (Section 4.8). |
| `branch_disjoint`        | ERROR    | A parallel node's branch node-sets must be pairwise disjoint, except for the shared fan-in (Section 4.8). |
| `no_nested_parallel`     | ERROR    | A parallel node may not appear inside another parallel node's branches; nested fan-out is out of scope for this version. |
| `fan_in_single_parallel` | ERROR    | Every `parallel.fan_in` node is the designated fan-in of exactly one parallel node (Section 4.9 depends on this being unambiguous). |
| `parallel_thread_disjoint`| ERROR   | No two nodes that can execute concurrently (nodes of different branches of one parallel node) may resolve to the same thread key (Sections 4.8, 5.4). Fully static. |
| `thread_branch_boundary` | ERROR    | A thread key resolved by a node inside a parallel branch must not be resolved by any node outside that branch (fan-in included): sessions are workdir-bound and cannot span the worktree boundary (Section 12.1). Fully static. |
| `fan_in_entry`           | ERROR    | Every incoming edge of a `parallel.fan_in` node must originate inside its parallel node's branches -- a fan-in reached any other way would read branch evidence that does not exist (Section 4.9). |
| `branch_entry`           | ERROR    | Every incoming edge of a branch-interior node must originate inside the same parallel's branches; branch roots additionally accept their parallel node (Section 4.8 defines the branch node-set). |
| `max_visits_positive`    | ERROR    | `max_visits`, when set, must be a positive integer. |
| `max_parallel_positive`  | ERROR    | `max_parallel`, when set, must be a positive integer. |
| `max_retries_nonnegative`| ERROR    | `max_retries`, when set (on a node or in `defaults`), must be a nonnegative integer. |
| `edge_condition_missing` | ERROR    | Every outgoing edge of a node that routes via the choice schema (codergen and `parallel.fan_in`) with more than one outgoing edge must have a `condition` that is non-empty after trimming whitespace -- the conditions are the routing text presented to the model (Section 4.5). An absent, empty, or whitespace-only condition all fail. |
| `type_known`             | ERROR    | Every node `type` must be registered with the handler registry: an unknown value never falls through to another handler (Section 4.2). |
| `fidelity_valid`         | ERROR    | Fidelity mode values must be one of `full`, `compacted`, `none`, or a mode the active implementation defines (Section 5.4). An unsupported mode is rejected, never given accidental runtime semantics (Section 12.1). |
| `thread_id_collision`    | ERROR    | An explicit `thread_id` must not equal any node ID -- implicit per-node threads and named shared threads are separate namespaces (Section 5.4). |
| `thread_harness_consistent`| ERROR  | Nodes sharing a resolved thread key under session-reusing fidelity modes (`full`, `compacted`) must resolve to models that route to the same harness (Section 12.1). Thread keys resolve statically (Section 5.4), so this check is fully static. |
| `fan_in_max_visits`      | WARNING  | `max_visits` on a `parallel.fan_in` node does not bound the loop at the parallel node (the fan-in is reached via the parallel's `next`, not an offered set); it takes effect only inside branch walks, where an exhausted fan-in starves the branches' final offered sets and fails the run (Sections 3.2-3.4). Bound the loop at the parallel node or a downstream node. |
| `branch_root_max_visits` | WARNING  | `max_visits` on a parallel branch root silently shrinks later fan-outs: an exhausted root is excluded from the offered set the fan-out consumes (Section 4.8). Bound the loop at the parallel node instead. |
| `prompt_on_llm_nodes`    | WARNING  | Codergen nodes should have a `prompt` or `label` field. |

### 7.3 Validation API

```
FUNCTION validate(graph, extra_rules=NONE) -> List<Diagnostic>:
    rules = BUILT_IN_RULES
    IF extra_rules is not NONE:
        rules = rules + extra_rules
    diagnostics = []
    FOR EACH rule IN rules:
        diagnostics.extend(rule.apply(graph))
    RETURN diagnostics


FUNCTION validate_or_raise(graph, extra_rules=NONE):
    diagnostics = validate(graph, extra_rules)
    errors = [d FOR d IN diagnostics WHERE d.severity == ERROR]
    IF errors is not empty:
        RAISE ValidationError with error messages
    RETURN diagnostics
```

### 7.4 Custom Lint Rules

Implementations may register custom lint rules by implementing the rule interface:

```
INTERFACE LintRule:
    name : String
    FUNCTION apply(graph) -> List<Diagnostic>
```

Custom rules are appended to the built-in rules and run during validation.

---

## 8. Model Selection

### 8.1 Fields

Three node fields configure the LLM for a codergen node:

| Field              | Values                      | Description |
|--------------------|-----------------------------|-------------|
| `llm_model`        | Any model identifier string | Provider-native model ID (e.g., `gpt-5.2`, `claude-opus-4-6`) |
| `llm_provider`     | Provider key string         | `openai`, `anthropic`, `gemini`, etc. Auto-detected from the model when unset. |
| `reasoning_effort` | `low`, `medium`, `high`     | Controls reasoning/thinking depth for the LLM |

### 8.2 Resolution

For each property, the value on a node resolves as:

1. Explicit node field -- highest precedence
2. The file-level `defaults` object (Section 2.7)
3. Implementation-configured system default

Every codergen turn carries concrete resolved model, provider, and
reasoning-effort values; session bindings never supply defaults for later
turns (Section 12.1). The recommended system default for
`reasoning_effort` is `high`.

Upstream defines a CSS-like `model_stylesheet` (selectors, classes,
specificity arithmetic) for the same job. It is intentionally omitted:
the file default covers the common case, and a node that differs names
its model where a reader sees it.

### 8.3 Example

```json
{
  "goal": "Implement feature X",
  "defaults": { "llm_model": "claude-opus-4-6" },
  "nodes": [
    { "id": "start", "type": "start", "edges": [{ "to": "plan" }] },
    { "id": "plan", "type": "codergen",
      "llm_model": "claude-sonnet-4-5",
      "edges": [{ "to": "implement" }] },
    { "id": "implement", "type": "codergen",
      "edges": [{ "to": "critical_review" }] },
    { "id": "critical_review", "type": "codergen",
      "llm_model": "gpt-5.2", "reasoning_effort": "high",
      "edges": [{ "to": "exit" }] },
    { "id": "exit", "type": "exit" }
  ]
}
```

`implement` inherits the file default; `plan` and `critical_review` name
their own models.

---

## 9. Extensibility

The extension points are deliberately few:

- **Custom handlers** (Section 4.10) -- new node types by registering a type
  string.
- **Custom lint rules** (Section 7.4) -- appended to the built-in rules.
- **Custom backends** (Sections 1.4, 12) -- anything satisfying
  `CodergenBackend`.
- **Additional content-part types** (Section 3.9) -- the steering/turn parts
  array is an explicit extension point.

**Variable expansion** is the one built-in graph preprocessing step: `$goal`
in node prompts is replaced with the top-level `goal` field -- simple
string replacement, applied by the codergen handler at prompt build time
(Section 4.5), not a templating engine.

Upstream additionally defines an AST transform framework (a registry of
graph-rewriting passes), pipeline composition via graph merging, an HTTP
server mode with nine management endpoints, and pre/post tool-call hooks.
All are intentionally omitted. The graph is data -- anyone wanting
preprocessing can transform the file before submitting it. The only server
surface a run exposes is its control socket (Section 3.9). Tool-call
interception is impossible through a harness-backed backend -- the harness
owns the tool loop -- and harnesses ship their own native hook systems for
exactly that job.

---

## 10. Observability and Events

The engine emits typed events during execution for UI, logging, and metrics integration:

**Pipeline lifecycle events:**
- `PipelineStarted(name, id)` -- pipeline begins
- `PipelineCompleted(duration)` -- pipeline completed
- `PipelineFailed(error, duration)` -- pipeline failed

**Stage lifecycle events:**
- `StageStarted(name, index)` -- stage begins
- `StageCompleted(name, index, duration, next)` -- stage finished; `next` is the chosen successor when one was chosen
- `StageFailed(name, index, error, will_retry)` -- stage's turn errored
- `StageRetrying(name, index, attempt, delay)` -- stage retrying a retryable turn error

**Parallel execution events:**
- `ParallelStarted(branch_count)` -- parallel block started
- `ParallelBranchStarted(branch, index)` -- branch started
- `ParallelBranchCompleted(branch, index, duration)` -- branch converged
- `ParallelCompleted(duration, branch_count)` -- all branches converged

**Checkpoint events:**
- `CheckpointSaved(node_id)` -- checkpoint written

**timeline.jsonl.** Implementations MUST persist this event stream to
`{logs_root}/timeline.jsonl`, one JSON event per line, as it is emitted
(Section 5.6). It costs a file append and it is the external observer's
spine: one never-rotated file that narrates the whole run -- what started,
what ended, where the walk went, when checkpoints landed. Supervisors tail
it; dashboards render it; the run-log segments (Section 12.4) carry the
per-turn detail it deliberately does not. Events are open JSON objects:
implementations MAY attach fields (harness, model, and session on stage
starts; provider-reported usage on completions -- the reserved slot for
usage-aware routing), and consumers MUST ignore fields and event types they
do not recognize. The same stream MAY be served over the control server's
SSE streams (Section 3.9).

Events can be consumed via an observer/callback pattern or an asynchronous stream:

```
-- Observer pattern
runner.on_event = FUNCTION(event):
    log(event.description)

-- Stream pattern (for async runtimes)
FOR EACH event IN pipeline.events():
    process(event)
```

---

## 11. Definition of Done

This section defines how to validate that an implementation of this spec is complete and correct. An implementation is done when every item is checked off.

### 11.1 JSON Parsing

- [ ] Parser accepts a well-formed pipeline document (Section 2.1) and produces the in-memory Graph model with each node carrying its own edges
- [ ] Top-level fields (`name`, `goal`, `defaults`, `nodes`) are extracted correctly
- [ ] The node union validates per type: each type accepts exactly its own fields (Sections 2.4, 2.5)
- [ ] File-level `defaults` fill unset node fields per the resolution order (Section 2.7); a default for a field a node type lacks does not apply
- [ ] Duplicate node IDs, duplicate JSON member names within one object, invalid ID syntax, `null` values, wrong field types, and unknown fields at any level are parse errors (Section 2.8)
- [ ] `defaults` admits exactly the six fields of Section 2.7; anything else there is a parse error; a required field is never satisfied from `defaults`
- [ ] Duration strings parse (`"250ms"`, `"900s"`, `"15m"`, `"2h"`, `"1d"`); bare numbers are rejected
- [ ] A custom-type node parses with its `custom` object carried opaquely

### 11.2 Validation and Linting

- [ ] Exactly one node with `type="start"` is required
- [ ] Start node has exactly one outgoing edge and no incoming edges
- [ ] Exactly one node with `type="exit"` is required
- [ ] Exit node has no outgoing edges
- [ ] Every non-terminal node has at least one outgoing edge (`dead_end`)
- [ ] All nodes are reachable from start (no orphans)
- [ ] All edges reference valid node IDs
- [ ] Tool node routing validates (`tool_routing`: one or two successors; two requires `on_fail` naming one)
- [ ] Choice-schema branch points (codergen and fan-in) carry edge conditions (`edge_condition_missing`)
- [ ] Parallel branches converge on a single fan-in (`parallel_fan_in`); branch node-sets are pairwise disjoint (`branch_disjoint`); no nested fan-out (`no_nested_parallel`); each fan-in belongs to exactly one parallel node (`fan_in_single_parallel`)
- [ ] Concurrent branches resolve to distinct thread keys (`parallel_thread_disjoint`); no thread key spans a branch boundary (`thread_branch_boundary`); explicit `thread_id` never collides with a node ID (`thread_id_collision`)
- [ ] Fan-in nodes are entered only from inside their parallel's branches (`fan_in_entry`); branch interiors are entered only from inside the same branches (`branch_entry`)
- [ ] Numeric bounds validate (`max_visits_positive`, `max_parallel_positive`, `max_retries_nonnegative`)
- [ ] Unregistered `type` (`type_known`) and unsupported fidelity values (`fidelity_valid`) are ERRORs
- [ ] Shared threads route to one harness (`thread_harness_consistent`)
- [ ] Warnings fire: misleading budget placement (`fan_in_max_visits`, `branch_root_max_visits`)
- [ ] Codergen nodes have a `prompt` or `label` (warning if missing)
- [ ] `validate_or_raise()` throws on error-severity violations
- [ ] Lint results include rule name, severity (error/warning), node/edge ID, and message

### 11.3 Execution Engine

- [ ] Engine resolves the start node and begins execution there
- [ ] Each node's handler is resolved from the registry by its `type`; an unregistered type is a terminal pre-execution Error
- [ ] Handler is called with (node, offered, scope, graph) and returns an Outcome or a categorized Error
- [ ] The engine creates `stages/{seq}-{node_id}/` per handler attempt, passes it as `scope.stage_dir`, and writes the returned Outcome to `outcome.json` there for every handler type
- [ ] `stages/latest/{node_id}` is repointed after each successful execution; earlier executions' directories are never overwritten, including across resume (`seq` is checkpointed)
- [ ] A failed attempt's stage directory carries an engine-written `error.json`
- [ ] A set `scope.stop` makes every in-flight handler return an interrupted Error promptly; the engine also observes the signal before each dispatch and each retry attempt and during backoff sleeps
- [ ] Offered successors exclude targets with exhausted visit budgets
- [ ] Single offered successor: engine advances without a decision; codergen choice schema contains no `next`
- [ ] Multiple offered successors: engine follows `outcome.next`; an unoffered `next` fails the run
- [ ] Engine loops: offer -> execute -> resolve next -> checkpoint -> advance -> repeat
- [ ] Checkpoint records the resolved successor (`next_node`); resume continues there without re-asking the choice
- [ ] Terminal node (`type="exit"`) completes the run (`RunResult` COMPLETED)

### 11.4 Visit Budgets

- [ ] Every top-level dispatch of a node increments its visit counter (retry attempts and failure-resume re-executions do not, Sections 3.4, 5.3); counters are checkpointed
- [ ] A node at `max_visits` is excluded from every offered set
- [ ] An exhausted node is absent from the codergen choice-schema enum
- [ ] When all of a node's successors are exhausted, the run fails with an explicit reason

### 11.5 Retry Logic

- [ ] Turn errors categorized `retryable` are retried up to `max_retries` (node, else `defaults.max_retries`, else 0)
- [ ] `terminal` and `interrupted` errors fail the run without retry
- [ ] Exponential backoff with jitter is applied between attempts
- [ ] Attempt counts are tracked per node and checkpointed
- [ ] Routing decisions are never retried

### 11.6 Node Handlers

- [ ] **Start handler:** No-op; single successor enforced by lint
- [ ] **Exit handler:** Reaching it completes the run
- [ ] **Codergen handler:** Expands `$goal`, constructs a fully resolved
      CodergenTurn (choice schema, workdir) and calls
      `CodergenBackend.run()`, passes Errors to the engine, and writes
      prompt.md and response.md to `scope.stage_dir`
- [ ] **Tool handler:** Runs `tool_command` in `scope.workdir`; exit 0 routes to the success target, nonzero routes to `on_fail` or fails the run
- [ ] **Parallel handler:** Freezes the workspace, walks each branch in its own git worktree, waits for all branch walks to return, writes `branches.json` with BranchResult paths BEFORE propagating any branch error; a branch error fails the run as terminal
- [ ] **Fan-in handler:** Executes as codergen with the BranchResults rendered into its prompt; branch worktrees exist at the recorded paths while it runs
- [ ] Custom handlers can be registered by type string

### 11.7 State

- [ ] Engine state is typed (`EngineState`, Section 5.1); handlers receive an ExecutionScope, never a generic context map
- [ ] Checkpoint is saved after every top-level execution (never between retry attempts): on success with the resolved `next_node`, on failure with the failed node as `current_node` and `next_node`, `completed_nodes` untouched, and counters and sessions recording what happened (parallel branch deltas rolled back, Section 4.8)
- [ ] Resume from checkpoint: load checkpoint -> restore state, recovering `seq` as max(checkpoint, stages/ scan) -> continue at next_node
- [ ] Resume from a failure checkpoint (`retry_visit`) skips the retried node's visit increment exactly once; a `max_visits=1` node failed and resumed repeatedly re-executes each time with `node_visits` still 1 (Sections 3.7, 5.3)
- [ ] A failed run's worktrees survive on disk; a completed run's are cleaned up at Finalize by sweeping `worktrees.jsonl`
- [ ] Stage artifacts are written to `{logs_root}/stages/{seq}-{node_id}/` (prompt.md, response.md, engine-written outcome.json)

### 11.8 Human-in-the-Loop

No protocol to certify: human interaction has no engine surface
(Sections 4.6, 6). The authoring pattern rides on machinery proven
elsewhere -- the choice schema (11.5), the tool handler's blocking
command and exit-code routing (11.6), and steering (11.10). But
Section 4.6 states cross-feature replay semantics, and those are
certified with one scenario (disclosure of limits, not delivery
guarantees):

- [ ] A node whose execution performs an observable external side effect
  (a stub "send") fails after the side effect and is resumed: the side
  effect occurs a second time, and nothing in `ExecutionScope`
  distinguishes the re-execution from a fresh decision. Repeated inside
  a parallel branch, the replayed fan-out runs in a fresh worktree with
  a rebound session -- no marker file or session memory carries forward.

### 11.9 Model Selection

- [ ] Node-level `llm_model`, `llm_provider`, `reasoning_effort` resolve with file-level `defaults` (Section 2.7)
- [ ] Provider auto-detects from the model when unset
- [ ] Every codergen turn carries concrete resolved model, provider, and reasoning-effort values

### 11.10 Observability

- [ ] Engine emits the Section 10 event stream
- [ ] Events are persisted to `{logs_root}/timeline.jsonl` as JSONL
- [ ] Run-log segments, the segment index (`events/index.jsonl`), and `current.jsonl` behave per Section 12.4
- [ ] Steering requests are audited to the active execution's `steering.jsonl`
- [ ] The engine rejects steering while a parallel node is the active top-level execution, before backend handoff

### 11.11 Cross-Feature Parity Matrix

Run this validation matrix -- each cell must pass:

| Test Case                                        | Pass |
|--------------------------------------------------|------|
| Parse a simple linear pipeline (start -> A -> B -> done) | [ ] |
| Parse a pipeline with top-level fields (goal, name, defaults) | [ ] |
| Reject unknown fields and duplicate node IDs     | [ ] |
| Validate: missing start node -> error            | [ ] |
| Validate: missing exit node -> error             | [ ] |
| Validate: unreachable node -> error              | [ ] |
| Validate: tool node with two successors and no `on_fail` -> error | [ ] |
| Execute a linear 3-node pipeline end-to-end      | [ ] |
| Agent chooses among multiple successors via the choice schema | [ ] |
| Single-successor node advances with no `next` in the schema | [ ] |
| Tool node routes on exit code (0 -> success target, nonzero -> `on_fail`) | [ ] |
| Retryable turn error retried (max_retries=2), then run fails | [ ] |
| `max_visits` exhaustion removes the target from offered sets | [ ] |
| Run fails when every successor is exhausted      | [ ] |
| Checkpoint save and resume produces same result   | [ ] |
| File-level `defaults.llm_model` applies; node field overrides | [ ] |
| Prompt variable expansion ($goal) works           | [ ] |
| Parallel fan-out runs branches in isolated worktrees and converges at fan-in with BranchResult evidence | [ ] |
| Custom handler registration and execution works   | [ ] |
| timeline.jsonl records the full run               | [ ] |
| Pipeline with 10+ nodes completes without errors  | [ ] |

### 11.12 Integration Smoke Test

End-to-end test with a real LLM callback:

```
-- Test pipeline: plan -> implement -> review -> done, with an agent-chosen
-- fix loop and a visit budget
PIPELINE = """
{
  "name": "test_pipeline",
  "goal": "Create a hello world Python script",
  "nodes": [
    { "id": "start", "type": "start", "edges": [{ "to": "plan" }] },
    { "id": "plan", "type": "codergen",
      "prompt": "Plan how to create a hello world script for: $goal",
      "edges": [{ "to": "implement" }] },
    { "id": "implement", "type": "codergen",
      "prompt": "Write the code based on the plan", "max_visits": 3,
      "edges": [{ "to": "review" }] },
    { "id": "review", "type": "codergen",
      "prompt": "Review the code for correctness. Choose: finish if it is correct, or send it back to implementation with your findings.",
      "edges": [
        { "to": "done", "condition": "Correct" },
        { "to": "implement", "condition": "Fix" }
      ] },
    { "id": "done", "type": "exit" }
  ]
}
"""

-- 1. Parse
graph = parse_pipeline(PIPELINE)
ASSERT graph.goal == "Create a hello world Python script"
ASSERT LENGTH(graph.nodes) == 5
ASSERT LENGTH(edges_total(graph)) == 5

-- 2. Validate
lint_results = validate(graph)
ASSERT no error-severity results in lint_results

-- 3. Execute with LLM callback
result = run_pipeline(graph, llm_callback = real_llm_callback)

-- 4. Verify
ASSERT result.status == COMPLETED
-- (artifacts_exist resolves through the stages/latest/{node_id} pointer,
--  Section 5.6)
ASSERT artifacts_exist(logs_root, "plan", ["prompt.md", "response.md", "outcome.json"])
ASSERT artifacts_exist(logs_root, "implement", ["prompt.md", "response.md", "outcome.json"])
ASSERT artifacts_exist(logs_root, "review", ["prompt.md", "response.md", "outcome.json"])

-- 5. Verify the review node's routing decision was recorded
review_outcome = read_json(logs_root + "/stages/latest/review/outcome.json")
ASSERT review_outcome.next IN {"done", "implement"}

-- 6. Verify checkpoint
checkpoint = load_checkpoint(logs_root)
ASSERT checkpoint.current_node == "done"
ASSERT "plan" IN checkpoint.completed_nodes
ASSERT "implement" IN checkpoint.completed_nodes
ASSERT "review" IN checkpoint.completed_nodes
ASSERT checkpoint.node_visits["implement"] >= 1

-- 7. Verify the timeline narrates the run
ASSERT file_exists(logs_root + "/timeline.jsonl")
```

The `llm_callback` above is the test's stand-in for a `CodergenBackend`
(Section 4.5); a harness-backed backend (Section 12) satisfies it.

### 11.13 HarnessAdapter Conformance

A production `HarnessAdapter` is done when the same standalone
conformance program passes against its real native harness. The program runs
outside the graph engine and `HarnessBackend`; it calls only the interface in
Section 12.2 and observes its public behavior and workspace effects.

The conformance program and the first real adapter are developed together,
after directly characterizing the corresponding native harness behavior. A
simulated or replayed harness MUST NOT define the contract or provide
certification evidence. Such a test aid MAY be derived from an established
real integration later, but it is not part of this definition of done.

#### Conformance Program

The conformance executable calls the adapter directly in its own process. The
native harness wrapped by the adapter may run in a separate process as usual;
the specification does not introduce another process, RPC protocol, or loading
mechanism between the conformance code and the adapter. Where a scenario calls
for reconstruction, the executable discards the adapter instance and creates a
new one using the test target's ordinary constructor or factory.

The evidence for the common program is the public adapter transcript: every
method and argument supplied by the controller, every `on_event` callback,
every returned object or Error, call and return ordering and timing, and
observable workspace effects. The common program MUST NOT require raw
native-protocol capture, an `on_harness_event` escape hatch, provider-specific
logs, or adapter-specific synchronization hooks.

This does not prevent an adapter implementation from privately capturing or
interpreting native harness input and output during development.
Provider-specific diagnostics and unit tests MAY inspect construction of
native requests, normalization of raw events, rejection of nonconforming
result candidates, error classification, control handoff races, timeout
behavior, and compaction mechanics. Those tests are development aids and
engineering hygiene: they do not constitute conformance evidence and cannot
replace the shared real-harness program's public behavioral proof.

Each run records the adapter identity, native harness name and version, and
pass or fail for each required scenario. On failure it records a useful message
and the public observations needed to diagnose that failure. Full transcripts,
timings, session IDs, callbacks, and workspace snapshots MAY be retained for
diagnosis but are not required on successful runs. The executable exits
successfully only when every required scenario passes.

The scenarios MAY share setup and fixtures where that does not invalidate an
assertion. For steering, interruption, and timeout, the program supplies one
simple long-running workspace task that writes a started marker, waits for a
known duration, then writes a finished marker. The controller uses reasonable
bounded deadlines and a short post-return observation window when asserting
"promptly" or that no later callback occurred. The specification does not
prescribe a native barrier or provider-specific control mechanism.

#### Required Scenarios

1. **Real workspace turn.** Create a session with a configured real model and
   valid workdir. Place unpredictable task data only in that workdir, then ask
   the harness to inspect it, make a trivial tool-backed change, and return the
   task-dependent result under a strict schema. The schema uses runtime-chosen
   property and enum names and includes a nested value and ordered array. The
   program independently validates the returned object and verifies the
   workspace result. The driver supplies one `text` part; the event stream opens
   with an exact copy of that parts array, contains complete assistant content
   and a matched `tool_call` / `tool_result` pair, and produces no callbacks
   after `run_turn` returns.

2. **Continuation and reconstruction.** Give the session an unpredictable
   value to remember. Discard the adapter instance, construct a new one in the
   same conformance process, and revisit the exact returned session ID under an
   incompatible strict schema. The revisit returns the remembered value even
   though it appears in neither the revisit prompt, schema, nor workdir.

3. **Isolation and serialization.** Create distinct sessions A and B and run
   the long-running task in both concurrently with different workdirs and
   nonces. After both started markers appear, their results, events, callbacks,
   and workspace changes do not mix. While A is live, a second `run_turn` for A
   either returns an Error or waits; it never overlaps A's first turn.

4. **Invalid inputs and public errors.** A generated unknown session ID makes
   `run_turn` return a terminal Error without assistant or tool events or
   workspace changes. A deliberately nonexistent model supplied through the
   ordinary create-and-run path likewise produces a terminal Error before task
   activity; if `create_session` accepts it, `run_turn` must reject it. Every
   observed Error has an allowed category and non-empty message.

5. **Steering.** Start the long-running task, wait for its started marker, and
   call `steer` with distinctive text containing an unpredictable value and an
   instruction to record it in the workspace. Before the turn ends, its event
   stream contains exactly one additional `user` event with that text and the
   requested workspace effect contains the value. This is one end-to-end smoke
   test, not a promise that models always comply with steering. Separately,
   steer a newly created session that has never run; its first turn contains no
   queued steering event.

6. **Interruption and timeout.** Interrupt the active long-running task.
   `interrupt` returns promptly, `run_turn` returns an interrupted Error rather
   than success within a bound materially shorter than the task's known normal
   duration, no callback arrives afterward, and the same session completes a
   later normal turn. Repeat the task with a short timeout: it returns an
   interrupted Error before normal completion and the session remains usable.
   These checks do not require transactional cleanup of arbitrary detached
   tool subprocesses or proof of the adapter's internal stop mechanism.

7. **Compaction.** Establish an unpredictable remembered value, call `compact`,
   wait for it to return, and immediately revisit the same session. The revisit
   succeeds and returns the remembered value without it appearing in the
   revisit prompt, schema, or workdir. Compacting an active session returns a
   terminal Error without disrupting its turn: that turn later succeeds and
   writes its finished marker. Compacting an unknown session returns a terminal
   Error.

Passing all scenarios against one identified native harness version establishes
that the adapter is release-ready and demonstrably usable. It intentionally
does not claim exhaustive JSON Schema keyword coverage; exact model or
reasoning-effort forwarding and model changes; steering's narrow completion
race; distinguishing native compaction from a behaviorally equivalent no-op on
a short conformance history; usage semantics; whole-conformance-process
restart; every authentication deployment or provider failure; performance or
soak limits; disaster recovery; cleanup of arbitrary detached tool processes;
general model compliance with steering; or compatibility with future harness
versions. Those cases constitute the bounded residual risk rather than reasons
to delay the first usable adapter.

### 11.14 HarnessBackend Conformance

`HarnessBackend` is pure orchestration over the adapter seam, so -- unlike
the adapter, which must be certified against its real harness
(Section 11.13) -- the backend's behavior is proven with **scripted
adapters** injected through the `adapters` map: test doubles that record
calls, return prepared objects and Errors, and let every claim below run
deterministically without a provider. The two certifications compose: a
real adapter passing 11.13 behind a backend passing this section is the
release-ready `CodergenBackend`.

A `HarnessBackend` implementation is done when an executable suite proves,
through the public `CodergenBackend` surface plus `bindings()` and the run
directory:

- [ ] **Harness selection.** The resolved provider/model selects the
      adapter via the configured routing table; an unroutable
      provider/model is a terminal Error.
- [ ] **Binding at open.** A reusable thread is bound
      `{harness, session_id, workdir}` at session open, before the first
      turn completes: `bindings()` taken mid-turn already contains it, so
      a failure checkpoint durably records the session the dying node
      opened (Section 5.3).
- [ ] **`full`.** A revisit reuses the exact bound session and never calls
      `compact`.
- [ ] **`compacted`.** A revisit calls `compact` and awaits success before
      `run_turn`; a compact Error is returned categorized, and the revisit
      prompt is never sent (Section 5.4).
- [ ] **`none`.** Every visit opens a fresh session under the disjoint
      `none_key` namespace, replacing the prior binding; the live turn is
      steerable like any other.
- [ ] **Workdir binding.** The full workdir x harness matrix on a bound
      reusable thread: same harness + same workdir reuses the session;
      same harness + different workdir treats the binding as stale -- a
      fresh session opens in the turn's workdir and replaces the binding,
      with no Error; a different harness is a terminal Error whether the
      workdir matches or not -- the harness check precedes staleness
      classification (Section 12.1).
- [ ] **Reconstruction.** A backend constructed in a new process with a
      restored bindings map and an empty live list continues a bound
      session on the next revisit turn -- same native session ID.
- [ ] **Outcome conversion.** A schema-conforming adapter object becomes a
      native Outcome (`next` present exactly when the choice schema
      required it); a nonconforming result surfaces as a terminal Error;
      every categorized adapter Error -- `retryable`, `terminal`,
      `interrupted` -- passes through to the handler unchanged.
- [ ] **Steering cardinality.** Zero live turns reject as not-active; one
      live turn hands the parts to that session's adapter; more than one
      rejects as ambiguous. (Rejection of steering during a parallel
      top-level step is the engine's job and happens before the backend
      is reached, Section 3.9.)
- [ ] **Interrupt.** `interrupt_all()` signals every currently live
      adapter turn, and only live ones.
- [ ] **Run log.** Each turn writes exactly one zero-padded,
      sequence-prefixed segment stamped with timestamps and the node id;
      `events/index.jsonl` gains its line when the segment is created;
      the segment counter resumes above the highest existing sequence
      after reconstruction; `current.jsonl` swaps atomically, and pins to
      the index while more than one turn is live (Section 12.4).
- [ ] **Honest logging failure.** A failed segment or index write is never
      reported as a successfully recorded turn; the backend documents its
      smallest honest failure behavior.

**Integration proof:** one graph exercising both harnesses, session reuse
across a loop, compaction, steering, interruption, checkpoint
reconstruction in a fresh process, and a parallel step whose branch turns
carry worktree workdirs -- then inspect the native session IDs, the
bindings map, and the run directory against every claim above.

## 12. Harness-Backed Backends

The recommended `CodergenBackend` implementation wraps third party coding agent
harness software such as ChatGPT's codex app-server or Claude Code, and routes
calls to the correct harness and session ID. A Harness Adapter implementation
implements the HarnessAdapter interface and translates a given harness
vocabulary into the harness's native protocol. It MAY be somewhat lossy.

Primary reasons for choosing this are highly convincing:

1. Claude and ChatGPT include their specific tools in the model training,
   meaning the provider has spend immense effort into perfecting the tool
   definitions and system messages.
2. Claude Code and ChatGPT apps include subscription tiers that dramatically
   decrease the per-token price of running them.

> The reasons for rolling your own `CodergenBackend` include taking complete
> control over the system message(s) and tool definitions, or in order to
> completely own context storage along with the capacity to transport one
> context between different model providers.


### 12.1 The Backend

```
HarnessBackend (implements CodergenBackend):
    adapters : Map<String, HarnessAdapter>  -- harness name -> adapter
    threads  : Map<String, ThreadBinding>   -- thread key -> binding;
                                            -- checkpointed (Section 5.3)
    live     : List<String>                 -- keys of threads with a
                                            -- turn in flight; in-memory
                                            -- only, empty at construction

ThreadBinding:
    harness    : String    -- which adapter owns the session
    session_id : String    -- harness-native session ID
    workdir    : Path      -- the session's working directory, fixed at
                           -- create_session (see Construction below)

FUNCTION run(turn: CodergenTurn) -> OneOf<Outcome, Error>:
    fidelity = turn.fidelity
    IF fidelity NOT IN {"full", "compacted", "none"}
            AND fidelity is not a mode this implementation defines:
        -- rejected outright (Section 5.4): an unrecognized mode must not
        -- fall through to accidental fresh-session semantics
        RETURN Error("terminal", "unsupported fidelity mode: " + fidelity)
    key      = turn.thread_key
    IF fidelity == "none":
        key = none_key(turn.node_id)  -- internal namespace; never clobbers a reusable thread
    h = select_harness(turn.provider, turn.model)
    revisit  = FALSE
    IF (key IN threads) AND (fidelity IN {"full", "compacted"}):
        IF threads[key].harness != h:
            -- checked BEFORE workdir staleness: a harness change is
            -- terminal on every path (Section 11.14), never masked by a
            -- simultaneous workdir change
            RETURN Error("terminal", "logical thread cannot change harness")
        revisit = (threads[key].workdir == turn.workdir)
        -- a binding whose workdir differs from the turn's is STALE, not an
        -- error: its conversation describes a different workspace state (a
        -- previous fan-out's worktree). It is not resumed; the create
        -- branch below replaces it with a fresh session in the turn's
        -- workdir (see Construction)
    IF not revisit:
        -- first visit, none-fidelity visit, or stale-workdir replacement:
        -- bind at session open (Section 5.3)
        s, err = adapters[h].create_session(turn.model, turn.workdir)
        IF err: RETURN err
        threads[key] = ThreadBinding(h, s, turn.workdir)
    ELSE:
        IF fidelity == "compacted":
            err = adapters[h].compact(threads[key].session_id, turn.workdir)
            -- on failure the turn is not sent (Section 5.4)
            IF err: RETURN err
    binding = threads[key]

    -- 2. Run one turn, steerable while live
    live.append(key)
    response, err = adapters[binding.harness].run_turn(
                  binding.session_id, turn.model, turn.reasoning_effort,
                  turn.output_schema, turn.workdir, turn.parts, turn.timeout,
                  run_log(turn.node_id))
    live.remove(key)

    -- 3. Return a native Outcome or the categorized adapter Error
    IF err is not None: RETURN err
    outcome, err = decode_outcome(response)
    IF err: RETURN Error("terminal", err.message)
    RETURN outcome

FUNCTION steer(parts) -> status:                  -- Section 3.9
    -- defense-in-depth only: the engine has already rejected steering
    -- while a parallel step is the active top-level execution (3.9);
    -- this check cannot detect a fan-out whose branch turns happen not
    -- to overlap
    IF live is empty:   RETURN not_active
    IF size(live) > 1:  RETURN ambiguous_target
    t = threads[live[0]]
    adapters[t.harness].steer(t.session_id, parts)
    RETURN accepted

FUNCTION interrupt_all():   -- shutdown
    FOR key IN live:
        t = threads[key]
        adapters[t.harness].interrupt(t.session_id)

FUNCTION bindings() -> Map<String, ThreadBinding>
    -- snapshot of threads; checkpointed (Section 5.3)
```

`none` starts a fresh session on every visit, carrying nothing beyond the
rendered prompt (Section 5.4). It enters `threads` under a key derived from the
node id -- steerable and checkpointed like any other binding -- and a later
visit simply replaces it. `none_key` MUST use a namespace disjoint from
reusable thread keys.

**Harness selection.** `select_harness` uses the turn's resolved provider and
model with the configured routing table -- Anthropic -> Claude Code and OpenAI
-> Codex by default. Session threads cannot be transported between harnesses;
every visit provides concrete model and reasoning-effort values. A shared
thread whose turns route to different harnesses is both a lint error
(`thread_harness_consistent`, Section 7.2) and a terminal runtime Error.

**Construction.** A `HarnessBackend` instance is constructed per run with
the run's `logs_root`. It carries no workspace path of its own: every turn
arrives with its `workdir` (the run workspace, or a branch worktree during
a fan-out, Section 4.1), and the backend passes that to the adapter. A
session is workdir-bound at `create_session`, and the backend records the
workdir in the binding. A turn arriving with a different workdir than its
key's binding treats that binding as stale and opens a fresh session in the
turn's workdir, replacing it -- sessions never move between working
directories, and the stale session's context would describe a workspace
state that no longer exists. This is exactly the right semantic for the one
case that produces it legitimately: a re-executed parallel node creates
fresh worktrees, so its branch nodes' default threads rebind fresh per
fan-out. A thread *explicitly* shared across a branch boundary would also
trip it -- silently losing the author's intended continuity -- so that
shape is a lint ERROR instead (`thread_branch_boundary`, Section 7.2). On
resume (and at construction generally) the backend recovers its run-log
segment counter from the highest sequence already present in
`{logs_root}/events/` before allocating; allocation is atomic across
concurrent branch turns (Section 12.4).

### 12.2 The HarnessAdapter Interface

One adapter per harness. Adapters hold no durable state of their own --
the durable state is the harness's.

```
INTERFACE HarnessAdapter:
    FUNCTION create_session(model, workdir) -> OneOf<SessionId, Error>
    -- Establishes a new empty session and returns an id required by the other methods

    FUNCTION run_turn(
      session_id, model, reasoning_effort, output_schema, workdir, parts,
      timeout, on_event) -> OneOf<JSON Object, Error>
        -- blocks until the turn ends; at most one live turn per session;
        -- success validates against the exact supplied output_schema

    FUNCTION steer(session_id, parts)
        -- Best-effort signal to the session if active; no-op otherwise

    FUNCTION interrupt(session_id)
        -- Requests that the session halt if active; no-op otherwise

    FUNCTION compact(session_id, workdir) -> Maybe<Error>
        -- Blocks until harness-native compaction completes. Error: session is active or service unavailable

Error:
    category : "retryable" | "terminal" | "interrupted"  -- Appendix D
    message  : String
```

#### Interface Notes

1. Every returned Error is categorized. Whether a harness-specific usage,
   rate-limit, or similar failure from `compact` is retryable is
   implementation-defined; adapters MUST classify it according to whether a
   later attempt can reasonably succeed. This specification does not prescribe
   a usage-recovery policy.
2. `create_session`, `run_turn`, and `compact` clearly differentiate
   non-retryable errors for things like a non-existing model or session ID, or
   compacting a session that is still active. `steer` and `interrupt` are
   best-effort signals and return nothing.
3. `run_turn` and `steer` receive the same non-empty ordered
   `List<ContentPart>` defined in Section 3.9. The only mandatory part type is
   `text`; an adapter translates supported extensions into native harness input.
4. `output_schema` describes a JSON object. The adapter translates that schema
   into the harness's native structured-output mechanism and treats its contents
   as opaque application data: it does not interpret nodes, edges, routing, or
   Outcome semantics. A successful `run_turn` returns the JSON object only after
   validating it against the exact supplied schema. If the harness cannot
   produce a conforming object, `run_turn` returns an Error rather than a
   successful nonconforming response. The orchestration layer owns construction
   and interpretation of the schema.
5. How an adapter produces the result is implementation-specific. It may use
   harness-native structured output, a follow-up evaluation turn, an
   agent-written `response.json`, or another harness-appropriate mechanism.
   Implementers should choose the simplest reliable approach; the adapter
   remains responsible for running the agentic task and returning a validated
   conforming object.
6. Steering is "deliver when possible / best effort" by the harness so that
   the agent/user who submits it can fire and forget.
7. We purposely do not insist on extremely specific semantics for steering so
   that different harnesses can have slightly different mechanics without
   preventing implementation or forcing complex implementation.  The primary
   idea should be implemented in a reasonable way -- a steering message is
   meant to be added to the message array soon, without waiting for the
   current task to run to completion.  Adapter MUST NOT deliver a steering
   message to a session not currently running a prompt task.
8. We currently take a pass on defining authentication.  Since most harnesses
   have their own separate authentication mechanism, this spec version leaves
   authentication as a configuration detail external to the attractor software.
9. Interrupt is a signal, not a completion protocol. The in-flight `run_turn`
   return is the observation that work ended. The same native stop mechanism is
   reused for timeout enforcement.
10. Adapters emit events; they are not responsible for persisting them to storage.
11. This spec does not define disaster prevention or recovery.  Destruction of
   harness storage is not recoverable and the recourse is to re-run the workflow.
   On this topic: agents working on this are advised to consider occasional
   workflow retry as an acceptable fallback in place of difficult recovery logic.

### 12.3 Events

`on_event` is the adapter's narration of one turn: a stream
of JSON objects, each a complete logical item -- never a provider-native
delta. The types:

```
user        { parts }                -- initial or steering user message,
                                     -- using ordered ContentPart values
assistant   { text }                 -- one complete assistant message
thinking    { text }                 -- public reasoning output, composed
                                     -- to paragraphs or whole messages
tool_call   { call_id, name, args }
tool_result { call_id, output }
usage       { ... }                  -- OPTIONAL; provider-reported
                                     -- counters, verbatim
```

#### Event Notes

1. The adapter coalesces the harness's streamed fragments and emits each
   item once, when the harness reports it complete. When the harness
   supplies a canonical completed-item value, the adapter emits that
   value rather than text it reconstructed from deltas. Consumers MUST
   NOT need to concatenate events to recover a message.
2. `user` makes the segment a self-contained transcript: it opens with the
   initial turn parts, and a steering instruction appears when the adapter
   hands it to the harness. The two are not differentiated; position tells the
   story.
3. `thinking` items may follow the harness's native boundaries (e.g. one
   event per reasoning-summary block) but are composed to at least
   paragraph size.
4. `tool_call` and `tool_result` are distinct events -- a call is a
   complete observation while the tool runs -- paired by `call_id`.
5. The type set is open: adapters MAY emit additional types, and
   consumers MUST ignore types they do not recognize.
6. Accumulation lives in the adapter, per in-flight `run_turn`, because
   only the adapter knows native item identities and completion
   signals; concurrent turns never mix.
7. `usage` is optional and verbatim -- this spec defines no counter
   schema; it exists so usage-aware routing has something to read.

### 12.4 Run Log

Each backend turn (codergen and fan-in executions) appends its events to a
segment file, and the run maintains an index and a live pointer:

- `{logs_root}/events/{seq}-{node_id}.jsonl` -- one segment per backend
  turn, sequence-prefixed (zero-padded to six digits) so a directory
  listing sorts in execution order. One event per line, stamped by the
  backend with a timestamp and the originating node id. A multi-turn node
  execution (a retry, a branch that revisits a node) produces one segment
  per turn. The backend's segment counter is its own -- recovered from
  the `events/` directory at construction (Section 12.1) -- and is
  independent of the engine's stage-dir counter (Section 3.5): equal
  numbers across `events/` and `stages/` imply no correspondence.
- `{logs_root}/events/index.jsonl` -- the append-only **segment index**,
  a discovery manifest, not a turn segment: the backend appends one line
  `{seq, node_id, path, ts}` at the moment it creates any segment,
  whole-run. A watcher that tails the index discovers every segment as it
  is born -- including concurrent branch segments during a fan-out --
  without predicting future paths.
- `{logs_root}/current.jsonl` -- a symlink, atomically swapped by the
  backend. A turn that starts while no other turn is live points it at
  that turn's segment. The moment two turns are ever live together (only
  possible during a fan-out), the backend points it at
  `events/index.jsonl` and leaves it there until the live count returns
  to zero -- no per-turn flapping while branches interleave -- after
  which the next lone turn swaps it back to a segment. (The live count
  can legitimately touch zero mid-fan-out while every branch sits at a
  tool node or another node with no backend turn, ending stickiness
  early; that residual retargeting
  is accepted.) The index is the surface a watcher can always rely on;
  `current.jsonl` is a best-effort convenience pointer to wherever live
  activity is. A swap is ordinary log rotation:
  `tail -F` (follow the name, reopen on inode change) is the tailing
  contract; the control server's SSE streams (Section 3.9) are the
  no-filesystem alternative.

The backend writes the segments, appends the index, and swaps
`current.jsonl`; adapters only emit events through `on_event`. Nodes that
run no backend turn (start, tool nodes) produce no segment;
`current.jsonl` continues to point at the most recently started segment,
and `timeline.jsonl` (Section 10) narrates those steps. The parallel node
itself owns no run-log file -- its `branches.json` (Section 4.8) carries
each branch's segment paths for after-the-fact readers, while the index
serves the live watcher. Engine lifecycle stays out of the run log -- the
checkpoint and the engine event stream (Section 10) carry it.

---

## Appendix A: Complete Field Reference

### Top-Level Fields

| Field                   | Type       | Default  | Description |
|-------------------------|------------|----------|-------------|
| `name`                  | String     | `""`     | Display name for the pipeline |
| `goal`                  | String     | `""`     | Pipeline-level goal description; `$goal` in prompts |
| `defaults`              | Object     | `{}`     | File-level node defaults (Section 2.7) |
| `nodes`                 | Node array | required | The graph; each node carries its own edges |

### Node Fields (common to every type)

| Field                   | Type       | Default       | Description |
|-------------------------|------------|---------------|-------------|
| `id`                    | String     | required      | Unique node identity |
| `type`                  | String     | required      | Handler type discriminator (Section 2.5) |
| `label`                 | String     | node ID       | Display name |
| `edges`                 | Edge array | `[]`          | This node's outgoing edges |
| `max_visits`            | Integer    | unset         | Visit budget; exhausted nodes are not offered (Section 3.4) |

### Node Fields (per type)

| Field                   | Type     | Default       | On types | Description |
|-------------------------|----------|---------------|----------|-------------|
| `prompt`                | String   | `""`          | codergen, parallel.fan_in | LLM prompt (supports `$goal` expansion) |
| `max_retries`           | Integer  | inherited     | codergen, parallel.fan_in, custom | Additional attempts for retryable turn errors (Section 3.5) |
| `fidelity`              | String   | inherited     | codergen, parallel.fan_in | Context fidelity mode (Section 5.4) |
| `thread_id`             | String   | node ID       | codergen, parallel.fan_in | Session reuse key; unset = the node's own thread (Section 5.4) |
| `timeout`               | Duration | unset         | codergen, parallel.fan_in, tool | Max execution time (Sections 4.7, 12.2) |
| `llm_model`             | String   | inherited     | codergen, parallel.fan_in | LLM model (Section 8) |
| `llm_provider`          | String   | auto-detected | codergen, parallel.fan_in | LLM provider override |
| `reasoning_effort`      | String   | inherited     | codergen, parallel.fan_in | Reasoning depth: low/medium/high (Section 8) |
| `tool_command`          | String   | required      | tool     | Shell command (Section 4.7) |
| `on_fail`               | String   | `""`          | tool     | Successor on nonzero exit (Section 4.7) |
| `max_parallel`          | Integer  | `4`           | parallel | Max concurrent branches (Section 4.8) |
| `custom`                | Object   | `{}`          | custom types | Opaque handler configuration (Sections 2.5, 4.10) |

### Edge Fields

| Field          | Type     | Default | Description |
|----------------|----------|---------|-------------|
| `to`           | String   | required | Target node ID |
| `condition`    | String   | unset   | Routing condition presented to the chooser; required at choice-schema branch points (Section 7.2). The engine never reads it. |

---

## Appendix B: Node Types

| Type              | Handler Behavior |
|-------------------|------------------|
| `start`           | No-op entry point; exactly one per pipeline |
| `exit`            | Reaching it completes the run; exactly one per pipeline |
| `codergen`        | LLM task |
| `parallel`        | Concurrent branch execution |
| `parallel.fan_in` | Consolidate branch results (codergen variant) |
| `tool`            | Shell command; routes on exit code |

---

## Appendix C: Outcome File

The engine writes an `outcome.json` file into every successful execution's
stage directory (`stages/{seq}-{node_id}/`, Sections 3.5, 5.6), for every
handler type: the returned Outcome (Section 5.2) serialized as JSON, an
audit record for observers. Handlers never write it, and the engine never
reads it back; routing already happened through the returned Outcome.

```
{
    "next": "<node_id, present when the node had >1 offered successor>",
    "notes": "Human-readable account of the stage"
}
```

Implementations MAY attach additional observational fields (timing, attempt
number, model, session ID). Consumers MUST ignore fields they do not
recognize.

---

## Appendix D: Error Categories

Every operational Error carries one of three categories:

**Retryable** -- transient failures where re-execution may succeed. Examples: LLM rate limits, network timeouts, temporary provider unavailability. The engine retries these within the node's budget (Section 3.5).

**Terminal** -- permanent failures where re-execution will not help. Examples: a nonexistent model or session ID, a result that cannot conform to the supplied schema, misconfiguration, authentication failures. These fail the run (Section 3.7).

**Interrupted** -- the turn was deliberately stopped: an operator stop via the StopSignal (Section 4.1), an interrupt request, or timeout enforcement (Section 12.2, note 9). An interrupted turn fails the run (an operator stop is simply a failed run with a "stopped" reason, resumable like any other); the failure checkpoint (Section 3.7) records the session binding, so the session stays bound and continuable on resume.

Structural defects in the pipeline itself -- no start node, unreachable nodes -- are validation diagnostics (Section 7), caught before execution; they are not runtime Errors.

Harness adapters and Codergen backends classify operational failures as
`retryable`, `terminal`, or `interrupted`. A harness-backed backend returns its
adapter's categorized Error without converting it into an Outcome, and the
handler passes it to the engine unchanged: retryable errors are retried within
the node's budget (Section 3.5); terminal and interrupted errors, and
retryable errors that outlive the budget, fail the run (Section 3.7). An
interrupted or timed-out turn's session stays bound and resumable, its
activity up to the stop preserved in the run log.
