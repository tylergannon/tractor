---
title: Authoring pipelines
description: Write a small typed graph, validate it, and let Tractor coordinate the work through the agent harnesses you already use.
eyebrow: Pipeline guide
order: 2
sourceLabel: Read the normative Tractor specification
sourceUrl: https://github.com/tylergannon/tractor/blob/main/docs/spec.md
---

Tractor pipelines are closed, typed graphs written as JSON or YAML. Every node declares what kind of work it performs and owns the routes that can follow it. That keeps the decision and its possible next steps together.

> Start with the smallest graph that can prove the outcome. Add retries, branches, parallel work, or supervision only when the workflow actually needs them.

## Your first pipeline

Save this as `pipeline.yaml`. It asks one agent to make a focused change, then uses a shell command as a mechanical acceptance gate.

```yaml
name: ship-a-fix
goal: Fix the reported bug and leave the repository passing its tests.

defaults:
  llm_provider: openai
  llm_model: gpt-5.6-sol
  reasoning_effort: high
  timeout: 20m

start: implement

nodes:
  - id: implement
    type: codergen
    prompt: |
      Investigate $goal. Make the smallest complete change and explain
      the evidence that should be checked before shipping.
    edges:
      - to: verify

  - id: verify
    type: tool
    tool_command: go test ./...
    on_success: success
    on_error: implement
    max_visits: 3
```

Ask Codex to use it:

```text
Validate pipeline.yaml with Tractor, start it in this repository, and monitor it until it finishes. Tell me if you need a decision.
```

Codex will use Tractor's deferred MCP tools to validate the file, start a detached run, and inspect it by run ID. The run survives an MCP or Codex restart. If you prefer the CLI, validate with `tractor validate pipeline.yaml`; direct runs also require explicit workspace and log paths.

```sh
tractor validate pipeline.yaml
tractor run pipeline.yaml --workdir . --logs .tractor/runs/ship-a-fix
```

## The document shape

Only `start` and `nodes` are required at the top level.

| Field      | What it does                                                  |
| ---------- | ------------------------------------------------------------- |
| `name`     | Gives the pipeline a display name.                            |
| `goal`     | Defines the objective and becomes `$goal` inside prompts.     |
| `defaults` | Supplies shared model, retry, fidelity, and timeout settings. |
| `start`    | Names the first walk node.                                    |
| `nodes`    | Holds the flat, typed graph.                                  |

JSON is canonical and strictly validated. YAML is an authoring convenience that decodes through the same generated schema, so it supports comments and multiline strings without creating a second graph language.

Node IDs must match `[A-Za-z_][A-Za-z0-9_]*`. `success` and `failure` are reserved terminal targets; you do not declare nodes for them. Unknown fields and `null` values are rejected.

## Choose the right node

Tractor deliberately has a small, fixed node vocabulary.

| Type              | Use it for                                          | How it routes                                                     |
| ----------------- | --------------------------------------------------- | ----------------------------------------------------------------- |
| `codergen`        | Planning, implementation, review, or any LLM task   | The agent chooses from its `edges`.                               |
| `tool`            | Tests, builds, scripts, and other mechanical checks | Exit code selects `on_success` or `on_error`.                     |
| `parallel`        | Running independent alternatives concurrently       | `branches` reference authored roots or declare Codergen branches. |
| `parallel.fan_in` | Comparing and consolidating parallel results        | The fan-in agent reads branch evidence, artifacts, and routes.    |
| `supervisor`      | Periodically observing and coaching active nodes    | It returns `ok` or steers a node; it never joins the walk.        |

Use `tool` when a process can decide correctly from an exit code. Use `codergen` when the decision requires judgment. That distinction saves tokens and makes deterministic gates genuinely deterministic.

## Put routing on the chooser

A `codergen` or `parallel.fan_in` node carries an `edges` array. If there is one possible successor, the edge can be unconditional. At a branch point, every edge needs a concise, mutually useful prose condition.

```yaml
- id: review
  type: codergen
  prompt: Review the implementation against the request and the test evidence.
  edges:
    - to: success
      condition: The change is complete, scoped, and demonstrated.
    - to: implement
      condition: A concrete defect remains and can be repaired.
    - to: failure
      condition: The goal cannot be met safely without user input.
```

Conditions are instructions to the choosing agent, not a miniature expression language. Tractor constrains the answer to an offered target, records the response, and then follows that target.

Tool nodes are different because the shell has already made the decision:

```yaml
- id: verify
  type: tool
  tool_command: vp run verify
  timeout: 10m
  on_success: success
  on_error: repair
```

If `on_error` is absent, a nonzero exit fails the run. That is a good default for assertion-style checks.

## Control sessions and retries

