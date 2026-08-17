# Tractor MCP Plugin — Host Validation Proof

**Date:** 2026-08-17
**Host:** Claude Code (Claude Agent SDK), not Codex. The task named a "Codex tool registry";
this run was executed by a Claude Code session, so registry observations below describe that
host's deferred-tool mechanism. Everything else is verbatim tool output.
**Transport:** `tractor` MCP server over stdio (plugin-installed).
**Method:** MCP tool calls only. No `tractor` binary was invoked via shell. Shell was used
solely to read run artifacts off disk after the run reached a terminal state.

## 1. Tool registry: deferred, not preloaded

The four Tractor tools were **not** present in the initial tool schema set. They appeared only
as names in a deferred-tool `system-reminder`, alongside the other Tractor tools
(`steer_run`, `stop_run`). Calling them without loading schemas would have failed with
`InputValidationError`.

Schemas were loaded explicitly with:

```
ToolSearch(query="select:mcp__tractor__get_pipeline_schema,mcp__tractor__validate_pipeline,mcp__tractor__start_run,mcp__tractor__get_run_status")
```

All four resolved and returned full JSONSchema definitions, after which they were directly
callable. **Verdict: deferred — yes; resolvable and callable after ToolSearch — yes.**

Server-level instructions were delivered to the host as MCP server instructions:

> Pipeline definitions are files. Read the current schema only when authoring or changing a
> pipeline, validate before starting, and use the returned run_id for later operations. Runs
> belong to this stdio session and are stopped when it closes.

## 2. Target

- **workdir:** `/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/live-workspace`
- **pipeline_path:** `pipeline.json` (relative, resolved from workdir)

```json
{
  "name": "mcp-live-proof",
  "nodes": [
    { "id": "start", "type": "start", "edges": [{ "to": "done" }] },
    { "id": "done", "type": "exit" }
  ]
}
```

## 3. `mcp__tractor__get_pipeline_schema`

Called with no arguments (`additionalProperties: false`, no properties). Returned a single
`schema` field holding the generated pipeline JSON Schema as a string.

Observed structure:

- Root: `Graph is a complete pipeline definition.` — `required: ["nodes"]`, `additionalProperties: false`
- Top-level properties: `name`, `goal`, `defaults`, `nodes`
- `defaults`: `fidelity`, `llm_model`, `llm_provider`, `max_retries`,
  `reasoning_effort` (enum `low|medium|high`), `timeout` (pattern `^[0-9]+(ms|s|m|h|d)$`)
- `nodes[]` is an `anyOf` over 7 node variants:
  1. `start` (const)
  2. `exit` (const)
  3. `codergen` (const) — `prompt`, `thread_id`, `llm_*`, `reasoning_effort`, `max_retries`, `timeout`
  4. `parallel` (const) — `max_parallel`
  5. `parallel.fan_in` (const) — LLM fields, evaluates parallel branch evidence
  6. `tool` (const) — `required: ["type","id","tool_command"]`, plus `on_fail`, `timeout`
  7. Custom node — `type` constrained by `not/enum` excluding the six built-ins; carries an
     open `custom` object of `nonNullJSON`
- Node `id` pattern: `^[A-Za-z_][A-Za-z0-9_]*$`
- `Edge` is owned by its origin node: `{ to (required), condition }`
- `$defs.nonNullJSON` is a recursive non-null JSON value union

The `custom` description confirms the deterministic schema-normalization step opens the
generated placeholder because go-gen-jsonschema has no map type.

**Verdict: PASS** — live schema served from Tractor's graph type.

## 4. `mcp__tractor__validate_pipeline`

Request:
```json
{ "pipeline_path": "pipeline.json",
  "workdir": ".../live-workspace" }
```

Response (verbatim):
```json
{"valid":true,"warnings":[]}
```

**Verdict: PASS** — parser and runtime lint rules both clean; relative path resolution from
`workdir` confirmed working.

## 5. `mcp__tractor__start_run`

Request:
```json
{ "pipeline_path": "pipeline.json",
  "workdir": ".../live-workspace" }
```

Response (verbatim):
```json
{"run_id":"62c1fb6f98f2a20c9da25e8fcdb11ba4","pid":43884,"status":"RUNNING",
 "pipeline_path":".../live-workspace/pipeline.json",
 "workdir":".../live-workspace",
 "logs_root":".../live-workspace/.tractor/runs/62c1fb6f98f2a20c9da25e8fcdb11ba4",
 "warnings":[],
 "stdout_path":".../62c1fb6f98f2a20c9da25e8fcdb11ba4/mcp-stdout.log",
 "stderr_path":".../62c1fb6f98f2a20c9da25e8fcdb11ba4/mcp-stderr.log"}
```

Returned asynchronously with `status: RUNNING` and a live `pid`, as documented. Default
`logs_root` resolved to `.tractor/runs/<run-id>` under workdir.

**Verdict: PASS**

## 6. `mcp__tractor__get_run_status` — poll to terminal

