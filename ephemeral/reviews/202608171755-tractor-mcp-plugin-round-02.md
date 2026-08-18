# Adversarial review — Tractor MCP plugin (round 02)

Target: current work in worktree `/Users/tyler/src/.worktrees/tractor/mcp-plugin`,
clean tree at `d8a69dc` "Harden MCP run lifecycle and prove Codex plugin",
reviewed cumulatively as `0102c97..HEAD` against the same authoritative sources
as round 01.

Authoritative requirements (unchanged, reconstructed from the caller's brief,
`ephemeral/worklog/202608171718-tractor-mcp-plugin.md`, and the Codex
`plugin-creator` system skill):

1. Publish Tractor as a Codex **plugin-provided** stdio MCP server.
2. Implement with `github.com/mark3labs/mcp-go`; no substitute SDK, no
   hand-rolled protocol or transport.
3. Compact, deferred tool definitions.
4. Graph schema and validation automatically synchronized with Tractor's graph
   language.

## Scope note

The launch prompt contained no narrowing instruction requiring refusal. It gave
only valid operating constraints (read-only outside the artifact, fixed artifact
path, "re-review the entire current work"). Scope was re-derived from the
authoritative sources above, and the whole surface was re-inspected rather than
only the round-01 findings.

## Evidence inspected

- `git diff 0102c97 HEAD` in full (30 files), plus the round-01 baseline.
- `cmd/tractor/mcp.go` (current, 572 lines), `cmd/tractor/mcp_test.go`,
  `cmd/tractor/main.go`, `cmd/tractor/root.go` (`runPipeline` signal handling at
  :205-213, `loadPipeline`, `cliValidator`).
- `engine/control.go` (new exported `LoadControlSocket`, `runManifest`,
  `loadOrCreateManifest`, `serveControl`), `engine/tool.go:55-100`,
  `harness/codex/rpc.go:85-110`, `engine/git_workspace.go:229`.
- `graph/jsonschema_gen.go`, `graph/parse_test.go` drift guard.
- Proofs: `REPORT.md`, `CODEX-HOST-PROOF.md`, `CLAUDE-HOST-PROOF.md`, the three
  committed run ledgers, `smoke.go`.
- Codex install state: `~/.agents/plugins/marketplace.json`,
  `~/.codex/plugins/cache/personal/tractor/0.1.0/`,
  `~/.codex/config.toml` `[plugins."tractor@personal"] enabled = true`.
- Dependency semantics: `mcp-go@v0.58.0` `server/stdio.go` (`Listen`,
  `processInputStream`) and `client/transport/stdio.go` (`Close`,
  `gracefulShutdownTimeout`, `waitForProcessExit`).
- Executed `go build ./...`, `go vet ./...`, `go test ./cmd/tractor -run TestMCP -v`
  (4/4 pass), `go test ./cmd/...` (pass).

## Round-01 findings: disposition

| # | Round-01 finding | Status |
|---|---|---|
| 1 | Runs memory-only; exit orphans pipelines, strands run IDs | **Partially fixed.** A session-lifetime model was chosen and documented instead of durability. Run IDs are still session-scoped by design, which is now a stated contract (`README.md:34-36`, server instructions). The orphan half is not actually fixed — see Findings 1 and 2. |
| 2 | Codex plugin path unimplemented and unproven | **Fixed.** `~/.agents/plugins/marketplace.json` now carries a `tractor` entry, the plugin is installed and enabled (`[plugins."tractor@personal"]`), cached at `~/.codex/plugins/cache/personal/tractor/0.1.0`, and `CODEX-HOST-PROOF.md` records real `mcp_tool_call` events against server `tractor` from `codex exec`. |
| 3 | `steer_run`/`stop_run` untested; manifest decode duplicated | **Mostly fixed.** `TestMCPStdioSteersAndStopsRunningPipeline` covers both; the duplicated struct is replaced by exported `engine.LoadControlSocket` (`engine/control.go:40-55`); stale-socket confusion is replaced by a status precheck (`mcp.go:355-361`). Residual gap in Finding 4. |
| 4 | `stop_run` race returns hard error on just-finished run | **Fixed.** `requestRunStop` (`mcp.go:411-436`) treats `os.ErrProcessDone` as benign and re-reads status after `run.done`. |
| 5 | `go run` as the plugin server command | **Accepted with documentation** (`README.md:18-21`) and now demonstrated working from the installed plugin cache. Reasonable; not re-raised. |

Requirements 2, 3 and 4 remain solidly met, and requirement 1 is now genuinely
demonstrated. The findings below are all in the newly added shutdown/lifecycle
machinery, which is where this round's risk moved.

---

