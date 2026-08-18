# Adversarial review — Tractor MCP plugin (round 04)

Target: current work in worktree `/Users/tyler/src/.worktrees/tractor/mcp-plugin`,
clean tree at `0d37941` "Isolate MCP launcher cache" (on `d426b38`, `16828ab`,
`d8a69dc`, `0102c97`), reviewed cumulatively as `0102c97..HEAD` against the same
authoritative sources as rounds 01–03.

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

- `git show 0d37941` in full, plus the cumulative `0102c97..HEAD` picture.
- `cmd/tractor/mcp.go` (671 lines, read end to end), `cmd/tractor/mcp_test.go`,
  `cmd/tractor/main.go`, `cmd/tractor/root.go`.
- `scripts/tractor-mcp` (19 lines, mode `0755`), `.mcp.json`,
  `.codex-plugin/plugin.json`, `skills/`, `README.md:9-38`.
- `engine/tool.go:40-100`, `engine/store.go:68-99` and `:175-182`,
  `engine/runner.go`, `engine/control.go`, `harness/codex/rpc.go`.
- Live host state: `~/.codex/plugins/cache/personal/tractor/` (one entry,
  `0.1.0+codex.20260818000356`), `~/.agents/plugins/marketplace.json`,
  `~/.cache/tractor/codex-plugin/` (both the orphaned pre-fix `tractor` binary
  and the new `4016100978/tractor`).
- `diff -r` of the installed package against the worktree.
- Proofs: `REPORT.md`, `CODEX-HOST-PROOF.md`, `CLAUDE-HOST-PROOF.md`, `smoke.go`.
- Executed `go build ./...`, `go vet ./...` (clean),
  `go test ./cmd/tractor/...` (pass, 2.8s),
  `python3 ~/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py .`
  ("Plugin validation passed").
- **A live reproduction** of Finding 1 against the built binary (transcript below).

## Round-03 findings: disposition

| # | Round-03 finding | Status |
|---|---|---|
| 1 | Shared launcher build path let a pinned plugin exec another checkout's binary (critical) | **Fixed in source.** `scripts/tractor-mcp:5-7` now keys `cache_root` by `cksum` of the resolved `plugin_root`; the worktree builds into `~/.cache/tractor/codex-plugin/4016100978/`. **Not fixed in the published artifact** — see Finding 2. Residual same-root skew — see Finding 5. |
| 2 | Force-stop cannot reach tool subprocesses in their own process group (issue) | **Not addressed.** `engine/tool.go:65` still sets `Setpgid: true`; `forceRunStop` (`mcp.go:501`) still signals only `-pid(tractor run)`. Now reproduced live — Finding 1. |
| 3 | `requestRunStop` skipped its liveness check and raw-`kill`ed a possibly reaped PGID (issue) | **Mostly fixed.** `mcp.go:436-438` adds an unconditional `waitForRunDone(run, 0)`. The guard keys on `run.done`, which is closed after the reap, so a narrow window survives — Finding 4. |
| 4 | 500 ms graceful window makes SIGKILL the default for agent runs (issue) | **Not addressed.** Constants unchanged (`mcp.go:35-36`); `stopRunOutput` (`mcp.go:131-134`) still reports only `run_id`/`status`. Finding 3. |
| — | Round-03 nitpicks | Unaddressed: `waitForAllRuns` goroutine leak (`mcp.go:553-566`), `context.Canceled` → `tractor: context canceled` + exit 1 (`main.go:9-12`), synthesized `http_status: 409` (`mcp.go:371-375`), `s.runs` never pruned, `codex-cli-live-run-v2/` committed as a populated `logs_root`. |

Requirements 2, 3 and 4 remain solidly met and were re-verified this round: only
`mcp-go` is used, all six tools carry `mcp.WithDeferLoading(true)`
(`mcp.go:169-208`), and `get_pipeline_schema` returns `string(graph.Graph{}.Schema())`
directly (`mcp.go:174`) with the `graph/parse_test.go` drift guard intact.
`validate_plugin.py` passes and `skills/orchestrate-attractor-loops` is present,
so `plugin.json` is structurally sound. This round's findings are the two
round-03 defects that were left in place, the gap between the repository and the
published package, and one residual of each of the two fixes that landed.

