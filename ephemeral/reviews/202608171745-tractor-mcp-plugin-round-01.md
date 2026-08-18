# Adversarial review — Tractor MCP plugin (round 01)

Target: commit `0102c97` "Add Tractor MCP plugin server" on branch worktree
`/Users/tyler/src/.worktrees/tractor/mcp-plugin`.

Authoritative request reconstructed from the caller's brief, the repository
`ephemeral/worklog/202608171718-tractor-mcp-plugin.md` corrections/decisions, and
the Codex `plugin-creator` system skill:

1. Publish Tractor as a Codex **plugin-provided** stdio MCP server.
2. Implement it with `github.com/mark3labs/mcp-go` (no substitute SDK, no
   hand-rolled protocol/transport).
3. Tool definitions must be compact and deferred.
4. Graph schema and validation must be automatically synchronized with Tractor's
   graph language.

## Scope note

The launch prompt contained no narrowing instruction that had to be refused. It
supplied only valid operating constraints (read-only outside the artifact, fixed
artifact path). Review scope was derived from the sources listed above, not from
the prompt's framing.

## Evidence inspected

- Full diff of `0102c97` (all 20 files), via `git show`.
- `cmd/tractor/mcp.go` (506 lines, read in full), `cmd/tractor/mcp_test.go`,
  `cmd/tractor/root.go` (`loadPipeline`, `cliValidator`, `runPipeline`),
  `cmd/tractor/root_test.go`.
- `.codex-plugin/plugin.json`, `.mcp.json`, `README.md` additions.
- `engine/control.go` (`runManifest`, `loadOrCreateManifest`, `serveControl`),
  `engine/observability_test.go` steering contract.
- `graph/jsonschema_gen.go`, `graph/parse_test.go` (generation + `.sum` drift
  guard).
- Live proof `ephemeral/projects/tractor/mcp-plugin/REPORT.md`, the committed
  smoke client `smoke.go`, and the committed run ledger
  (`checkpoint.json`, `manifest.json`, `timeline.jsonl`, `mcp-stdout.log`).
- Codex plugin contract: `~/.codex/skills/.system/plugin-creator/SKILL.md`,
  `references/plugin-json-spec.md`, `scripts/validate_plugin.py`.
- Local Codex state: `~/.codex/config.toml` `[mcp_servers.*]` entries,
  `~/.agents/` contents.
- Executed `go build ./...`, `go vet ./...`, `go test ./cmd/...` (all pass),
  `time go run ./cmd/tractor --help`.

## What the commit gets right

Requirements 2 and 4 are genuinely met. `mcp.go:21-22,139-198` uses
`mcp-go/server` and `mcp-go/mcp` only, with `server.NewStdioServer(...).Listen`
for transport. `get_pipeline_schema` returns `graph.Graph{}.Schema()` verbatim
(`mcp.go:159`), which is `go-gen-jsonschema`-generated from the `graph.Graph`
type and guarded against drift by `graph/parse_test.go:422-433` (regenerate +
`.sum` comparison). Validation reuses `loadPipeline` + `cliValidator()`
(`mcp.go:438-442`), the same path the CLI uses. `mcp_test.go:66-69` and
`smoke.go:147-151` both assert byte equality with the graph type's schema.
Requirement 3 also holds: every tool sets `mcp.WithDeferLoading(true)`, the
`start_run` input schema is 554 bytes, and this reviewing session's own deferred
tool registry lists `mcp__tractor__*` — independent live corroboration that the
deferred surface loads.

Findings below concern requirement 1 and the operational robustness of the
server, ordered most severe first.

---

## Finding 1 — Managed runs exist only in server memory; server exit orphans pipelines and permanently strands run IDs (critical)

`cmd/tractor/mcp.go:143` creates `runs: make(map[string]*managedRun)` as the sole
record of every started run. `startRun` (`mcp.go:268-276`) spawns a detached
`tractor run` child and records it there. `runTractorMCP` (`mcp.go:138-140`)
returns as soon as `Listen` ends; there is no `defer`, no context-cancellation
hook, and nothing that signals or waits on the children.

