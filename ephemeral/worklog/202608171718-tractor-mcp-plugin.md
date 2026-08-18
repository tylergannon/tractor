correction: Before reasoning from public product docs about Codex tool loading, inspect the live deferred tool registry when the current Codex task exposes it.
correction: Use github.com/mark3labs/mcp-go for Tractor's MCP implementation; do not substitute another SDK or hand-roll protocol or transport handling.
decision: Tractor MCP tools accept pipeline paths instead of embedding the graph language in tool input schemas; parsing, validation, and schema output call the existing graph package so language changes cannot drift from the MCP surface.
correction: Cache launcher binaries by the resolved plugin root so an installed, version-pinned snapshot cannot execute a concurrently built binary from a development checkout.
correction: Re-check run completion immediately before a raw process-group kill so a reaped PID cannot be signaled after the graceful stop window.
correction: A Tractor run can contain tool commands in their own process groups; forced MCP shutdown must discover and terminate detached descendant groups before considering the session closed.
decision: Use a content-addressed launcher executable so concurrent launches and later development rebuilds cannot change the binary used by an existing MCP server or its child runs.
