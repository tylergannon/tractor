correction: Before reasoning from public product docs about Codex tool loading, inspect the live deferred tool registry when the current Codex task exposes it.
correction: Use github.com/mark3labs/mcp-go for Tractor's MCP implementation; do not substitute another SDK or hand-roll protocol or transport handling.
decision: Tractor MCP tools accept pipeline paths instead of embedding the graph language in tool input schemas; parsing, validation, and schema output call the existing graph package so language changes cannot drift from the MCP surface.