Request: `{ "run_id": "62c1fb6f98f2a20c9da25e8fcdb11ba4" }`

Response (verbatim):
```json
{"run_id":"62c1fb6f98f2a20c9da25e8fcdb11ba4","pid":43884,"status":"COMPLETED",
 "pipeline_path":".../live-workspace/pipeline.json",
 "workdir":".../live-workspace",
 "logs_root":".../.tractor/runs/62c1fb6f98f2a20c9da25e8fcdb11ba4",
 "started_at":"2026-08-17T23:49:00.29992Z",
 "finished_at":"2026-08-17T23:49:00.325551Z",
 "exit_code":0,
 "current_node":"done",
 "last_stage":"start",
 "last_response":"run started"}
```

Terminal state (`COMPLETED`, `exit_code: 0`) was reached on the **first** poll — the pipeline
is a two-node start→exit graph and finished in ~25.6ms wall clock, so no polling loop was
needed. Both process-level fields (`pid`, `exit_code`) and checkpoint-level fields
(`current_node`, `last_stage`, `last_response`) were populated, matching the tool's stated
contract of reading process *and* checkpoint status.

**Verdict: PASS**

## 7. Checkpoint inspection

`.tractor/runs/62c1fb6f98f2a20c9da25e8fcdb11ba4/` contained:
`checkpoint.json`, `manifest.json`, `timeline.jsonl`, `mcp-stdout.log`, `mcp-stderr.log`,
`stages/`, `events/`.

### checkpoint.json
```json
{
  "timestamp": "2026-08-17T23:49:00.323748Z",
  "current_node": "done",
  "next_node": "",
  "completed_nodes": ["start"],
  "node_visits": { "start": 1 },
  "node_attempts": { "start": 1 },
  "seq": 1,
  "retry_visit": false,
  "last_stage": "start",
  "last_response": "run started",
  "sessions": {}
}
```

Consistent with the graph: `start` executed once and completed; `done` is an exit node so it
is `current_node` with an empty `next_node` and is not itself a completed stage. `sessions` is
empty — no LLM nodes in this pipeline.

### manifest.json
```json
{
  "id": "01174989732ef1152285d742b817497d",
  "name": "mcp-live-proof",
  "goal": "",
  "workdir": ".../live-workspace",
  "started_at": "2026-08-17T23:49:00.322061Z",
  "control_socket": "/var/folders/lt/.../T/tractor-control-950301844/control.sock"
}
```

Note the pipeline `id` (`0117…497d`) is distinct from the MCP `run_id` (`62c1…1ba4`); the MCP
layer's run identifier is the one accepted by `get_run_status`. A control socket was allocated
(the channel `steer_run` / `stop_run` would use).

### timeline.jsonl
```
{"id":"01174989732ef1152285d742b817497d","name":"mcp-live-proof","ts":"...322405Z","type":"PipelineStarted"}
{"node_id":"start","ts":"...322881Z","type":"CheckpointSaved"}
{"index":1,"name":"start","ts":"...323005Z","type":"StageStarted"}
{"duration":"347.292µs","index":1,"name":"start","next":"done","ts":"...323353Z","type":"StageCompleted"}
{"node_id":"start","ts":"...323701Z","type":"CheckpointSaved"}
{"node_id":"done","ts":"...324002Z","type":"CheckpointSaved"}
{"duration":"1.668708ms","ts":"...324072Z","type":"PipelineCompleted"}
```

Full lifecycle present, checkpoints saved before and after the stage, and edge traversal
recorded as `next: "done"` — the edge declared in the pipeline.

### stages/
`stages/000001-start/outcome.json`:
```json
{ "notes": "run started" }
```
Matches `last_response` surfaced by `get_run_status`. `events/` was empty (no LLM/tool events).

- `mcp-stdout.log`: `COMPLETED`
- `mcp-stderr.log`: empty (0 bytes)

## 8. Final result

**PASS.** All four Tractor MCP tools were exercised end-to-end over the plugin's stdio
transport with no shell invocation of Tractor itself.

| Check | Result |
|---|---|
| Tools deferred in host registry | Yes — names only, schemas via `ToolSearch` |
| Schemas load and tools become callable | Yes, all 4 |
| `get_pipeline_schema` returns live graph schema | PASS — 7 node variants, recursive `$defs` |
| `validate_pipeline` | PASS — `valid: true`, no warnings |
| `start_run` async with `run_id` + `pid` | PASS — `status: RUNNING` |
| Poll to terminal | PASS — `COMPLETED`, `exit_code: 0`, first poll |
| Checkpoint written and internally consistent | PASS |
| Timeline / stage artifacts | PASS — full lifecycle, stage outcome matches status |
| stderr clean | PASS — 0 bytes |

Caveat on coverage: this pipeline is a minimal `start → exit` graph. It proves transport,
schema service, validation, async run lifecycle, status reporting, and checkpoint durability.
It does **not** exercise `codergen`/`tool`/`parallel` node execution, retries, resume from
checkpoint, or the `steer_run` / `stop_run` control-socket path — those remain unvalidated.
