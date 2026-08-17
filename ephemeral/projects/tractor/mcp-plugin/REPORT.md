# Tractor MCP plugin live proof

Date: 2026-08-17

## Claim

The committed Codex plugin configuration launches Tractor's real stdio MCP
entry point. The entry point advertises only compact operational schemas,
marks every tool for deferred loading, serves the graph schema directly from
`graph.Graph{}.Schema()`, and starts a Tractor process that produces the normal
run ledger and checkpoint.

## Command

From the repository root:

```sh
go run ./ephemeral/projects/tractor/mcp-plugin/smoke.go
```

The smoke client reads `.mcp.json` instead of supplying its own server command.
It uses `github.com/mark3labs/mcp-go/client` to initialize the stdio session,
list tools, call `get_pipeline_schema`, validate `live-workspace/pipeline.json`,
start the pipeline, poll its status, and load its checkpoint.

## Observed result

```json
{
  "plugin_command": "go [run ./cmd/tractor mcp]",
  "server_name": "tractor",
  "server_version": "0.1.0",
  "tools": [
    "get_pipeline_schema",
    "get_run_status",
    "start_run",
    "steer_run",
    "stop_run",
    "validate_pipeline"
  ],
  "all_deferred": true,
  "start_schema_bytes": 554,
  "graph_schema_sha256": "ee9310f1743fbe4cf2bb99565529379a05318e95b2e95198156040b47307b350",
  "schema_matches_graph_type": true,
  "run_id": "7a93ba69ad835c253eb25766365c151d",
  "run_pid": 36389,
  "run_status": "COMPLETED",
  "exit_code": 0,
  "current_node": "done",
  "next_node": "",
  "checkpoint_exists": true
}
```

The child process also wrote `COMPLETED` to `mcp-stdout.log`, nothing to
`mcp-stderr.log`, and a `PipelineCompleted` event to `timeline.jsonl`. Its
checkpoint SHA-256 is
`b93a76b659fd9b100df8ca5c9ed0b267d66e40035f9a89a40c30bd08cb000eb9`.

## Package validation

```text
$ python3 /Users/tyler/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py .
Plugin validation passed: /Users/tyler/src/.worktrees/tractor/mcp-plugin
```

The plugin descriptor SHA-256 is
`0a1a3a74491049702bfd7ca189108117ed494608ec5e28eef03b091496f3da9d` and
the MCP configuration SHA-256 is
`c6854608a298fd169f9e467b5935acfa2a59bfeef879e05815668416bc161c9d`.
