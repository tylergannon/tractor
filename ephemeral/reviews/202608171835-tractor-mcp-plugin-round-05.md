# Adversarial review — Tractor MCP plugin (round 05)

Target: current work in worktree `/Users/tyler/src/.worktrees/tractor/mcp-plugin`,
clean tree at `3cd8abb` "Harden forced MCP shutdown" (on `0d37941`, `d426b38`,
`16828ab`, `d8a69dc`, `0102c97`), reviewed cumulatively as `0102c97..HEAD`
against the same authoritative sources as rounds 01–04.

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

- `git show 3cd8abb` in full, plus the cumulative `0102c97..HEAD` picture.
- `cmd/tractor/mcp.go` (738 lines, read end to end), `cmd/tractor/mcp_test.go`,
  `cmd/tractor/root.go`, `cmd/tractor/main.go`.
- `scripts/tractor-mcp` (23 lines), `.mcp.json`, `.codex-plugin/plugin.json`,
  `README.md:9-41`.
- `engine/tool.go:40-100`, `engine/store.go:68-99` and `:175-182`,
  `engine/runner.go`, `engine/control.go`, `harness/codex/rpc.go`.
- Go 1.26 `$GOROOT/src/os/exec_unix.go:32-110` (`pidWait` / `pidSignal`), to
  settle whether `Process.Kill()` is reap-safe.
- Live host state: `~/.codex/plugins/cache/personal/tractor/` (single entry
  `0.1.0+codex.20260818002540`), `~/.cache/tractor/codex-plugin/` (55 MB across
  three naming schemes).
- `diff -r` of the installed package against the worktree, file by file.
- Proofs: `CODEX-HOST-PROOF.md` (refreshed), `CLAUDE-HOST-PROOF.md`, `REPORT.md`,
  `smoke.go`, and the newly committed run ledger.
- Executed `go build ./...`, `go vet ./...` (clean), `go test ./cmd/tractor/...`
  (pass, 2.96 s), and timed `ps -Ao pid=,ppid=,pgid=` (805 rows, 12 ms).
