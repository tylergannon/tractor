# Adversarial review — Tractor MCP plugin (round 06)

Target: current work in worktree `/Users/tyler/src/.worktrees/tractor/mcp-plugin`
at `defd545` "Refresh final Codex plugin proof" (on `785626f`, `c0cb24e`,
`3cd8abb`, `0d37941`, `d426b38`, `16828ab`, `d8a69dc`, `0102c97`), reviewed
cumulatively as `0102c97..HEAD` against the same authoritative sources as rounds
01–05. The tree is not clean: one untracked run ledger is present (see Nitpicks).

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
valid operating constraints (read-only outside the artifact, a fixed artifact
path, "re-review the entire current work"). Scope was re-derived from the
authoritative sources and the whole surface was re-inspected, including code
earlier rounds passed clean.

## Evidence inspected

- `git show c0cb24e`, `git show 785626f`, `git show defd545` in full, plus the
  cumulative `0102c97..HEAD` picture.
- `cmd/tractor/mcp.go` (738 lines, read end to end), `cmd/tractor/mcp_test.go`,
  `cmd/tractor/root.go`, `cmd/tractor/main.go`.
- `scripts/tractor-mcp` (rewritten, 37 lines), `.mcp.json`,
  `.codex-plugin/plugin.json`, `README.md:9-40`.
- `engine/store.go:174-197` (the new atomic `writeJSON`) and `:68-99`,
  `engine/tool.go:40-100`, `engine/runner.go`, `engine/control.go`.
- Live host state: installed plugin `0.1.0+codex.20260818003931`, verified by
  `diff -r` to match HEAD outside `plugin.json`'s version/formatting;
  `~/.cache/tractor/codex-plugin/` (one in-use `run.V9tuUw/`, 14 MB); the live
  process table.
- Proofs: `CODEX-HOST-PROOF.md` (refreshed), `CLAUDE-HOST-PROOF.md`, `REPORT.md`,
  `smoke.go`.
- Executed `go build ./...`, `go vet ./...` (clean), `go test ./...`
  (all packages pass).
- **Two live reproductions**, transcripts inline below: an end-to-end MCP
  `initialize` handshake through the new launcher pattern under three shells, and
  a targeted stdin-inheritance test.

## Round-05 findings: disposition

| # | Round-05 finding | Status |
|---|---|---|
| 1 | Descendant kill unguarded against the reap window (issue) | **Partly fixed.** `forceRunStop` now SIGSTOPs the leader first (`mcp.go:503-508`) via the reap-safe `Process.Signal`, which does pin `rootPID` and its ancestry for the duration of the scan. The descendant pgids themselves are still snapshot values killed later with no guard — Finding 3. |
| 2 | Content-addressed cache never reclaimed (issue) | **Approach changed.** The launcher now builds into a `mktemp -d` per-session directory and `rm -rf`s it on EXIT (`scripts/tractor-mcp:7-13, 21`). Cache is down to 14 MB. But the change also removed `exec`, which broke the server outright on POSIX shells — Findings 1 and 2. |
| 3 | 500 ms window; non-atomic artifacts; no force/timeout control (issue) | **Partly fixed.** `writeJSON` (`engine/store.go:174-197`) is now temp-file-and-rename, so `manifest.json`, `outcome.json`, `branches.json` and `error.json` survive a mid-write SIGKILL. The window, the never-run adapter `Close()`, and the missing force/timeout surface are unchanged — Finding 5. |
| 4 | `ps`-parsing supervision is unrequested infrastructure (issue) | **Not addressed; enlarged.** A SIGSTOP freeze protocol was layered on top of the same scan — Finding 4. |

Requirements 2, 3 and 4 remain met in the source: only `mcp-go` is used, all six
tools carry `mcp.WithDeferLoading(true)` (`mcp.go:169-208`), and
`get_pipeline_schema` returns `string(graph.Graph{}.Schema())` with the
`graph/parse_test.go` drift guard intact. Requirement 1, however, regressed this
round — the plugin no longer starts at all on the most common Linux host.

---

## Finding 1 — Replacing `exec` with a background job loses the server's stdin on POSIX-conforming shells, so the plugin serves nothing wherever `/bin/sh` is dash (critical)

`scripts/tractor-mcp:26-27` now starts the server asynchronously:

```sh
26  "$binary" mcp <&0 &
27  server_pid=$!
```

POSIX XCU 2.9.3 requires that for an asynchronous list, when job control is
disabled, standard input "shall be assigned to /dev/null" *before* any explicit
redirections are applied. `<&0` therefore duplicates the already-reassigned
descriptor onto itself — a no-op — and the server inherits `/dev/null`. macOS's
`/bin/sh` is bash, which deviates from this and preserves the parent's stdin,
which is why the change looked correct here.

Reproduction — the identical JSON-RPC `initialize` request piped into the exact
launcher pattern, changing only the shell:

