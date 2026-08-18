# Tractor

Tractor is the Go implementation of the normative [Attractor
specification](docs/spec.md). That document is copied byte-for-byte from
upstream revision `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`.

Tractor has four documented implementation choices around that contract:

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
Runnable, self-verifying workflows for external steering, parallel
fan-out/fan-in, YAML input, and live supervision are in
[`examples/`](examples/README.md).

## Codex plugin and MCP server

This repository is also a Codex plugin. Its `.mcp.json` launches the stdio
server through:

```sh
./scripts/tractor-mcp
```

Install it from the repository marketplace:

```sh
codex plugin marketplace add tylergannon/tractor
codex plugin add tractor@tractor
```

Start a new Codex task after installation. See [`llms.txt`](llms.txt) for the
agent-oriented installation and MCP usage reference.

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
requests a cooperative stop and then force-stops the run's process group within
its bounded shutdown window. Tool commands receive the cooperative stop through
Tractor's runtime; processes that deliberately detach from Tractor's process
groups are outside this guarantee.

## License

Tractor is available under the [MIT License](LICENSE).
