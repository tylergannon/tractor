# Tractor plugin proof through Codex

Date: 2026-08-17

Codex version: `codex-cli 0.147.0`

Codex task: `01a0124d-94de-7460-92b0-00902f36b780`

Installed plugin: `tractor@personal`, version
`0.1.0+codex.20260818003702`, enabled, with cached package at
`/Users/tyler/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818003702`.

## Command

```sh
codex exec --ephemeral --json \
  "Use the installed Tractor MCP server only; do not use shell or Go tools. ..."
```

The task was explicitly prohibited from launching Tractor or Go through the
shell. It emitted actual `mcp_tool_call` events with server `tractor` for
`validate_pipeline`, `start_run`, and `get_run_status`.

## Observed MCP results

`validate_pipeline`:

```json
{"valid":true,"warnings":[]}
```

`start_run`:

```json
{
  "run_id": "ee1f5a9764b98151e4b78e32e08b2d57",
  "pid": 77078,
  "status": "RUNNING",
  "logs_root": "/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/live-workspace/.tractor/runs/ee1f5a9764b98151e4b78e32e08b2d57"
}
```

`get_run_status`:

```json
{
  "run_id": "ee1f5a9764b98151e4b78e32e08b2d57",
  "pid": 77078,
  "status": "COMPLETED",
  "exit_code": 0,
  "current_node": "done",
  "last_stage": "start",
  "last_response": "run started"
}
```

The task's final result was:

```json
{
  "valid": true,
  "run_id": "ee1f5a9764b98151e4b78e32e08b2d57",
  "pid": 77078,
  "status": "COMPLETED",
  "exit_code": 0,
  "current_node": "done",
  "next_node": null
}
```

The resulting checkpoint exists and has SHA-256
`b22ed7089e7ec40a24a2bbb62bbae1dc02021fd178a2aa8d568b3cc4f32ad792`.
It records `current_node: "done"`, an empty `next_node`, and one visit and
attempt for `start`.

## Verdict

Codex itself loaded the installed plugin's Tractor tools on demand, invoked the
stdio MCP server, validated a pipeline, started a child process, observed its
terminal status, and produced the normal durable run artifacts.