```text
$ REQ='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{...}}'

$ printf '%s\n' "$REQ" | /bin/sh   /tmp/lclone.sh "$BIN"     # macOS bash-as-sh
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05",
 "capabilities":{"tools":{}},"serverInfo":{"name":"tractor","version":"0.1.0"},...

$ printf '%s\n' "$REQ" | /bin/dash /tmp/lclone.sh "$BIN"     # POSIX behaviour
                                                             # (no output at all)
[exit=0]

$ printf '%s\n' "$REQ" | /bin/dash -c 'exec "$1" mcp' _ "$BIN"   # the old design
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05", ...
```

Isolated confirmation that stdin is the cause, not timing:

```text
sh -c 'head -c 100 > cap; wc -c < cap' <&0 &   with PAYLOAD-12345 on stdin
  /bin/sh   → bytes=14   content=[PAYLOAD-12345]
  /bin/dash → bytes=0    content=[]
```

Impact: on Debian, Ubuntu, and every distribution where `/bin/sh` is dash — the
default Linux case and the usual CI image — the MCP server reads immediate EOF,
`server.NewStdioServer(...).Listen` returns, and the plugin exposes no tools at
all. Requirement 1 ("publish Tractor as a Codex plugin-provided stdio MCP
server") is therefore satisfied only on macOS. `README.md:11-16` states the
plugin's sole launch path is `./scripts/tractor-mcp`, so there is no fallback.
The previous `exec "$binary" mcp` had none of this exposure; the regression is a
direct cost of moving to a background job to make `rm -rf "$run_root"` reachable.

## Finding 2 — Removing `exec` reintroduces the intermediate-process problem round 02 fixed, and makes binary cleanup contingent on an exit that observably does not always happen (issue)

Round 02's finding was that an intermediate process between the host and the
server prevents host signals from landing on the server; `exec` was the fix, and
`README.md` cited it as intentional. `c0cb24e` removed it and replaced it with a
hand-rolled forwarder (`scripts/tractor-mcp:15-22, 29-37`):

- Only `HUP INT TERM` are trapped, and the trap sends **`kill -TERM`** regardless
  of which signal arrived, so a host's SIGINT-then-escalate sequence is flattened.
- `server_pid` is empty for the entire `go build` phase (`:9, :25`), so a signal
  during the build — the longest part of a cold start, which `README.md:18-19`
  admits may download modules — forwards nothing.
- `trap cleanup EXIT` cannot run at all if the launcher is SIGKILLed or if its
  process group is killed, which is exactly how hosts escalate.

That last point is not hypothetical here. The live process table on this machine
shows **eight** orphaned MCP servers left behind by earlier launcher generations:

```text
  PID  PPID  PGID  COMMAND
54158  2644 54158  ~/.cache/tractor/codex-plugin/tractor mcp
54164  2644 54164  ~/.cache/tractor/codex-plugin/tractor mcp
 ... six more ...
79558 79506 79495  /bin/sh ./scripts/tractor-mcp          ← current launcher
79569 79558 79495  ~/.cache/tractor/codex-plugin/run.V9tuUw/tractor mcp
```

Hosts demonstrably leave stdio servers running. Under the previous design a leak
cost nothing extra; under this one every leaked server also strands its
`run.XXXXXX` directory permanently, so `README.md:19-22`'s claim — "removes it
after the server exits … without accumulating a second binary cache" — holds only
on the clean-exit path. The stated goal (no accumulation) and the previous goal
(direct signal delivery) are both achievable at once: build to a per-session
temporary path and `exec` it, letting the kernel reclaim the unlinked inode, or
have the *server* remove its own executable at startup.

## Finding 3 — Freezing the leader stabilizes only the leader; descendant process groups are still SIGKILLed from a stale snapshot with no ownership guard (issue)

`mcp.go:501-522`:

```go
503 	if err := run.command.Process.Signal(syscall.SIGSTOP); err != nil { ... }
509 	descendantGroups, scanErr := descendantProcessGroups(pid)
514 	if err := killProcessGroup(pid); err != nil { ... }
517 	for _, processGroup := range descendantGroups {
518 		if err := killProcessGroup(processGroup); err != nil { ... }
```

SIGSTOP is delivered to one process, not to a tree — the run's detached tool
groups (`engine/tool.go:65`) keep running throughout. So while `rootPID` and its
ancestry are now pinned (the worklog's stated intent, correctly achieved), the
values in `descendantGroups` are ordinary snapshot pgids: between the scan at
`:509` and the loop at `:517-521` any of those group leaders may exit and have its
pgid recycled, and `syscall.Kill(-pgid, SIGKILL)` at `:526` has no ownership
check. `killProcessGroup` swallows `ESRCH` (`:527-529`), so a kill that lands on
an unrelated group is indistinguishable from a no-op — nothing is logged and
nothing is reported.

Two further failure modes the freeze introduces:

- **Indefinite freeze on a slow scan.** `descendantProcessGroups` shells out to
  `ps` with no timeout and no context (`:534`). If `ps` blocks, the run stays
  SIGSTOPped with no watchdog, and `waitForRunDone(run, forcedRunStopTimeout)`
  (`mcp.go:443`) then reports "timed out waiting for Tractor run to stop" for a
  process the server itself froze.
- **Freeze-and-abandon.** If `Process.Signal(SIGSTOP)` returns any error other
  than `ErrProcessDone`, `:507` returns immediately having killed nothing, and
  `requestRunStop` (`mcp.go:440-442`) propagates that error while `run.status`
  stays `"STOPPING"` forever — a state no later `stop_run` can leave, because the
  `"STOPPING"` branch will retry the same failing signal.

## Finding 4 — Process supervision by parsing `ps` remains unrequested infrastructure, now with a freeze protocol layered on top (issue)

`mcp.go:501-579` is now ~80 lines: a SIGSTOP freeze, a fork of
`ps -Ao pid=,ppid=,pgid=`, text parsing, a transitive-closure fixpoint over every
process on the machine, and a multi-target kill — all on the shutdown-critical
path, inside a 500 ms + 1 s budget, in a supervisor whose job is to stop
processes it started itself.

It exists only to compensate for `engine/tool.go:61-65`, where Tractor
deliberately puts each tool command in its own group. Tractor owns both sides:
`engine/tool.go:66-75` already holds every tool's pgid in its `Cancel` closure,
and `engine/control.go` already publishes a `manifest.json` that the MCP server
reads through `engine.LoadControlSocket`. Recording live tool pgids there would
give the supervisor an authoritative list instead of an inference. Costs of the
inference approach that remain unaddressed:

- **Silent leak on scan failure.** If `ps` is missing, sandboxed, or fails,
  `:535-537` returns `nil, err`; the error is collected but **no descendant is
  killed** — precisely the leak the code exists to prevent — and it surfaces only
  as `errors.Join` text on stderr at `main.go:9-12`, a stream MCP hosts discard.
- **Over-broad enumeration.** `ps -A` lists every uid's processes; a descendant
  owned by another user yields `EPERM`, which `killProcessGroup` does *not*
  swallow (only `ESRCH` is), so `stop_run` fails with an error even though the run
  did stop.
- **Snapshot staleness.** Groups created after `:509` are never killed, and the
  leader's own group is killed at `:514` in between.

## Finding 5 — SIGKILL is still the guaranteed outcome for agent runs, and the MCP surface still cannot request or report anything else (issue)

`gracefulRunStopTimeout = 500 * time.Millisecond` (`mcp.go:36`) is still measured
against `runner.Stop()` (`root.go:205-213`), a one-shot cooperative signal checked
between stages that cannot interrupt an in-flight agent turn. For any `codergen`
pipeline the window expires with certainty, so the second `stop_run` and every
session shutdown terminate by SIGKILL as the default.

`785626f` fixed the most serious consequence — `writeJSON` (`engine/store.go:174-197`)
is now temp-file-and-rename, so run metadata can no longer be truncated mid-write.
That is a real improvement and it is correctly implemented. What remains:

- `defer codexAdapter.Close()` / `defer claudeAdapter.Close()` (`root.go:164-167`)
  still never run, so agent sessions are never closed cleanly on any stop.
- `stop_run` still takes only `run_id` (`mcp.go:203-208`) and `stopRunOutput`
  (`mcp.go:131-134`) still carries only `run_id` and `status`, so a caller can
  neither ask for a longer graceful window nor tell a cooperative stop from a kill.
- The new atomic write leaves a `.<name>-*` temp file in the logs root if the
  process is killed between `os.CreateTemp` and `os.Rename`. That directory is the
  one `requireFreshLogsRoot` (`mcp.go:700-712`) checks with `os.ReadDir`, so a
  stray temp file makes the run's own logs root permanently unusable for a fresh
  run — a new, if narrow, failure mode created by the fix.
- The tests still stop only `slowPipeline`, a `tool` node whose stop path *is*
  prompt (`engine/tool.go:46-55`), so none of this is exercised.

---

## Nitpicks

- The working tree is dirty: `ephemeral/projects/tractor/mcp-plugin/live-workspace/.tractor/runs/00b3a8f96a4c65ff4f94f6341dab677f/`
  is untracked, a fourth committed-or-stray `logs_root` alongside
  `codex-cli-live-run/`, `codex-cli-live-run-v2/` and the `20f379…` and `4789fa…`
  ledgers.
- `scripts/tractor-mcp:32-35`'s `while kill -0 "$server_pid"; do wait; done` can
  spin if the reaped pid is recycled between the first `wait` and the `kill -0`:
  `kill -0` succeeds, `wait` returns immediately with "not a child", and the loop
  never terminates.
- `CODEX-HOST-PROOF.md` still records no deferred-loading observation; requirement
  3 rests on `mcp_test.go` and `CLAUDE-HOST-PROOF.md:11-25`.
- `waitForAllRuns` (`mcp.go:622-635`) still spawns a goroutine blocked on
  `<-run.done` and abandons it on every timeout return; called twice per shutdown.
- On signal shutdown `Listen` returns `context.Canceled`, which `errors.Join`
  propagates to `main.go:9-12`, printing `tractor: context canceled` and exiting 1
  — a clean teardown reported to the host as a crash.
- `steer_run`'s status precheck (`mcp.go:371-375`) still synthesizes
  `http_status: 409` although no HTTP exchange occurred.
- `s.runs` (`mcp.go:144`) is still never pruned.

## Outcome

material findings remain