## Finding 1 — Shutdown's force-kill reaches only the direct child, so the documented "no unsupervised agent work" guarantee fails precisely for agent pipelines (critical)

`README.md:34-36` now promises: "Runs belong to the stdio server session and are
stopped when that session closes, preventing an unloaded plugin from leaving
unsupervised agent work running in the background." The server instructions
string (`mcp.go:150`) repeats it. The implementation does not deliver it for the
only workload where it matters.

`shutdownRuns` (`mcp.go:438-486`) escalates with
`run.command.Process.Kill()` (`mcp.go:470`) — a SIGKILL to the single
`tractor run` PID. Nothing signals that process's descendants:

- `harness/codex/rpc.go:86` starts the agent CLI with plain `exec.Command`: no
  `Setpgid`, no `Pdeathsig`, no cancel hook. Its only cleanup is `adapter.Close()`,
  deferred in `runPipeline` (`root.go:164`) — and `defer` does not run on SIGKILL.
- `engine/tool.go:61-75` deliberately places tool commands in *their own* process
  group (`SysProcAttr{Setpgid: true}`) whose sole killer is the in-process
  `command.Cancel` closure. SIGKILL of the parent bypasses that closure, and the
  new process group means the orphan is not even collaterally signalled.
- Nothing in the tree sets `Setpgid` on the `tractor run` child itself
  (`grep -rn 'Setpgid|SysProcAttr|Pgid'` returns exactly one hit, `engine/tool.go:65`),
  so there is no group to kill.

Why escalation is the *expected* path rather than the exception: the graceful
step sends SIGINT (`mcp.go:421`), which the child converts to a **cooperative**
stop — `root.go:205-213` wires `signal.NotifyContext` to `runner.Stop()`, a
one-shot stop signal (`engine/runner.go:67-78`) checked between stages. It does
not interrupt an in-flight agent turn. Agent turns routinely run for minutes.
`shutdownRuns` waits 5 seconds (`mcp.go:462`) and then force-kills. For any real
`codergen` pipeline the 5-second budget expires with certainty.

Reproduction (causal chain, each link verified above):
1. `start_run` a pipeline containing a `codergen` node; the harness starts a
   `codex` CLI subprocess (`harness/codex/rpc.go:86`) and enters a turn.
2. The host unloads the plugin; stdin closes; `shutdownRuns` sends SIGINT.
3. `tractor run` records the stop but stays inside the agent turn.
4. At T+5s, `mcp.go:470` SIGKILLs `tractor run` only.
5. The `codex` subprocess is reparented to init and continues its turn — burning
   tokens, writing to the workspace, with no supervisor and no run ID left to
   address it. This is verbatim the state the README says is prevented.

Why the test does not catch it: `TestMCPStdioShutdownStopsRunningPipeline`
(`mcp_test.go`) uses `slowPipeline`, a **tool** node (`sleep 30`), and asserts
only `processExists(start.PID)` — the direct child. It never inspects
descendants, and because the tool path stops within milliseconds the run never
reaches the force-kill branch at all. The test passes while proving nothing about
the branch under discussion.

## Finding 2 — The shutdown path is reachable only on a graceful stdin EOF completed within ~2 seconds; signal-based teardown and normal host force-kill both bypass it entirely (issue)

`runTractorMCP` (`mcp.go:139-143`) runs `shutdownRuns` only after
`Listen` returns. Two common teardown paths never get there.

**(a) No signal handling in the MCP server.** `cmd/tractor/main.go:9` calls
`newRootCommand().Execute()`, not `ExecuteContext(...)`, so `command.Context()`
inside the `mcp` RunE is `context.Background()` with no cancellation source. The
process has no handler for SIGTERM or SIGINT; the default disposition terminates
it immediately, `Listen` never returns, and `shutdownRuns` never executes. Every
child is orphaned. The asymmetry is notable: the *child* `tractor run` correctly
installs `signal.NotifyContext(..., os.Interrupt, syscall.SIGTERM)` at
`root.go:205`, while the parent that now claims ownership of child lifetimes does
not. The shipped `go run ./cmd/tractor mcp` command (`.mcp.json:5`) inserts an
extra `go` process between host and server, adding another hop a signal must
survive.

**(b) The shutdown budget exceeds the host's grace period.** `shutdownRuns`
budgets up to 5s graceful (`mcp.go:462`) plus 1s post-kill (`mcp.go:476`) — about
6 seconds after stdin closes. mcp-go's own stdio client — the reference behaviour
for hosts built on this SDK, and the client this repo's tests and `smoke.go` use
— closes stdin then force-kills after `gracefulShutdownTimeout = 2 * time.Second`
(`client/transport/stdio.go:63`, used at :274). Such a host SIGKILLs the Tractor
MCP server roughly 2 seconds into its 6-second shutdown, i.e. during the graceful
wait and *before* the escalation loop at `mcp.go:466-473` ever runs. The result is
the same orphaning as (a), reached from the path the code was written for.

