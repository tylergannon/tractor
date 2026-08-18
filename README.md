# Tractor

Tractor is a Go module. Pipeline files may be JSON, YAML, or YML; inline
documents use `--json` or `--yaml`.

Runnable, self-verifying workflows for external steering and parallel
fan-out/fan-in are in [`examples/`](examples/README.md).

## Codex plugin and MCP server

This repository is also a Codex plugin. Its `.mcp.json` launches the stdio
server through:

```sh
./scripts/tractor-mcp
```

The source-distributed plugin therefore requires Go 1.26 or newer. The first
launch may download modules; subsequent launches reuse Go's build cache. The
launcher builds the plugin snapshot into an immutable, build-ID-addressed cache
entry and then replaces itself with `tractor mcp`, so MCP stdin and host signals
reach the server directly. Building from the snapshot is intentional: the MCP
server and graph language are always compiled from the same revision.

The server exposes deferred tools to validate, start, monitor, steer, and stop
pipeline runs. Their input schemas contain only operational arguments such as
pipeline and workspace paths; they do not embed Tractor's graph language.
`get_pipeline_schema` returns `graph.Graph{}.Schema()` only when called, while
validation and execution use Tractor's existing parser and runtime validator.
Consequently the MCP surface and graph language are built from the same source
revision and cannot acquire a separately maintained schema.

Run the server directly with `tractor mcp` when Tractor is installed as a
binary. MCP clients should communicate over stdin and stdout; diagnostics and
run output are written elsewhere so they cannot corrupt the protocol stream.
Runs belong to the stdio server session. When that session closes, the server
first requests a cooperative stop and then terminates any remaining run process
tree within the MCP host's shutdown deadline, preventing an unloaded plugin
from leaving unsupervised work in the background.
