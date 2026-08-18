correction: The Codex plugin should run a normally installed Tractor binary; requiring Go is acceptable, but cloning and compiling the repository on every MCP launch is not.
decision: Install and update Tractor with `go install github.com/tylergannon/tractor/cmd/tractor@latest`; configure the plugin to execute `tractor mcp` directly.
friction: The persistent Codex app-server hit its inherited 256-FD soft limit with 53 retained task and MCP children; 49 stale process groups were terminated gracefully before work resumed -> Codex should reap idle tool processes or raise its descriptor limit.
doc_bug: The MCP live-proof fixture still used removed `start` and `exit` node types, so it could launch the installed server but failed current-schema validation -> keep proof fixtures aligned with the shipped graph language.
doc_bug: The MCP server reported `0.2.0` independently of the `0.2.1` plugin manifest -> make release proof reject server and manifest version drift.
friction: The live MCP smoke client wrote run logs into its tracked fixture workspace -> give proof runs a disposable logs root and remove it on exit.