LLM nodes can set `llm_provider`, `llm_model`, `reasoning_effort`, `timeout`, `max_retries`, `fidelity`, and `thread_id`. Put shared values under `defaults`, then override only where the stage has a real reason to differ.

- `fidelity: full` reuses the native harness session and preserves its conversation.
- `fidelity: compacted` reuses the session after native compaction.
- `fidelity: none` starts without prior conversational context; the workspace and run evidence still exist.
- `max_retries` counts additional attempts after the first failure. It never retries a routing choice.
- `max_visits` limits how often the graph may dispatch a node, which is how you bound loops.

Keep prompts responsible for the work, not for re-explaining the entire pipeline. Tractor already supplies the available successors as a strict choice schema.

## Fan out, then converge

Parallel nodes support two branch forms. A string names an authored branch root, preserving the original multi-node branch model. A structured branch declares one synthesized Codergen stage, its required artifacts, and an optional `codergen` override. The synthesized stage inherits the parallel node's prompt, model, provider, reasoning effort, retry, fidelity, thread, timeout, label, visit, and routing fields; only fields present in `codergen` replace those values.

The default `workspace` is `isolated`. Tractor freezes the parent Git repository, creates a separate worktree for each branch, and collects every declared regular file or directory into durable fan-out stage evidence before deleting the branch worktrees. `branches.json` records each declared path, collected path, source path, outcome, stage directory, and run-log segment. The fan-in receives those collected paths directly.

```yaml
- id: compare
  type: parallel
  workspace: isolated
  max_parallel: 2
  prompt: Review the change and write the branch's declared report.
  llm_provider: openai
  llm_model: gpt-5.6-sol
  reasoning_effort: low
  edges:
    - to: choose
  branches:
    - id: openai_review
      artifacts: [reports/openai.md]
      codergen:
        prompt: Write the OpenAI review to reports/openai.md.
    - id: claude_review
      artifacts: [reports/claude.md]
      codergen:
        prompt: Write the Claude review to reports/claude.md.
        llm_provider: anthropic
        llm_model: claude-sonnet-4-5
        reasoning_effort: medium

- id: choose
  type: parallel.fan_in
  prompt: Read both collected reports, reconcile their findings, and verify the result here.
  edges:
    - to: success
```

Set `workspace: shared` only when branches intentionally coordinate through one directory. Shared branches do not require a Git repository, and `branches.json` exposes each declared artifact at its live workspace path. Tractor provides no write-conflict protection in this mode, so give concurrent branches distinct outputs unless the agents explicitly coordinate a shared edit.

Legacy string branches remain useful when each alternative needs several authored nodes:

```yaml
- id: explore
  type: parallel
  branches: [minimal_fix, structural_fix]
```

Every branch form must converge on one `parallel.fan_in`. Legacy branch node sets must be disjoint until that join, nested parallel nodes are not supported, and a failed fan-out replays as a unit. Use parallelism for genuinely independent alternatives—not merely to make a linear workflow look sophisticated.

## Add live supervision sparingly

A supervisor observes declared nodes outside the main walk. At its interval, it receives an engine-built digest of active work and can either approve the situation or send targeted steering.

```yaml
- id: coach
  type: supervisor
  prompt: Keep implementation aligned with the requested scope. Steer only on a material divergence.
  supervises: [implement, review]
  interval: 45s
  llm_provider: anthropic
  llm_model: fable
  reasoning_effort: low
```

Supervisors are advisory. They do not route, block, or decide success. Prefer clear worker prompts and deterministic checks first; add supervision when useful mid-turn correction is worth the extra model calls.

## Validate before spending tokens

Validation checks the generated closed schema and Tractor's graph-level lint rules before a run starts. It catches unknown fields, invalid targets, ambiguous branches, unsafe parallel topology, session collisions, supervision cycles, and other structural mistakes.

```sh
tractor validate pipeline.yaml
tractor print-schema > tractor-pipeline.schema.json
```

Inside Codex, ask Tractor for the current pipeline schema only when authoring or changing a graph. The plugin defers the large schema so ordinary run-management requests stay small.

## Authoring checklist

1. Write one sentence for `goal` that is observable in the workspace.
2. Start with a linear graph and explicit proof at the end.
3. Use tool nodes for facts a command can decide.
4. Put branch conditions on the agent that has enough context to choose.
5. Bound intentional loops with `max_visits`.
6. Choose isolated or shared branch workspaces deliberately, declare every structured-branch artifact, and converge at one fan-in.
7. Validate the file before starting the run.

The complete field contract and execution semantics live in the normative [Tractor specification](https://github.com/tylergannon/tractor/blob/main/docs/spec.md). Runnable steering, YAML, parallel, and supervision pipelines live in the repository's [examples](https://github.com/tylergannon/tractor/tree/main/examples).
