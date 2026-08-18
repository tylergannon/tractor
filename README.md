# Tractor

Tractor is the reference Go implementation of its Attractor variant. The
[Tractor Attractor specification](docs/spec.md) is the sole normative and
authoritative definition of that variant; where Tractor's code, schemas,
examples, or other documentation disagree with it, the specification governs.
The substantive specification moved here from the now-archived
`tylergannon/attractor` repository at revision
`0aca8b748e6ecc23446fc690d2b66690b77fe0d3`. Before Tractor added its explicit
authority notice, the imported document matched that source byte-for-byte.

The archived project's [north star](https://github.com/tylergannon/attractor/blob/0aca8b748e6ecc23446fc690d2b66690b77fe0d3/ephemeral/projects/spec-rebuild/north-star.md)
captures the thrust of this variant: replace DOT with a closed typed JSON graph,
put routing on the node whose occupant makes the choice, run work through
existing Codex, Claude, and Antigravity harnesses, and make steering, context fidelity,
uniform checkpointing, isolated parallel worktrees, and live advisory
supervision first-class. It also applies a strict cost razor to new language and
protocol surface, keeps human interaction and extensions in ordinary authored
nodes or build-time code, and reserves usage-aware routing as future work.

The archived implementation also contains ideas that are intentionally not
part of Tractor today. See the
[Attractor archive inventory](docs/attractor-archive-inventory.md) for a
source-backed account of the useful gaps, deliberate exclusions, and their
tradeoffs.

Tractor has five documented implementation choices around that contract:

- Pipeline files may be JSON, YAML, or YML; inline documents use `--json` or
  `--yaml`. YAML is a Tractor authoring extension decoded through the same
  generated, closed schema as JSON. JSON field names and semantics remain
  canonical.
- Tractor's backend interface includes a synchronous binding-open callback.
  The engine uses it to save a newly opened supervisor session before its turn
  is dispatched, implementing the checkpoint guarantee without polling backend
  state.
- A supervisor briefing is idempotent, at-least-once input across a crash. A
  successful supervisor turn records its exact session binding in
  `briefed.json`; the same binding suppresses a resend. Missing, changed, or
  inconclusive completion evidence resends the briefing. This accepts the
  unavoidable duplicate-delivery window rather than risking a silently
  unbriefed resumed session. Advisory file/render failures are recorded in the
  supervisor's `errors.jsonl` because the upstream spec requires them to be
  recorded but does not assign a wire artifact.
- Codex's native strict-output API requires every declared root property to be
  structurally required. The Codex adapter therefore presents optional root
  properties as required nullable fields, removes returned nulls for fields
  that were optional, and validates the result against the caller's unchanged
  schema. Claude receives the caller schema unchanged.
- Gemini models route through the authenticated `agy` CLI. Tractor validates
  agy's structured output locally and allows one repair turn; `agy` print mode
  has no live steering channel, so steering is a documented no-op for this
  adapter while context compaction remains service-managed.
Runnable, self-verifying workflows for external steering, parallel
fan-out/fan-in, YAML input, and live supervision are in
[`examples/`](examples/README.md).

## Codex plugin and MCP server

This repository is also a Codex plugin. The normal installer installs the
latest Tractor binary with Go and then cleanly installs or replaces the Codex
plugin:

```sh
curl -fsSL https://raw.githubusercontent.com/tylergannon/tractor/main/scripts/install.sh | sh
```

To install or update explicitly, run the same two operations directly:

```sh
go install github.com/tylergannon/tractor/cmd/tractor@latest
tractor plugin install
```

The installer script resolves `GOBIN` or `GOPATH/bin` itself. For the explicit
form, that directory must already be on `PATH`. `tractor plugin install`
refreshes the marketplace, removes the old plugin and its Codex cache, installs
the current plugin, retires MCP servers registered by versions with
detached-run support, and removes the obsolete source-building wrapper cache.
It never stops detached Tractor runs. Idle MCP servers from older versions are
stopped by PID only; a legacy MCP server with a child run is preserved so that
run keeps both its process and its existing control owner.

The Go binary directory must be on the `PATH` inherited by Codex. Start a new
Codex task after installation or update so the plugin launches the installed
binary with `tractor mcp`. See
[`llms.txt`](llms.txt) for the agent-oriented installation and MCP usage
reference.

The server exposes deferred tools to validate, start, monitor, steer, and stop
pipeline runs. Their input schemas contain only operational arguments such as
pipeline and workspace paths; they do not embed Tractor's graph language.
`get_pipeline_schema` returns `graph.Graph{}.Schema()` only when called, while
validation and execution use Tractor's existing parser and runtime validator.
Consequently the MCP surface and graph language are compiled into the same
Tractor binary and cannot acquire a separately maintained schema.

Run the server directly with `tractor mcp`. MCP clients should communicate over
stdin and stdout; diagnostics and run output are written elsewhere so they
cannot corrupt the protocol stream.
Each `start_run` call launches a new detached Tractor runner and atomically
records its state under `~/.local/state/tractor/mcp-runs`. Closing or replacing
the MCP server does not stop the run. A later MCP server can use the same
`run_id` to inspect, steer, or stop it. `stop_run` signals only the process group
whose persisted command identity matches that run; repeating the call after the
graceful window escalates to a forced stop.

## License

Tractor is available under the [MIT License](LICENSE).