Reproduction:
1. Start the server, call `start_run` on any long pipeline. `mcp.go:258-266`
   spawns a child with `command.Dir = workdir`, stdout/stderr redirected to files.
2. Close the client's stdin (what Codex does when it restarts or unloads a
   plugin, and what happens on session end). `Listen` returns; the process exits.
3. The `tractor run` child is not in the server's process group's kill path and
   is never signalled. It keeps running — driving Codex/Claude agent turns and
   spending tokens with no supervisor.
4. Restart the server and call `get_run_status` or `stop_run` with the run ID
   returned in step 1. `lookupRun` (`mcp.go:413-421`) returns
   `unknown run ID "<id>"` for every one of them, forever.

Impact: the plugin's entire advertised value — "Validate, start, monitor, steer,
and stop Tractor pipelines" (`.codex-plugin/plugin.json` `longDescription`) —
survives only as long as one stdio process. Monitoring and stopping are exactly
the operations a user needs *after* a restart, and they are unrecoverable. The
run's `logs_root` already contains `manifest.json` (with `control_socket`),
`checkpoint.json`, and `timeline.jsonl`, so the state needed to rehydrate a run
is on disk and simply is not read; `steer_run` (`mcp.go:354`) already proves the
server knows how to reach a run purely from `logsRoot`. Nothing but the
in-memory map forces a run ID to be non-durable.

## Finding 2 — The "Codex plugin-provided" half of the requirement is unimplemented and unproven (issue)

The request was to publish Tractor as a *Codex plugin-provided* MCP server. What
exists is a valid manifest and a valid `.mcp.json` that were never connected to
Codex:

- No marketplace entry exists anywhere. `find . -name marketplace.json` in the
  repo returns nothing, and `~/.agents/plugins/marketplace.json` does not exist
  (`~/.agents/` contains only `skills/` and `.skill-lock.json`). The
  `plugin-creator` skill treats a personal-marketplace entry as the default
  publication step (`SKILL.md` step 3, `references/plugin-json-spec.md`
  "Marketplace JSON sample spec"). Without one, `codex plugin marketplace add` /
  install has no source and the plugin is not installable.
- `~/.codex/config.toml` has no `[plugins."tractor@..."]` entry, so the plugin
  was never installed or loaded even locally.
- The live proof does not exercise the plugin path. `REPORT.md` claims "The
  committed Codex plugin configuration launches Tractor's real stdio MCP entry
  point," but `smoke.go:74-102` reads `.mcp.json` itself, then *manufactures* the
  launch semantics with `serverCWD := filepath.Join(repository, serverConfig.CWD)`
  and `transport.WithCommandFunc` forcing `process.Dir = serverCWD`. It never
  reads `.codex-plugin/plugin.json` and never involves Codex's plugin loader. The
  only check that touched the manifest was
  `validate_plugin.py`, which for MCP entries verifies nothing beyond "server
  name is a non-empty string and its value is an object"
  (`validate_plugin.py:358-372`).

Impact: the proof establishes that a `mark3labs/mcp-go` stdio server works. It
does not establish anything about plugin provisioning, which is the requirement's
distinguishing word. The `REPORT.md` claim overstates what was demonstrated.

(For the record, `cwd: "."` in `.mcp.json` *is* a supported Codex MCP field —
`~/.codex/config.toml` `[mcp_servers.computer-use]` uses `command = "./..."`
with `cwd = "."` — so the file's shape is not the problem; the absence of any
end-to-end plugin exercise is.)

## Finding 3 — `steer_run` and `stop_run` have zero test and zero live coverage (issue)

`mcp_test.go` exercises `ListTools`, `get_pipeline_schema`, `validate_pipeline`,
`start_run`, and `get_run_status`. `smoke.go` exercises the same five.
`steer_run` (`mcp.go:342-394`) and `stop_run` (`mcp.go:396-411`) are called by
nothing, anywhere. They are the two tools the plugin manifest leads with
("Interactive" capability, "Start this Tractor pipeline and monitor it.") and the
two with the most machinery behind them: unix-socket dialing, manifest decoding,
and signal delivery.

