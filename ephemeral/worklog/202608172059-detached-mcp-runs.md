request: Replace the unusual source-building MCP wrapper with a normal install/reinstall path, clean resources from earlier plugin instances without killing existing runs, and make MCP-started Tractor runs independent of Codex/MCP lifetime.
decision: Use a normal Go-installed `tractor` binary plus `tractor plugin install`; expose a curlable shell installer that performs both steps.
decision: Persist every MCP run in an atomic, locked state record and launch a hidden `mcp-runner` in a new session so closing the stdio MCP parent does not stop it.
decision: Register only MCP servers whose runs are detached. Reinstall may retire those registered servers safely; legacy/unregistered servers are preserved because their child-run ownership cannot be proven safe.
safety: Validate the persisted run ID and live process command before signaling a run, preventing a stale state record from killing an unrelated PID.
correction: Retire idle legacy MCP parents with a PID-only hard stop, but preserve any legacy MCP parent with descendants so an existing run retains its control owner.