The existing test cannot detect this: `session.Close()` does close stdin, but the
`sleep 30` tool child dies in well under 2s, so the run completes shutdown inside
the grace window every time.

## Finding 3 — `stop_run` cannot force a run that will not stop cooperatively; a repeated call is silently inert (issue)

`requestRunStop` (`mcp.go:411-436`) short-circuits on `run.status != "RUNNING"`
(`mcp.go:413`). Once the first `stop_run` sets `"STOPPING"` (`mcp.go:433`), every
subsequent `stop_run` for that run returns `{status: "STOPPING"}` **without
re-signalling and without escalating**. There is no force flag, no second-SIGINT
escalation, and no timeout — the escalation logic exists only inside
`shutdownRuns`.

Combined with Finding 1's cooperative-stop semantics, a user whose `codergen` run
is stuck in a long or hung agent turn has no way to terminate it through the MCP
surface: `stop_run` reports `"STOPPING"` forever, and the tool is nonetheless
annotated `mcp.WithDestructiveHintAnnotation(true)` (`mcp.go:196`), advertising a
decisiveness it does not have. The only remaining recourse is closing the whole
session — which, per Findings 1 and 2, orphans the subprocesses rather than
stopping them.

Reproduction: `start_run` any pipeline whose current stage outlasts the caller's
patience (a `tool` node with `sleep 300`, or any agent turn); call `stop_run`;
observe `"STOPPING"`; call `stop_run` again and again — `mcp.go:413` returns
early each time, no signal is delivered, and `get_run_status` stays `"STOPPING"`
until the stage finishes on its own.

## Finding 4 — The successful-steer branch of `steer_run` is still unexercised at the MCP layer (issue)

Steering is the plugin's advertised differentiator: `.codex-plugin/plugin.json`
declares capability `"Interactive"` and `longDescription` "Validate, start,
monitor, **steer**, and stop Tractor pipelines". The HTTP 200 branch —
`mcp.go:390-391`, the only path that returns `Accepted: true` — has no passing
evidence anywhere:

- `TestMCPStdioSteersAndStopsRunningPipeline` deliberately asserts the *failure*
  shape: `if steer.Accepted || steer.HTTPStatus != 409 { t.Fatalf(...) }`. A
  regression that made every steer return 409 would keep this test green.
- `CLAUDE-HOST-PROOF.md:230` explicitly disclaims it: "does **not** exercise …
  the `steer_run` / `stop_run` control-socket path — those remain unvalidated."
- `CODEX-HOST-PROOF.md` calls only `get_pipeline_schema`, `validate_pipeline`,
  `start_run`, `get_run_status`.
- `smoke.go` does not steer.

`engine/observability_test.go:177-179` proves the engine's `/steer` endpoint
returns 200 for an active turn, but nothing proves the MCP handler's request
construction reaches it successfully — the unix-socket `http.Transport`
(`mcp.go:374-377`), the `harness.ContentPart` body encoding (`mcp.go:369`), and
the `Content-Type` header (`mcp.go:382`) are all only ever exercised against a
run with no steerable turn, where a malformed body would also produce a
non-200 and could be mistaken for the expected 409. A test using a `codergen`
node with a stub harness, or a fixture control server, would close this.

---

## Nitpicks

- `steer_run` synthesizes `http_status: 409` in the status-precheck branch
  (`mcp.go:357-360`) without any HTTP exchange having occurred, so a field named
  for a protocol result reports a value the protocol never produced.
- `requestRunStop`'s `os.ErrProcessDone` path blocks up to one second
  (`mcp.go:427-429`) inside a synchronous tool handler.
- Carried over from round 01 and still open: `s.runs` (`mcp.go:144`) is never
  pruned; `readTail` (`mcp.go:563`) reads the whole stderr file before slicing
  the last 4 KiB on every status poll; `resume` re-opens
  `mcp-stdout.log`/`mcp-stderr.log` with `os.O_TRUNC` (`mcp.go:241-249`),
  discarding the prior attempt's captured output.
- `CLAUDE-HOST-PROOF.md:7` labels the transport "plugin-installed", but in Claude
  Code the server is loaded from the repository's `.mcp.json`, not from a Codex
  plugin install; the document's own header correctly notes the host difference,
  so the transport line reads as slightly over-claimed.
- `ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run/` is committed as a
  populated `logs_root`, so re-running that proof against the same path now fails
  `requireFreshLogsRoot` (`mcp.go:541-553`) unless `resume` is set.

## Outcome

material findings remain
