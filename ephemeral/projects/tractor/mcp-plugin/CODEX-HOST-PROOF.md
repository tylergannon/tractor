# Tractor plugin proof through Codex

Date: 2026-08-17

Codex version: `codex-cli 0.147.0`

Codex task: `01a01242-fcd5-7f90-8751-6579375ae833`

Installed plugin: `tractor@personal`, version
`0.1.0+codex.20260818002540`, enabled, with cached package at
`/Users/tyler/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818002540`.

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
  "run_id": "20f379f193634645b3011b1373eeb549",
  "pid": 67704,
  "status": "RUNNING",
  "logs_root": "/Users/tyler/src/.worktrees/tractor/mcp-plugin/ephemeral/projects/tractor/mcp-plugin/live-workspace/.tractor/runs/20f379f193634645b3011b1373eeb549"
}
```

`get_run_status`:

```json
{
  "run_id": "20f379f193634645b3011b1373eeb549",
  "pid": 67704,
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
  "run_id": "20f379f193634645b3011b1373eeb549",
  "pid": 67704,
  "status": "COMPLETED",
  "exit_code": 0,
  "current_node": "done",
  "next_node": null
}
```

The resulting checkpoint exists and has SHA-256
`ac1b8ceb156357bc6d89451a71921ab82a242cb21942fac0393f06144b217b95`.
It records `current_node: "done"`, an empty `next_node`, and one visit and
attempt for `start`.

## Verdict

Codex itself loaded the installed plugin's Tractor tools on demand, invoked the
stdio MCP server, validated a pipeline, started a child process, observed its
terminal status, and produced the normal durable run artifacts.
