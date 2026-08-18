# Adversarial review — Tractor MCP plugin (round 03)

Target: current work in worktree `/Users/tyler/src/.worktrees/tractor/mcp-plugin`,
clean tree at `d426b38` "Refresh installed Codex plugin proof" (on top of
`16828ab` "Make MCP shutdown decisive"), reviewed cumulatively as
`0102c97..HEAD` against the same authoritative sources as rounds 01 and 02.

Authoritative requirements (unchanged):

1. Publish Tractor as a Codex **plugin-provided** stdio MCP server.
2. Implement with `github.com/mark3labs/mcp-go`; no substitute SDK, no
   hand-rolled protocol or transport.
3. Compact, deferred tool definitions.
4. Graph schema and validation automatically synchronized with Tractor's graph
   language.

Sources: the caller's brief, `ephemeral/worklog/202608171718-tractor-mcp-plugin.md`,
and the Codex `plugin-creator` system skill.

## Scope note

The launch prompt contained no narrowing instruction requiring refusal — only
valid operating constraints (read-only outside the artifact, fixed artifact
path, "re-review the entire current work"). Scope was re-derived from the
authoritative sources and the whole surface was re-inspected, including code the
previous rounds passed clean.

## Evidence inspected

- `git diff d8a69dc HEAD` in full (25 files) plus the cumulative
  `0102c97..HEAD` picture.
- `cmd/tractor/mcp.go` (current, 668 lines), `cmd/tractor/mcp_test.go`,
  `cmd/tractor/main.go`, `cmd/tractor/root.go`.
- New launcher `scripts/tractor-mcp` (17 lines, mode `0755`) and the rewritten
  `.mcp.json`.
- `engine/tool.go:23-100` (tool process-group and cancel semantics),
  `engine/store.go:68-99` (atomic checkpoint) and `:175-182` (`writeJSON`),
  `engine/runner.go:67-78` (`StopSignal`), `engine/control.go`,
  `harness/codex/rpc.go:85-110`.
- `mcp-go@v0.58.0` `server/stdio.go` (`readNextLine` ctx-awareness at :480-499,
  `processInputStream` :417-440, `Listen`) and
  `client/transport/stdio.go:63,248-280` (`gracefulShutdownTimeout = 2s`).
- Proofs: `REPORT.md`, `CODEX-HOST-PROOF.md`, `CLAUDE-HOST-PROOF.md`,
  `smoke.go`, and the refreshed run ledgers.
