# Tractor plugin proof through Codex

Date: 2026-08-17

Codex version: `codex-cli 0.147.0`

Codex task: `01a01222-7d00-7c72-af6e-53386f423ddb`

Installed plugin: `tractor@personal`, version `0.1.0`, enabled, with cached
package at `/Users/tyler/.codex/plugins/cache/personal/tractor/0.1.0`.

## Command

```sh
codex exec --ephemeral --json -s danger-full-access \
  -C /Users/tyler/src/.worktrees/tractor/mcp-plugin \
  "Use only the installed Tractor MCP tools for Tractor operations ..."
```

The task was explicitly prohibited from launching Tractor or Go through the
shell. Its event stream said, before the first call:

```text
The Tractor tools were deferred and are now loaded.
```

It then emitted actual `mcp_tool_call` events with server `tractor` for
`get_pipeline_schema`, `validate_pipeline`, `start_run`, and
`get_run_status`.

## Observed MCP results

`validate_pipeline`:

```json
{"valid":true,"warnings":[]}
```

`start_run`:

```json
{
  "run_id": "4891ad5c0e4398aaa579f851a505fb2b",
  "pid": 44593,
  "status": "RUNNING",
  "logs_root": "/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run"
}
```

`get_run_status`:

```json
{
  "run_id": "4891ad5c0e4398aaa579f851a505fb2b",
  "pid": 44593,
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
  "tool_loading_deferred": true,
  "schema_title": null,
  "validation_result": {
    "valid": true,
    "warnings": []
  },
  "run_id": "4891ad5c0e4398aaa579f851a505fb2b",
  "final_status": "COMPLETED",
  "exit_code": 0,
  "current_node": "done",
  "logs_root": "/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run"
}
```

The resulting checkpoint exists and has SHA-256
`7d0fd796093a6007dffab2d4b3e2223fd6fcfb52c51a7554b971bd2a79a7ea88`.
It records `current_node: "done"`, an empty `next_node`, and one visit and
attempt for `start`.

## Verdict

Codex itself loaded the installed plugin's Tractor tools on demand, invoked the
stdio MCP server, validated a pipeline, started a child process, observed its
terminal status, and produced the normal durable run artifacts.