---

## Finding 1 — Force-stop leaves tool subprocesses running as orphans; the README's central guarantee is false (critical)

`forceRunStop` (`mcp.go:498-507`) sends `syscall.Kill(-run.command.Process.Pid,
syscall.SIGKILL)` — the group led by `tractor run`. `engine/tool.go:61-65` starts
every tool node's `/bin/sh -c` with `SysProcAttr{Setpgid: true}`, i.e. in a
**different** group, so that signal is by definition not delivered to it. The
only thing that kills the tool group is `command.Cancel` (`engine/tool.go:66-75`),
an in-process closure that a SIGKILL of `tractor run` bypasses.

Round 03 argued this causally. This round it was reproduced against the binary
the launcher actually builds
(`~/.cache/tractor/codex-plugin/4016100978/tractor`), with a pipeline whose tool
node is `sleep 400 & sleep 400`, started in its own process group exactly as
`startRun` does (`mcp.go:279`):

```text
  PID  PPID  PGID  COMMAND
62301 62296 62301  .../tractor run /tmp/tr-repro/p.json --workdir ... --logs ...
62303 62301 62303  /bin/sh -c sleep 400 & sleep 400
62304 62303 62303  sleep 400
62305 62303 62303  sleep 400

$ kill -9 -62301        # exactly what forceRunStop does

  PID  PPID  PGID  COMMAND
62303     1 62303  /bin/sh -c sleep 400 & sleep 400
62304 62303 62303  sleep 400
62305 62303 62303  sleep 400
```

The supervisor is gone; the tool's whole group survives, reparented to `init`,
with no remaining owner. Nothing in the MCP surface can reach it afterwards —
the `managedRun` that held the only reference is terminal, and `stop_run` on a
`STOPPED` run returns immediately (`mcp.go:446-449`).

Impact: this directly falsifies `README.md:36-38` — "Runs belong to the stdio
server session and are stopped when that session closes, preventing an unloaded
plugin from leaving unsupervised agent work running in the background." That
sentence is the stated justification for the entire session-scoped run model, and
`shutdownRuns` routes through the same `forceRunStop`, so closing the stdio
session is precisely when the leak happens. The escape is reachable on the normal
path, not a corner: any pipeline that reaches force-stop with a live tool node
leaves orphans, and Finding 3 shows force-stop is the *expected* outcome for
agent pipelines, not the exception.

`TestRepeatedStopForceKillsRunProcessGroup` still cannot catch it: its fixture
`exec.Command("/bin/sh", "-c", "trap '' INT TERM; sleep 30 & wait")` sets
`Setpgid` on the leader only, so the `sleep` is a child in the **same** group and
`kill(-pgid)` reaps it. The test models the covered case; the escaping case — a
grandchild that created its own group, which is what Tractor's own tool runner
does — is modelled nowhere.

## Finding 2 — The published plugin still contains the pre-fix launcher and pre-fix stop path, so requirement 1's only live evidence attests to superseded code (issue)

Requirement 1 is about a *plugin-provided* server, and the only evidence for it
is the installed package plus `CODEX-HOST-PROOF.md`. `diff -r` of that package
against the worktree shows it is behind HEAD in exactly the two places
`0d37941` changed:

```text
$ diff -r ~/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818000356/ .
... /cmd/tractor/mcp.go ./cmd/tractor/mcp.go
435a436,438
> 		if waitForRunDone(run, 0) {
> 			return currentRunStatus(run), nil
> 		}
... /scripts/tractor-mcp ./scripts/tractor-mcp
5c5,7
< cache_root=${XDG_CACHE_HOME:-"$HOME/.cache"}/tractor/codex-plugin
```

