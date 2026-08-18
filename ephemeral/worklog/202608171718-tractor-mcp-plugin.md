correction: Before reasoning from public product docs about Codex tool loading, inspect the live deferred tool registry when the current Codex task exposes it.
correction: Use github.com/mark3labs/mcp-go for Tractor's MCP implementation; do not substitute another SDK or hand-roll protocol or transport handling.
decision: Tractor MCP tools accept pipeline paths instead of embedding the graph language in tool input schemas; parsing, validation, and schema output call the existing graph package so language changes cannot drift from the MCP surface.
correction: Cache launcher binaries by the resolved plugin root so an installed, version-pinned snapshot cannot execute a concurrently built binary from a development checkout.
correction: Re-check run completion immediately before a raw process-group kill so a reaped PID cannot be signaled after the graceful stop window.
correction: A Tractor run can contain tool commands in their own process groups; forced MCP shutdown must discover and terminate detached descendant groups before considering the session closed.
decision: Use a content-addressed launcher executable so concurrent launches and later development rebuilds cannot change the binary used by an existing MCP server or its child runs.
correction: Persistent content-addressed launcher binaries accumulate across plugin revisions; use a unique per-session build, forward signals through the launcher, and remove the build only after the server has stopped its child runs.
correction: Freeze the owned run leader before inspecting detached descendant groups so its PID and ancestry cannot be recycled during the forced-stop scan.
correction: Run metadata other than checkpoints also needs temp-file-and-rename writes so a bounded forced shutdown cannot leave truncated JSON artifacts.
correction: A shell wrapper cannot portably background a stdio MCP server because POSIX shells may attach asynchronous commands to /dev/null; keep the final launcher step as exec.
decision: Tool groups are authoritatively canceled inside Tractor when the initial stop signal is broadcast; test that real shutdown path directly instead of reconstructing the global process tree with ps.
decision: Publish a repository marketplace and llms.txt that cover plugin installation and MCP operation only; defer pipeline-authoring guidance to later documentation work.
correction: MCP safety annotations must explicitly mark read-only tools non-destructive and run-start/steering tools destructive; mcp-go defaults an omitted destructive hint to true.
correction: Document the actual process-group shutdown boundary rather than promising termination of arbitrary descendants that deliberately detach themselves.
