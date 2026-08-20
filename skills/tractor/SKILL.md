---
name: tractor
description: Run coding agents as detached pipelines with Tractor — loop until a check actually passes, fan out across Claude/Codex/Gemini, or have a second model review the work. Use when the user says "don't stop until the tests pass", "loop on this until it works", "keep working while I'm gone", "keep going after I close my laptop", "run this in the background", "try a couple of approaches in parallel", "have another model check this", "get a second opinion from Codex or Gemini" — or asks to start, check on, steer, or stop a Tractor run. Starts from copy-and-run examples; no graph authoring needed. Do not use for ordinary single-agent work that finishes in this session.
---

# Tractor

Tractor runs pipelines of real coding agents — the Claude Code, Codex, and
Gemini CLIs with native sessions — as detached processes that outlive this
session. A pipeline is a small YAML or JSON file: start from an example and
change two strings; you rarely author a graph.

MCP tools: `start_run`, `get_run_status`, `steer_run`, `stop_run`,
`get_pipeline_schema`. They take file paths, not inline graphs.

## The whole idea

```yaml
goal: Implement the TODO in cmd/server/routes.go and make the tests pass
start: implement
nodes:
  - id: implement
    type: codergen          # an agent turn
    max_visits: 5           # the budget; nothing else stops a loop
    prompt: Work toward the goal. Make the smallest change that could satisfy the check.
    edges:
      - to: check
  - id: check
    type: tool              # a command decides what "done" means
    tool_command: go test ./...
    on_success: success
    on_error: implement     # failure routes back — that's the loop
```

An agent works, a command decides, failure routes back. Fan-out shapes add
`parallel` branches (one per provider) and a `parallel.fan_in` node to judge.

## Pick the user's moment

| The moment | Example |
|---|---|
| "Don't stop until it actually works" | `examples/fix-until-green.yaml` |
| "Have another model check this" | `examples/critique-circle.yaml` |
| "Try a couple of approaches in parallel" | `examples/bake-off.yaml` |
| "Keep working on this after I leave" | `examples/milestone-loop.yaml` |

They ship beside this file, mirroring `examples/loops/` in the Tractor repo;
if neither is at hand, the pipeline above is a complete start.

1. Copy the chosen example into the target project (a git repo).
2. Replace its placeholders with the user's goal; command-gated loops also
   need the check node's `tool_command`. Read the file — examples differ.
3. `start_run` with the copied file as `pipeline_path` and the repo as
   `workdir` — it lints the graph first and refuses to launch a broken one,
   so fix what it rejects and call it again. A fresh run needs an empty or
   absent `logs_root`; `resume: true` continues an existing run directory.
4. Report the returned `run_id`, process ID, and log paths. Run state lives
   under `~/.local/state/tractor/mcp-runs` and outlives this session and any
   MCP restart — a later session reconnects with the same `run_id`.

## Make "done" honest

The tool node is the only thing that decides. Point it at the closest
observable proof of the user's claim — run the app, curl the endpoint, assert
on the artifact. Tests and linters are worth requiring, but prove the claim
only when they exercise that behavior.

## While it runs

`get_run_status` returns `current_node`, `last_stage`, and `last_response`
— enough for a progress update in the user's terms. `steer_run` reaches the
active steerable turn; when none is active it returns `accepted: false`, so
retry after the next status check rather than reporting a failure. Steer only
when new authoritative information arrives or the run is leaving scope; a
complete goal up front beats frequent correction. `stop_run` asks a run to
stop; calling it again after the graceful window forces it.

## When a loop misbehaves

| Symptom | Fix |
|---|---|
| Runs forever | `max_visits` on the looping node. |
| Says it's done when it isn't | The command is checking the wrong thing — check the behavior itself. |
| Re-derives the same dead end every lap | Have the prompt keep a short notes file: append what the next attempt should do differently, read it first. |
| Reviewer rubber-stamps | Fresh session (`fidelity: none`), whole target every round, never say what to find. A different provider makes the independence real. |
| Fan-in averages instead of deciding | Tell it to inspect and run the work itself and adjudicate each finding on evidence — never count votes or concatenate reports. |
| Guesses at a decision that wasn't its to make | Give it a door: an edge conditioned on "this decision isn't mine" leading to a node that asks the user or writes a report and routes to `failure`. Agents improvise when forward is the only route offered. |
| Builds everything, nothing runs until the end | Steer the chooser to vertical slices: a step is done when you can run something that proves it. Stack-order plans are the model's default tic; say no to them in the prompt. |

## Authoring beyond the examples

`get_pipeline_schema` returns the current graph schema, and `start_run`'s
lint diagnostics teach as they reject. Keep each prompt to the decision its
node owns — the engine already supplies the goal, the workspace, and the
routing choices.