Two concrete latent defects in that untested code:

- `mcp.go:358-360` re-declares an anonymous `{ ControlSocket string
  \`json:"control_socket"\` }` to decode `manifest.json`. The producing type
  `engine.runManifest` (`engine/control.go:19-25`) is unexported, so the JSON key
  is duplicated across packages with no compile-time or test-time link. Renaming
  it in `engine` breaks `steer_run` silently at runtime — precisely the drift the
  commit correctly avoided for the graph schema.
- `steer_run` never checks run status. `engine` deletes the control socket when a
  run ends (`engine/observability_test.go:204`) but leaves `manifest.json` on
  disk with the now-dead socket path. Steering a completed run therefore reports
  `send steering instruction: dial unix ...: connect: no such file or directory`
  instead of the run's actual terminal status. The handler already models a clean
  "no active steerable turn" case for HTTP 409 (`mcp.go:390`) and should model
  this one too.

## Finding 4 — Race: `stop_run` on a just-finished run returns a hard tool error (issue)

`waitForRun` (`mcp.go:285-305`) calls `run.command.Wait()` *before* acquiring
`run.mu`. `stopRun` (`mcp.go:396-410`) acquires `run.mu`, tests
`run.status != "RUNNING"`, then signals.

Interleaving:
1. Child exits. `waitForRun`'s `Wait()` returns; the goroutine is descheduled
   before `run.mu.Lock()` at `mcp.go:290`. `run.status` is still `"RUNNING"`.
2. `stop_run` arrives, takes `run.mu`, passes the `!= "RUNNING"` guard at
   `mcp.go:403`.
3. `run.command.Process.Signal(os.Interrupt)` at `mcp.go:406` returns
   `os.ErrProcessDone` — Go's `os.Process` records completion during `Wait`.
4. `stopRun` returns `fmt.Errorf("interrupt Tractor run: %w", err)`, so the MCP
   client sees a tool *error* — `interrupt Tractor run: os: process already
   finished` — for a run that in fact completed successfully a millisecond
   earlier. It also leaves `run.status` at `"RUNNING"` until step 1's goroutine
   resumes.

The correct behaviour is the one the function already implements for the
non-racy case: report the terminal status. Treating `errors.Is(err,
os.ErrProcessDone)` as benign and re-reading status closes it.

## Finding 5 — The shipped plugin server command is `go run`, which requires a full Go toolchain and a populated module cache on the end user's machine (issue)

`.mcp.json:5` ships `"command": "go", "args": ["run", "./cmd/tractor", "mcp"]`.
For a plugin — software installed by other people — this makes every server
launch depend on: `go` being on Codex's `PATH`, a writable `GOCACHE`/`GOMODCACHE`,
and either a pre-populated module cache or network access to fetch
`mark3labs/mcp-go`, `cobra`, `claude-agent-sdk-go`, and the rest of `go.sum` on
first launch. A cold cache turns plugin startup into a module download plus full
build under Codex's MCP startup timeout (`startup_timeout_sec`, which existing
entries in `~/.codex/config.toml` explicitly raise to 120 for slow servers).

The commit already knows the better path: `README.md` says "Run the server
directly with `tractor mcp` when Tractor is installed as a binary." The shipped
configuration takes the development-convenience path instead, and the live proof
ran on a warm cache in the source tree, so this cost was never observed. A
prebuilt binary (or a wrapper script that builds once and caches) is what a
distributed plugin needs.

---

## Nitpicks

- `s.runs` (`mcp.go:143`) is never pruned; every completed run's `managedRun`,
  including its `LastResponse`-bearing status, is retained for the process
  lifetime.
- `readTail` (`mcp.go:497-506`) reads the entire stderr file into memory before
  slicing the last 4 KiB; on a long-running pipeline with a chatty harness this
  reads the whole log on every `get_run_status` poll (the tests poll every 20 ms).
- On `resume`, `startRun` opens `mcp-stdout.log`/`mcp-stderr.log` with
  `os.O_TRUNC` (`mcp.go:238-246`), discarding the previous attempt's captured
  output for the same `logs_root`.

## Outcome

material findings remain