The installed snapshot — the one `[plugins."tractor@personal"] enabled = true`
resolves to, and the only version in the cache directory — therefore still
carries round-03's **critical** shared-cache defect verbatim. That is not
theoretical on this machine: the pre-fix output `~/.cache/tractor/codex-plugin/tractor`
still exists (14 MB, mtime 18:09), is exactly the path that snapshot's line 8
will `mv` over and line 19 will `exec`, and is now written by nothing else — so
the installed plugin will execute a binary whose provenance no current checkout
controls.

Impact: the fix commit changed the repository but did not re-publish, so the
deliverable that satisfies requirement 1 and the source that was fixed have
diverged. `CODEX-HOST-PROOF.md:9-11` names version `0.1.0+codex.20260818000356`
as the proof subject, so the proof is now evidence for `d426b38`, not for HEAD,
and the worklog entry "Cache launcher binaries by the resolved plugin root so an
installed, version-pinned snapshot cannot execute a concurrently built binary
from a development checkout" is not yet true of anything installed. The remedy is
mechanical — re-run the cachebuster/reinstall flow and refresh the proof — but
until then the shipped artifact is not the reviewed one.

## Finding 3 — A 500 ms graceful window still makes SIGKILL the normal outcome for agent runs, and the MCP surface neither exposes nor reports it (issue)

Unchanged from round 03 and repeated because it is what makes Finding 1 routine
rather than rare. `gracefulRunStopTimeout = 500 * time.Millisecond`
(`mcp.go:35`) is measured against a stop mechanism that is explicitly not prompt:
SIGINT reaches `root.go:205-213`, which calls `runner.Stop()`, a one-shot
cooperative signal checked between stages that cannot interrupt an in-flight
agent turn. For any `codergen` pipeline the window expires with certainty, so
the second `stop_run` and every session shutdown terminate the run by SIGKILL as
the **default**.

Consequences of always taking the hard path:

- `defer codexAdapter.Close()` / `defer claudeAdapter.Close()`
  (`root.go:164-167`) never run, so agent sessions are never closed cleanly.
- Artifacts written through `engine/store.go:175-182` — `writeJSON` is a bare
  `os.WriteFile` with no temp-and-rename — can be truncated mid-write:
  `manifest.json`, `stages/*/outcome.json`, `branches.json`, `error.json`.
  `checkpoint.json` is safe (`saveCheckpoint`, `store.go:83-89`, does write-temp-
  then-rename), so resume survives, but the stage record a resumed run reads may
  not.
- The MCP surface hides all of it: `stop_run` takes only `run_id`
  (`mcp.go:203-208`) with no force or timeout argument, and `stopRunOutput`
  (`mcp.go:131-134`) carries only `run_id` and `status`, so a caller can neither
  ask for more patience nor tell a clean cooperative stop from a kill.

The tests cannot expose this because the only pipeline they stop is
`slowPipeline`, a `tool` node whose stop path *is* prompt
(`engine/tool.go:46-55`), so every test run finishes inside the graceful window
and never exercises the branch that dominates in production.

## Finding 4 — The new liveness guard keys on `run.done`, which is closed after the reap, so the "kill a released PGID" window is narrowed but not closed (issue)

`0d37941` added `mcp.go:436-438`:

```go
if waitForRunDone(run, 0) {
    return currentRunStatus(run), nil
}
if err := forceRunStop(run); err != nil { ... }   // :439
```

`waitForRunDone(run, 0)` is a non-blocking read of `run.done`
(`mcp.go:483-490`). `run.done` is closed by `waitForRun` at **`mcp.go:323`** —
after `run.command.Wait()` returns at `mcp.go:304` (which is where the pid is
reaped and released to the OS), after two `Close()` syscalls (`:305-306`), and
after acquiring `run.mu` (`:308`), which a concurrent `snapshotRun` or
`steerRun` may hold. Between the reap at `:304` and the close at `:323` the guard
reports "not done" for a process that no longer exists.

Causal chain, unchanged in shape from round 03:
1. `stop_run` SIGINTs the child; status `"STOPPING"`, `stopAt = now`.
2. The child exits at T+600 ms. `Wait()` returns and reaps the pid; the goroutine
   is descheduled (or blocks on `run.mu`) before `close(run.done)`.
