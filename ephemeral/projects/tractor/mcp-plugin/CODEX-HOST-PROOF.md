# Tractor plugin proof through Codex

Date: 2026-08-17

Codex version: `codex-cli 0.147.0`

Codex task: `01a0122e-da9c-7160-b9ad-aac648b05343`

Installed plugin: `tractor@personal`, version
`0.1.0+codex.20260818000356`, enabled, with cached package at
`/Users/tyler/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818000356`.

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
`validate_pipeline`, `start_run`, and `get_run_status`.

## Observed MCP results

`validate_pipeline`:

```json
{"valid":true,"warnings":[]}
```

`start_run`:

```json
{
  "run_id": "c4f5ce11f22c231acf8bf2fee011458b",
  "pid": 52657,
  "status": "RUNNING",
  "logs_root": "/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run-v2"
}
```

`get_run_status`:

```json
{
  "run_id": "c4f5ce11f22c231acf8bf2fee011458b",
  "pid": 52657,
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
  "loading_deferred": true,
  "validation_result": {
    "valid": true,
    "warnings": []
  },
  "run_id": "c4f5ce11f22c231acf8bf2fee011458b",
  "final_status": "COMPLETED",
  "exit_code": 0,
  "current_node": "done",
  "logs_root": "/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run-v2"
}
```

The resulting checkpoint exists and has SHA-256
`45c15e143c96fac3308c83ee00f40f230b8b0a8d001e22777f18e0f1ef022c57`.
It records `current_node: "done"`, an empty `next_node`, and one visit and
attempt for `start`.

## Verdict

Codex itself loaded the installed plugin's Tractor tools on demand, invoked the
stdio MCP server, validated a pipeline, started a child process, observed its
terminal status, and produced the normal durable run artifacts.