- Live host state: `~/.agents/plugins/marketplace.json`;
  `~/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818000356/`
  (verified `scripts/tractor-mcp` present with the exec bit preserved);
  `[plugins."tractor@personal"] enabled = true`;
  `~/.cache/tractor/codex-plugin/tractor` (the launcher's build output).
- Executed `go build ./...`, `go vet ./...`,
  `go test ./cmd/tractor -run 'TestMCP|TestSteer|TestRepeated' -v` (6/6 pass).

## Round-02 findings: disposition

| # | Round-02 finding | Status |
|---|---|---|
| 1 | Force-kill reached only the direct child; agent subprocesses survived | **Mostly fixed.** `startRun` now sets `SysProcAttr{Setpgid: true}` (`mcp.go:279`) and `forceRunStop` sends `syscall.Kill(-pid, SIGKILL)` (`mcp.go:498`), which does reach `harness/codex/rpc.go:86`'s CLI subprocess. One escape route remains — Finding 2. |
| 2 | Shutdown path unreachable via signals and over-budget vs host grace | **Fixed, verified.** `signal.NotifyContext(..., os.Interrupt, syscall.SIGTERM)` at `mcp.go:142`; mcp-go's `readNextLine` is ctx-aware (`server/stdio.go:493-495`) so `Listen` does return on cancel. Budget cut to 500 ms + 1 s (`mcp.go:35-36`), under the client's 2 s, and `TestMCPStdioShutdownStopsRunningPipeline` now asserts `elapsed < 2s`. The `exec` in `scripts/tractor-mcp:17` removes the intermediate `go run` process so host signals land on the server. |
| 3 | `stop_run` inert on repeat; no force escalation | **Fixed.** `requestRunStop`'s `"STOPPING"` case (`mcp.go:429-442`) escalates to a process-group SIGKILL, covered by `TestRepeatedStopForceKillsRunProcessGroup`. Residual defect in Finding 3. |
| 4 | Successful-steer branch unexercised | **Fixed.** `TestSteerRunForwardsAcceptedInstruction` stands up a real unix-socket `/steer` server and asserts `Accepted`, `HTTPStatus == 200`, and the decoded `harness.ContentPart` payload. |
| — | Round-02 nitpicks | `readTail` now seeks instead of reading whole files (`mcp.go:650-668`); `resume` appends instead of truncating run logs (`mcp.go:251-255`). |

Requirements 2, 3 and 4 remain solidly met; requirement 1 remains demonstrated
against a genuinely installed plugin (now `0.1.0+codex.20260818000356`, with the
launcher script present and executable in the cached package). This round's
findings are concentrated in the two things this commit introduced: the launcher
script and the tightened stop machinery.

---

## Finding 1 — The launcher's build cache is a single shared path, so a version-pinned installed plugin can exec a binary built from a different checkout (critical)

`scripts/tractor-mcp` derives its output path from the *user*, not from the
plugin it is launching:

```
5  cache_root=${XDG_CACHE_HOME:-"$HOME/.cache"}/tractor/codex-plugin
6  binary=$cache_root/tractor
...
13 go build -o "$candidate" ./cmd/tractor
14 mv -f "$candidate" "$binary"
...
17 exec "$binary" mcp
```

Every checkout that ships this script writes to and executes the same
`~/.cache/tractor/codex-plugin/tractor`. That is not hypothetical on this
machine: the file exists (verified, 14 MB, mtime 18:06) and **two** distinct
source trees are currently wired to produce it —

- the development worktree `/Users/tyler/src/.worktrees/tractor/mcp-plugin`, via
  the repository's own `.mcp.json`, and
- the frozen installed package
  `~/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818000356`, verified
  to contain `scripts/tractor-mcp` with mode `0755` and its own `.mcp.json`.

Reproduction (both hosts starting concurrently, the normal case here):
1. Codex launches the installed plugin. Its launcher builds the pinned snapshot
   into `$candidate` and `mv`s it to `$binary` (line 14).
2. Claude Code launches the repo server. Its launcher builds the worktree — a
   different, possibly uncommitted revision — and `mv`s it over `$binary`.
3. Step 1's process reaches line 17 and `exec`s `$binary`, which is now step 2's
   build. `mv` is a rename, so there is no error, no `ETXTBSY`, and no
   diagnostic: the pinned plugin silently serves the worktree's MCP surface,
   graph schema, and validation rules.

Impact and why it matters more than a normal race: this defeats the launcher's
own stated purpose. `README.md:21-23` justifies building from source with
"Building from the snapshot is intentional: the MCP server and graph language are
always compiled from the same revision." The shared path makes exactly that
invariant unenforceable, and it does so in the direction that matters — a
version-pinned, installed artifact executing code from an unrelated,
unreviewed working tree. Requirement 4's "automatically synchronized" guarantee
is sound *inside* the binary (`graph.Graph{}.Schema()` plus the
`graph/parse_test.go` drift guard) but is undone at the launcher.

The fix is available and cheap: `exec "$candidate" mcp` directly, or key
`cache_root` by a hash of `$plugin_root`. Nothing about the design requires a
shared filename.

## Finding 2 — Force-stop cannot reach tool subprocesses, which deliberately live in their own process group (issue)

`forceRunStop` (`mcp.go:497-506`) sends `syscall.Kill(-run.command.Process.Pid,
syscall.SIGKILL)` — the process group led by `tractor run`. That now covers the
agent CLI (`harness/codex/rpc.go:86` uses plain `exec.Command`, inheriting the
group), which was round-02's main concern. It does not cover tool nodes:

- `engine/tool.go:61-65` starts `/bin/sh -c "<tool_command>"` with
  `SysProcAttr{Setpgid: true}`, placing it in a **new** process group whose id is
  the shell's own pid, not `tractor run`'s.
- A signal to `-pgid(tractor run)` is by definition not delivered to that group.
- The only thing that kills the tool group is `command.Cancel`
  (`engine/tool.go:66-75`), an in-process closure — which a SIGKILL of
  `tractor run` bypasses.

Reachability: the force path fires whenever `tractor run` fails to exit within
`gracefulRunStopTimeout` (500 ms, `mcp.go:35`) of SIGINT. A `parallel` pipeline
with one `codergen` branch and one `tool` branch makes this deterministic —
SIGINT sets a cooperative stop that cannot interrupt the in-flight agent turn
(`engine/runner.go:67-78`, `root.go:205-213`), the 500 ms elapses, `tractor run`
is SIGKILLed, and the tool branch's `sh` and its descendants survive as orphans
holding the workspace. That is the same "unsupervised work running in the
background" the README (`:34-36`) says the session model prevents.

Why the test does not catch it: `TestRepeatedStopForceKillsRunProcessGroup` uses
`exec.Command("/bin/sh", "-c", "trap '' INT TERM; sleep 30 & wait")` with
`Setpgid` on the leader only. The `sleep` is a child in the **same** group, so
`kill(-pgid)` reaps it. The fixture models the covered case; the escaping case —
a grandchild that created its own group — is not modelled anywhere.

## Finding 3 — `requestRunStop` skips its liveness check when the graceful window has already elapsed, then raw-`kill`s a possibly reaped process-group id (issue)

`mcp.go:429-442`:

```go
case "STOPPING":
    stopAt := run.stopAt
    run.mu.Unlock()
    remaining := time.Until(stopAt.Add(gracefulRunStopTimeout))
    if remaining > 0 && waitForRunDone(run, remaining) {   // :433
        return currentRunStatus(run), nil
    }
    if err := forceRunStop(run); err != nil { ... }        // :436
```

When `remaining <= 0` the `&&` short-circuits, so `waitForRunDone` never runs and
`forceRunStop` is invoked with **no** check that the process is still alive.

Causal chain:
1. `stop_run` SIGINTs the child; status becomes `"STOPPING"`, `stopAt = now`.
2. The child exits at, say, T+600 ms. `waitForRun`'s `run.command.Wait()` returns
   and **reaps** the pid; the goroutine is then descheduled before it takes
   `run.mu` at `mcp.go:305`, so `run.status` is still `"STOPPING"`.
3. A second `stop_run` lands in that window. `remaining` is negative (600 ms >
   500 ms), so line 433 is skipped entirely.
4. `forceRunStop` executes `syscall.Kill(-pid, SIGKILL)` against a reaped pid.
   Normally this returns `ESRCH`, which line 499 swallows. But the pid has been
   released to the OS, and if it has been recycled as a new process-group
   leader the SIGKILL lands on an unrelated process group.

Two pieces of evidence that this is an oversight rather than a deliberate
tradeoff: `shutdownRuns`'s force loop *does* guard the same call with
`if waitForRunDone(run, 0) { continue }` (`mcp.go:537-539`), and the round-01 fix
for the analogous race relied on `os.Process.Signal`'s built-in
`os.ErrProcessDone` guard — a guard raw `syscall.Kill` does not have. Adding the
unconditional `waitForRunDone(run, 0)` check before `forceRunStop` closes it.

## Finding 4 — A 500 ms graceful window makes SIGKILL the normal outcome for agent runs, and nothing surfaces that to the caller (issue)

`gracefulRunStopTimeout = 500 * time.Millisecond` (`mcp.go:35`) was chosen to fit
the whole shutdown under mcp-go's 2 s client grace — a legitimate constraint,
correctly identified. But it is measured against a stop mechanism that is
explicitly *not* prompt: SIGINT reaches `root.go:205-213`, which calls
`runner.Stop()`, a one-shot signal (`engine/runner.go:67-78`) checked between
stages and unable to interrupt an in-flight agent turn. For any `codergen`
pipeline the 500 ms therefore expires with certainty, so the second `stop_run`
and every session shutdown terminate the run by SIGKILL as the *default*, not the
exception.

Consequences of always taking the hard path:
- `defer codexAdapter.Close()` / `defer claudeAdapter.Close()` (`root.go:164-167`)
  never run, so agent sessions are never closed cleanly.
- Artifacts written through `engine/store.go:175-182` (`writeJSON` →
  `os.WriteFile`, no temp-and-rename) can be truncated mid-write:
  `manifest.json` (`engine/control.go:120`), `stages/*/outcome.json`
  (`store.go:149`), `branches.json` (`engine/parallel.go:78`), `error.json`
  (`store.go:169`). `checkpoint.json` is safe — `saveCheckpoint`
  (`store.go:83-89`) does write-temp-then-rename — so resume survives, but the
  stage record a resumed run reads may not.
- The MCP surface hides all of it: `stop_run` takes no force/timeout argument and
  `stopRunOutput` (`mcp.go:126-129`) reports only `run_id` and `status`, so a
  caller cannot tell a clean cooperative stop from a kill, nor ask for more
  patience.

The tests cannot expose this because the only pipeline they stop is
`slowPipeline`, a `tool` node, whose stop path *is* prompt (`engine/tool.go:46-55`
cancels on `scope.Stop` and SIGKILLs the tool's group), so every test run
completes inside the graceful window and never exercises the branch that will
dominate in production.

---

## Nitpicks

- `waitForAllRuns` (`mcp.go:550-563`) spawns a goroutine blocked on `<-run.done`
  and abandons it on every timeout return; harmless at process exit, but it is
  called twice per shutdown and leaks both times when runs are slow.
- On signal shutdown `Listen` returns `context.Canceled`, which
  `errors.Join` propagates to `main.go:9`, printing `tractor: context canceled`
  and exiting 1. A clean, expected teardown is reported to the host as a crash.
- `steer_run`'s status precheck (`mcp.go:357-360`) still reports
  `http_status: 409` although no HTTP exchange occurred.
- `s.runs` (`mcp.go:144`) is still never pruned.
- `CODEX-HOST-PROOF.md`'s `"loading_deferred": true` is the model's self-report,
  not a protocol observation; the refreshed run's `mcp_tool_call` list also no
  longer includes `get_pipeline_schema`, so that proof now covers three tools
  rather than four. The deferral claim is nonetheless independently supported by
  `mcp_test.go` and `CLAUDE-HOST-PROOF.md:11-25`.
- `ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run-v2/` is committed as
  a populated `logs_root`; re-running that proof at the same path now fails
  `requireFreshLogsRoot` unless `resume` is set — the same pattern that forced
  the `-v2` suffix this round.

## Outcome

material findings remain