3. A second `stop_run` lands in that window: `remaining` is negative so `:433`
   short-circuits, and `:436` sees `run.done` still open.
4. `forceRunStop` executes `syscall.Kill(-pid, SIGKILL)` (`mcp.go:501`) against a
   reaped, released pgid. `ESRCH` is swallowed (`:502-504`) — but if the pid has
   been recycled as a new group leader, the SIGKILL lands on an unrelated
   process group.

`shutdownRuns`'s force loop (`mcp.go:539-545`) has the identical shape and the
identical residual. Closing it properly requires the liveness check and the kill
to be ordered against the reap — e.g. signalling through `run.command.Process`
(whose `os.ErrProcessDone` guard is reap-aware) rather than raw `syscall.Kill`,
or setting a `reaped` flag under `run.mu` before `Wait()` can complete. The
current guard reduces the window from ~hundreds of milliseconds to microseconds;
it does not eliminate it, and the code reads as though it does.

## Finding 5 — Cache keying by plugin root leaves a same-root skew that `start_run` re-exec makes reachable, and no cache is ever reclaimed (issue)

`scripts/tractor-mcp:5-8` now derives `cache_root` from `cksum` of the resolved
`plugin_root`, which correctly separates the pinned installed snapshot from the
development worktree. Two residuals remain:

- **Same-root, different-content skew.** A plugin root pinned by version is
  immutable, but the development worktree is not. Two concurrent launches from
  it build different revisions into distinct `tractor.$$` candidates and both
  `mv -f` onto the one `$binary` (`:16`). Worse, this outlives the launcher:
  `startRun` re-execs `os.Executable()` (`mcp.go:266, 276`), which is that same
  cached path, so a rebuild landing after the server started makes `start_run`
  spawn a **different revision** of `tractor run` than the server that validated
  the pipeline. That is precisely the schema/runtime skew `README.md:21-23`
  claims the design prevents ("the MCP server and graph language are always
  compiled from the same revision"). `exec "$candidate" mcp` — keeping the
  per-invocation name — would remove the class entirely rather than narrowing it.
- **Unbounded growth, nothing reclaimed.** Because the installed root embeds the
  cachebuster (`.../tractor/0.1.0+codex.<stamp>/`), every reinstall hashes to a
  new `cache_root` and strands a ~14 MB binary. The transition already stranded
  one: `~/.cache/tractor/codex-plugin/tractor` (14,417,986 bytes) is now
  unreferenced by the worktree yet is still the exact path the installed
  snapshot will use (Finding 2), so the directory simultaneously accumulates
  dead binaries and retains a live-but-stale one.

---

## Nitpicks

- `waitForAllRuns` (`mcp.go:553-566`) spawns a goroutine blocked on `<-run.done`
  and abandons it on every timeout return; it is called twice per shutdown.
- On signal shutdown `Listen` returns `context.Canceled`, which `errors.Join`
  propagates to `main.go:9-12`, printing `tractor: context canceled` and exiting
  1 — a clean, expected teardown reported to the host as a crash.
- `steer_run`'s status precheck (`mcp.go:371-375`) still synthesizes
  `http_status: 409` although no HTTP exchange occurred.
- `s.runs` (`mcp.go:144`) is still never pruned, so every run's `managedRun`,
  paths and buffers persist for the life of the session.
- `plugin_key` is a 32-bit `cksum`; collision is remote but the consequence of
  one is silently sharing a binary between two plugin roots.
- `CODEX-HOST-PROOF.md:24` `"loading_deferred": true` is the model's self-report
  rather than a protocol observation, and the refreshed run's `mcp_tool_call`
  list covers three tools, not four. Deferral is nonetheless independently
  supported by `mcp_test.go` and `CLAUDE-HOST-PROOF.md:11-25`.
- `ephemeral/projects/tractor/mcp-plugin/codex-cli-live-run-v2/` remains
  committed as a populated `logs_root`, so re-running that proof at the same
  path fails `requireFreshLogsRoot` (`mcp.go:629-641`) unless `resume` is set.

## Outcome

material findings remain