- **A live end-to-end reproduction** of the new shutdown algorithm against a real
  `tractor run` with a detached tool group (transcript in Finding 1's preamble).

## Round-04 findings: disposition

| # | Round-04 finding | Status |
|---|---|---|
| 1 | Force-stop orphaned tool subprocesses; `README.md` guarantee false (critical) | **Fixed, verified live.** `forceRunStop` (`mcp.go:501-523`) now scans the process tree and kills each detached descendant group. Re-ran round 04's exact reproduction — a real run whose tool node is `sleep 400 & sleep 400`, in its own group — and applied the new algorithm verbatim: it discovered `descendants: [70514, 70516, 70517, 70518]`, `groups: [70516]`, and after the kill sequence `ps` showed **no survivors**. The behavioral claim now holds. |
| 2 | Published package behind HEAD, so requirement 1's proof attested to superseded code (issue) | **Fixed.** Reinstalled as `0.1.0+codex.20260818002540`; `CODEX-HOST-PROOF.md:9-11` names it and `diff -r` shows the snapshot matches HEAD in `scripts/tractor-mcp`, `.mcp.json` and `README.md` byte for byte (one cosmetic drift noted under Nitpicks). |
| 3 | 500 ms window makes SIGKILL the default; no force/timeout control; non-atomic artifacts (issue) | **Not addressed.** Constants unchanged (`mcp.go:36-37`); `stopRunOutput` unchanged; `engine/store.go:175-182` unchanged. `README.md:38-41` was rewritten to describe the kill as intended. Finding 3. |
| 4 | Liveness guard keyed on `run.done`, so a reaped PGID could still be signalled (issue) | **Fixed for the leader.** `forceRunStop` now uses `run.command.Process.Kill()` (`mcp.go:508`) and gates the group kill on `leaderErr == nil` (`:512-516`). Verified against Go's implementation: `pidWait` calls `doRelease(statusDone)` *before* `Wait4` and drains `sigMu` (`exec_unix.go:50-56`), so `pidSignal` returns `ErrProcessDone` throughout the reap window. The guard is sound — but it was not applied to the new descendant path. Finding 1. |
| 5 | Same-root build skew plus never-reclaimed cache (issue) | **Skew fixed.** The launcher is now content-addressed (`scripts/tractor-mcp:13-20`), so an existing server and the runs it re-execs via `os.Executable()` are pinned to one immutable build. **Reclamation still absent, and now worse.** Finding 2. |

Requirements 2, 3 and 4 remain solidly met and were re-verified: only `mcp-go` is
used, all six tools carry `mcp.WithDeferLoading(true)` (`mcp.go:169-208`), and
`get_pipeline_schema` returns `string(graph.Graph{}.Schema())` directly with the
`graph/parse_test.go` drift guard intact. Requirement 1 is now demonstrated
against an installed package that matches the reviewed source. This round's
findings are one defect the new shutdown code introduced, one it left behind, and
two carried forward.

---

## Finding 1 — The descendant-group kill is unguarded against the reap window that the leader kill is guarded against, so a recycled PID can direct SIGKILL at an arbitrary tree of unrelated process groups (issue)

`forceRunStop` applies exactly the right guard in one place and omits it in the
two that now matter more (`mcp.go:501-522`):

```go
501 func forceRunStop(run *managedRun) error {
502 	pid := run.command.Process.Pid
503 	descendantGroups, scanErr := descendantProcessGroups(pid)   // UNGUARDED
...
508 	leaderErr := run.command.Process.Kill()                      // reap-safe
512 	if leaderErr == nil {
513 		if err := killProcessGroup(pid); err != nil {            // GUARDED
...
517 	for _, processGroup := range descendantGroups {
518 		if err := killProcessGroup(processGroup); err != nil {   // UNGUARDED
```

Both call sites reach `forceRunStop` only past `waitForRunDone(run, 0)`
(`mcp.go:437` and `:609`), which keys on `run.done`. `run.done` is closed by
`waitForRun` at `mcp.go:323` — after `run.command.Wait()` returns at `:304`
(where `Wait4` reaps the pid and releases it to the OS), after two `Close()`
syscalls (`:305-306`), and after acquiring `run.mu` (`:308`), which a concurrent
`snapshotRun` or `steerRun` may be holding. Inside that window:

1. The pid is released and may be recycled by any new process on the system.
2. Line 503 runs `descendantProcessGroups(pid)` with **no status check at all**,
   so `ps` is asked for the descendants of whatever now holds that pid.
3. `descendantProcessGroups` (`mcp.go:533-579`) computes the full transitive
   closure of that unrelated process's children and collects **every distinct
   pgid** in it, filtered only by `pgid > 1 && pgid != rootPID`.
4. Lines 508-516 correctly do nothing — `Process.Kill()` returns
   `ErrProcessDone`, so the leader and its group are spared.
5. Line 517-521 then SIGKILLs every group from step 3 anyway, because the loop is
   outside the `leaderErr == nil` guard.

If the recycled pid belongs to a shell, a build driver, or a terminal
multiplexer, every job group under it dies. This is a strict amplification of the
hazard rounds 03 and 04 flagged: the previous worst case was one recycled group
that the server at least had a `Process` handle for; the new worst case is an
unbounded set of groups the server never owned, discovered by string-parsing
`ps`, killed with raw `syscall.Kill` that has no ownership check. `ESRCH` is
swallowed (`mcp.go:527-529`) so a mis-aimed kill that succeeds is indistinguishable
from a no-op.

The probability per invocation is small — the window is short — but the guard
that closes it already exists three lines above and costs nothing: move the scan
below the `Process.Kill()` call and put the descendant loop inside the same
`leaderErr == nil` branch. As written, the code reads as though the reap race was
fully handled when only the least dangerous third of it was.

## Finding 2 — The content-addressed launcher never reclaims anything, so every distinct build permanently costs ~14 MB (issue)

`scripts/tractor-mcp:13-20` names the output `tractor-<sha256>` and `mv`s the
freshly built candidate to it. Immutability is genuinely achieved — that is the
correct fix for round-04's skew — but nothing ever deletes an old build, and
nothing ever deleted the two previous schemes either. Live state after roughly
half an hour of development:

```text
$ du -sh ~/.cache/tractor/codex-plugin/        →  55M
-rwxr-xr-x  14417986  tractor                                   # pre-0d37941 scheme, orphaned
drwxr-xr-x            4016100978/tractor       (14417986)       # 0d37941 scheme, orphaned
-rwxr-xr-x  14435074  tractor-518ab34afc4c…
-rwxr-xr-x  14435122  tractor-5419336a32a3…
```

Two content addresses already exist from a single commit because the installed
snapshot and the worktree embed different build paths, so *every* reinstall adds
one and *every* source edit in the development checkout adds another — each a
permanent ~14 MB file under `$XDG_CACHE_HOME`. There is no pruning in the script,
no retention bound, no cleanup command, and no mention in `README.md:9-27`, which
tells the reader only that "subsequent launches reuse Go's build cache" — true of
Go's own cache, misleading about this one. A normal iteration cycle on this plugin
writes gigabytes into a directory the user is never told about. Keeping the last
N by mtime, or simply `exec "$candidate" mcp` and deleting on exit, would preserve
immutability without the accumulation.

## Finding 3 — The 500 ms cooperative window still guarantees SIGKILL for agent runs, and this commit documented that outcome instead of narrowing it (issue)

`gracefulRunStopTimeout = 500 * time.Millisecond` (`mcp.go:36`) is still measured
against a stop mechanism that is explicitly not prompt: SIGINT reaches
`root.go:205-213`, which calls `runner.Stop()`, a one-shot cooperative signal
checked between stages that cannot interrupt an in-flight agent turn. For any
`codergen` pipeline the window expires with certainty, so the second `stop_run`
and every session shutdown terminate the run by SIGKILL as the **default**.

This commit's response was to change the prose. `README.md:38-41` now reads
"the server first requests a cooperative stop and then terminates any remaining
run process tree within the MCP host's shutdown deadline" — accurate about
mechanism, and Finding 1's disposition confirms the termination now works — but
the consequences it papers over are untouched:

- `defer codexAdapter.Close()` / `defer claudeAdapter.Close()` (`root.go:164-167`)
  still never run, so agent sessions are never closed cleanly.
- `engine/store.go:175-182` is still a bare `os.WriteFile` with no
  temp-and-rename, so `manifest.json`, `stages/*/outcome.json`, `branches.json`
  and `error.json` can still be truncated mid-write by the now-*guaranteed*
  SIGKILL. `checkpoint.json` is safe (`saveCheckpoint`, `store.go:83-89`), so a
  resumed run survives but may read a half-written stage record. Making the kill
  more thorough, as this commit did, strictly increases how often that happens.
- `stop_run` still takes only `run_id` (`mcp.go:203-208`) with no force or
  timeout argument, and `stopRunOutput` (`mcp.go:131-134`) still carries only
  `run_id` and `status`, so a caller can neither ask for more patience nor tell a
  clean cooperative stop from a kill.

The tests still cannot expose it: the only pipeline they stop is `slowPipeline`,
a `tool` node whose stop path *is* prompt (`engine/tool.go:46-55`), so every test
finishes inside the graceful window.

## Finding 4 — Reconstructing the system process tree by parsing `ps` is unrequested infrastructure compensating for a detachment Tractor itself creates (issue)

`mcp.go:533-579` adds 47 lines that fork `ps -Ao pid=,ppid=,pgid=`, parse its
text output, and compute a transitive-closure process tree — on the
shutdown-critical path, inside a 500 ms + 1 s budget, once per run, in a
supervisor whose job is to stop processes it started itself.

It exists only because `engine/tool.go:65` sets `SysProcAttr{Setpgid: true}` on
every tool command, deliberately detaching it from the run's group. Tractor owns
both sides of that boundary, so the information is available at the source:
`engine/tool.go:66-75` already holds each tool's pgid in its `Cancel` closure,
and `engine/control.go` already publishes a `manifest.json` the MCP server reads
(`engine.LoadControlSocket`). Recording live tool pgids there — or simply not
detaching tool commands, since their `Cancel` closure already covers the timeout
and cooperative-stop cases — would make the supervisor read an authoritative list
instead of inferring one from a global snapshot of every process on the machine.

Concrete costs of the guessing approach, beyond Finding 1:

- **Silent leak on scan failure.** If `ps` is missing, sandboxed, or fails,
  `descendantProcessGroups` returns `nil, err` (`:535-537`); the error is
  collected but *no descendant is killed*, which is precisely the leak the code
  exists to prevent. The error surfaces only as `errors.Join` output on stderr at
  `main.go:9-12`, i.e. into a stream MCP hosts discard.
- **Snapshot staleness.** Groups created between the scan (`:503`) and the kills
  (`:517`) are missed — and the leader is killed in between, which is exactly when
  a supervised child is most likely to spawn a replacement or reparent.
- **Over-broad enumeration.** `ps -A` lists every user's processes; a descendant
  belonging to another uid yields `EPERM`, which `killProcessGroup` does *not*
  swallow (only `ESRCH` is, `:527-529`), so `stop_run` would fail with an error
  even though the run stopped.

---

## Nitpicks

- The shipped snapshot is one edit behind HEAD: `mcp.go:544` is
  `strings.Split(...)` there versus `strings.SplitSeq(...)` here. Functionally
  identical, but the reinstall was taken mid-edit, so "the installed package is
  HEAD" is not literally true.
- `CODEX-HOST-PROOF.md` no longer records any deferred-loading observation at all
  (the previous `"loading_deferred": true` self-report was dropped rather than
  replaced with a protocol-level one). Requirement 3 still rests on `mcp_test.go`
  and `CLAUDE-HOST-PROOF.md:11-25`.
- A third populated `logs_root` is now committed
  (`ephemeral/projects/tractor/mcp-plugin/live-workspace/.tractor/runs/20f379…/`),
  alongside `codex-cli-live-run/` and `codex-cli-live-run-v2/`; each is
  permanently un-rerunnable in place because of `requireFreshLogsRoot`
  (`mcp.go:697-709`).
- `scripts/tractor-mcp:13-17` has no branch for a host with neither `sha256sum`
  nor `shasum`; under `set -eu` the launcher dies with a bare shell error rather
  than a diagnostic.
- `waitForAllRuns` (`mcp.go:622-635`) still spawns a goroutine blocked on
  `<-run.done` and abandons it on every timeout return; it is called twice per
  shutdown.
- On signal shutdown `Listen` returns `context.Canceled`, which `errors.Join`
  propagates to `main.go:9-12`, printing `tractor: context canceled` and exiting
  1 — a clean teardown reported to the host as a crash.
- `steer_run`'s status precheck (`mcp.go:371-375`) still synthesizes
  `http_status: 409` although no HTTP exchange occurred.
- `s.runs` (`mcp.go:144`) is still never pruned.

## Outcome

material findings remain
