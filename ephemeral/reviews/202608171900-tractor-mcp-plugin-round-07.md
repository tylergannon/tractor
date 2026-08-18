# Adversarial Review — Tractor Codex MCP Plugin — Round 07

Date: 2026-08-17
Branch: `codex/tractor-mcp-plugin` @ `5183505` ("Document plugin installation and use")
Working tree: clean

## Review target

The complete Tractor MCP plugin work, re-reviewed against the authoritative
sources used in rounds 01–06 plus the caller's newly restated requirement.

Reconstructed requirements:

1. Publish Tractor as a Codex **plugin-provided** stdio MCP server.
2. Implement it with `github.com/mark3labs/mcp-go` — no substitute SDK, no
   hand-rolled protocol or transport.
3. Compact, deferred tool definitions.
4. Graph schema and validation automatically synchronized with Tractor's graph
   language.
5. *(new this round)* README plugin information and an `llms.txt` for agents
   installing and using Tractor, **without pipeline-authoring advice**.

The launch prompt supplied the artifact path, the read-only boundary, and the
new documentation requirement. It did not narrow the defect classes, files, or
subject matter, and did not predict a verdict, so no narrowing had to be
ignored. Scope was derived from the authoritative sources above, from
`ephemeral/worklog/202608171718-tractor-mcp-plugin.md`, and from the current
artifacts; documentation, marketplace, implementation, and proof were all
inspected.

## Evidence inspected

Implementation and tests
- `cmd/tractor/mcp.go` (full), `cmd/tractor/mcp_test.go`, `cmd/tractor/root.go`
- `engine/tool.go`, `engine/runner.go`, `engine/parallel.go`, `engine/store.go`
- `scripts/tractor-mcp`, `.mcp.json`, `.codex-plugin/plugin.json`
- `.agents/plugins/marketplace.json`, `skills/orchestrate-attractor-loops/`

Documentation
- `README.md` (all 49 lines), `llms.txt` (all 57 lines)

Diff
- `git diff defd545..HEAD`, and per-commit review of `785626f`, `c0cb24e`,
  `3cd8abb`, `3707aad`, `5d0f73f`, `5183505`

Proof
- `ephemeral/projects/tractor/mcp-plugin/{REPORT.md, CODEX-HOST-PROOF.md,
  CLAUDE-HOST-PROOF.md, smoke.go}` and the committed run ledgers

Host state
- `codex plugin list`, `codex plugin marketplace list`, `~/.codex/config.toml`
  (`[plugins."tractor@personal"] enabled = true`), the installed snapshot at
  `~/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818004816`,
  `~/.agents/plugins/marketplace.json`, `/Users/tyler/plugins/tractor`,
  `~/.cache/tractor/codex-plugin/`
- `origin/main` @ `06b8621`

Executed
- `go build ./...` — clean; `go vet ./...` — clean; `go test ./...` — all
  packages pass (`cmd/tractor` 2.829s)
- Full JSON-RPC `initialize` + `tools/list` handshake through the *installed*
  launcher under `/bin/sh` (bash), `/bin/dash`, and `/bin/zsh` — all three
  returned the same 5599-byte, six-tool result
- `go build` + `go tool buildid` repeated three times — byte-identical binary
  and identical build ID, so the content-addressed cache is stable
- `diff -r` of the installed snapshot against HEAD — only `plugin.json`
  (version stamp) differs
- Live orphan reproduction against a real MCP session (below)

## Disposition of round-06 findings

| # | Round-06 finding | Status |
|---|---|---|
| 1 | **critical** — `"$binary" mcp <&0 &` loses stdin on POSIX shells | **Fixed.** `3707aad` restored `exec "$binary" mcp` (`scripts/tractor-mcp:18`). Re-verified end to end under bash, dash, and zsh. |
| 2 | Removing `exec` reintroduced intermediate-process orphans | **Fixed** by the same commit; the launcher no longer backgrounds anything and holds no traps past `mv`. |
| 3 | SIGSTOP freezes only the leader | **Unchanged but now consistent.** `forceRunStop` (`cmd/tractor/mcp.go:500-513`) still freezes only the leader before `kill(-pgid)`. See Finding 3 for the consequence that is now *documented* as solved. |
| 4 | `ps`-parsing supervision is unrequested infrastructure | **Fixed.** `descendantProcessGroups` was deleted in `3707aad`; shutdown now relies on the engine's own cancel path. This was the right call — the residual defect is that the README was not brought back down to what the code does. |
| 5 | 500 ms window; stray `writeJSON` temp files | **Partly carried.** `gracefulRunStopTimeout` is still 500 ms (`:35`) and `requireFreshLogsRoot` (`:642`) still rejects any non-empty logs root. Not re-reported at full weight; folded into Finding 5. |

Requirements 2, 3, and 4 remain satisfied and were re-verified this round:
`mcp-go` is the only MCP dependency; all six tools carry
`mcp.WithDeferLoading(true)` and `tools/list` emits `defer_loading: true` for
each; `get_pipeline_schema` returns `string(graph.Graph{}.Schema())` directly
(`:171`) with the committed `jsonschema/Graph.json.sum` drift guard still in
`graph/parse_test.go`.

Requirement 5 is partially met: `llms.txt` exists, is agent-oriented, and
correctly contains **no** pipeline-authoring advice — the decision recorded in
the worklog was honoured. What it documents, however, does not work.

## Findings

### 1. critical — The published install instructions cannot work; the only proven install came from an undocumented hand-made local copy

`README.md:18-23` and `llms.txt:13-18` both instruct an agent to run:

```sh
codex plugin marketplace add tylergannon/tractor
codex plugin add tractor@tractor
```

`.agents/plugins/marketplace.json` (the manifest that `codex plugin marketplace
add tylergannon/tractor` would read) pins the plugin source to
`https://github.com/tylergannon/tractor.git`, `"ref": "main"`.

`origin/main` is `06b8621` "Use generated YAML pipeline decoding". It contains
none of this work:

```
.codex-plugin/plugin.json          ABSENT
.mcp.json                          ABSENT
.agents/plugins/marketplace.json   ABSENT
scripts/tractor-mcp                ABSENT
llms.txt                           ABSENT
cmd/tractor/mcp.go                 ABSENT
```

(`git cat-file -e origin/main:<path>` for each; the branch is 12 commits ahead
of `origin/main`.)

So both documented commands fail, and they fail in the worst order: the
*marketplace* manifest is itself absent from `main`, so command one cannot even
discover a plugin, and command two therefore has no `tractor@tractor` to add.
Even after a merge, `ref: "main"` plus a private repository
(`gh repo view` → `"isPrivate": true`) means an unauthenticated
`https://github.com/...` clone is what the manifest asks Codex to perform;
`llms.txt:11` acknowledges "Repository access when the GitHub repository is
private" but the manifest offers no mechanism for it.

Meanwhile the install that every proof artifact rests on is a *different* one.
`codex plugin list` reports:

```
PLUGIN            STATUS              VERSION                     PATH
tractor@personal  installed, enabled  0.1.0+codex.20260818004816  /Users/tyler/plugins/tractor
```

`/Users/tyler/plugins/tractor` is a hand-made directory copy of the worktree
(already stale: `README.md` and `CODEX-HOST-PROOF.md` there differ from HEAD).
It is backed by `~/.agents/plugins/marketplace.json`, a *host* file with
`"source": "local", "path": "./plugins/tractor"` that exists nowhere in the
repository. Neither the marketplace name `personal`, the copy step, nor the
local-source manifest appears in `README.md`, `llms.txt`, or
`CODEX-HOST-PROOF.md`.

Impact: requirement 1 ("publish Tractor as a Codex plugin-provided stdio MCP
server") and the new requirement 5 are both undermined. The plugin genuinely
runs — that part is proven — but it is only installable by reproducing an
undocumented local setup, and the one procedure that *is* published to agents
is guaranteed to fail. `CODEX-HOST-PROOF.md` proves the server works once
installed; it does not prove, and does not claim to prove, the documented
installation.

### 2. issue — The plugin package ships the team's internal working files and an unrelated internal skill

The plugin root is the repository root, and there is no include/exclude
manifest, so the entire tree is the payload. Measured on the installed
snapshot at `~/.codex/plugins/cache/personal/tractor/0.1.0+codex.20260818004816`:

- `ephemeral/` is 2.8 MB across 424 files — 75 % of the 3.7 MB package. It
  contains `ephemeral/reviews/` (36 internal adversarial-review documents,
  including rounds 01–06 of this very review, which enumerate the project's
  defects), `ephemeral/worklog/`, and `ephemeral/projects/` agent transcripts
  and run ledgers carrying absolute local paths such as
  `/Users/tyler/src/.worktrees/tractor/...`.
- `.codex-plugin/plugin.json:12` declares `"skills": "./skills/"`, and the only
  skill present is `skills/orchestrate-attractor-loops/SKILL.md`. Its
  description is "Manually shepherd a substantial repository change through
  independent design, critique, implementation, review, and live validation
  loops." It is a description of the authors' own subagent-management process.
  It says nothing about running Tractor pipelines.

The `ephemeral/` bytes are a hygiene problem. The skill is worse than that,
because Codex surfaces plugin skills to the model: every session that enables
`tractor@personal` is offered an internal development-process playbook as if it
were Tractor product guidance. Note also that `llms.txt` — the artifact the
caller asked for specifically so installing agents have a reference — is
referenced only from `README.md:25`; nothing in `plugin.json`, `.mcp.json`, or
the bundled skill points at it, so the agents it targets are not routed to it.

Impact: the published artifact distributes internal critique and local
filesystem paths to every installer, and injects an off-topic skill into the
host's model context.

### 3. issue — `README.md:46-49` claims the shutdown terminates the run *process tree*; it terminates one process group, and I reproduced the survivor

`README.md:46-49`:

> Runs belong to the stdio server session. When that session closes, the server
> first requests a cooperative stop and then terminates any remaining run
> process **tree** within the MCP host's shutdown deadline, preventing an
> unloaded plugin from leaving unsupervised work in the background.

Two claims there are not true of the code as it now stands.

*"process tree"*: `forceRunStop` (`cmd/tractor/mcp.go:500-513`) SIGSTOPs the run
leader and then calls `killProcessGroup(pid)` — `kill(-pid, SIGKILL)` against
the leader's **own** group only. `descendantProcessGroups`, the scan that used
to widen this, was deleted in `3707aad` (correctly, per round-06 Finding 4).
Tool nodes deliberately run in their own process groups
(`engine/tool.go:65`), so the leader's group kill never reaches them; they are
reached only by the cooperative in-process cancel
(`engine/tool.go` → `cancel(errToolStopped)` → `command.Cancel` →
`kill(-toolpgid, SIGKILL)`), and that cancel likewise stops at the tool's own
group.

*"within the MCP host's shutdown deadline"*: the budget is a fixed
`gracefulRunStopTimeout = 500 * time.Millisecond` plus
`forcedRunStopTimeout = time.Second` (`:35-36`). It is a compile-time constant
unrelated to, and unnegotiated with, any host deadline.

Reproduction (real MCP session against a HEAD build, no test harness):

Pipeline node:

```json
{"id":"work","type":"tool",
 "tool_command":"echo $$ > /tmp/r7/tool.pid; nohup perl -e 'setpgrp(0,0); ...; exec @ARGV' -- sleep 300 >/dev/null 2>&1 & sleep 300"}
```

Driver: `initialize` → `notifications/initialized` → `start_run` → wait for the
grandchild to record its pid → close the server's stdin (session close →
`shutdownRuns`).

Result:

```
START: {"run_id":"f9b6ae0f6b829a138a2fffd79d1d632a","pid":94116,"status":"RUNNING", ...}
server exited rc=0 after 0.00s
=== survivors ===
tool.pid=94117 (gone)
gc.pid=94118  94118  1  94118  sleep 300
```

The tool's own shell (94117) died. Its detached grandchild 94118 — its own
process group, now reparented to `init` (ppid 1) — survived the session close
and was still running `sleep 300` afterwards.

Impact: the plugin's headline safety promise to an operator reading the README
is stronger than what it delivers. The right fix is almost certainly to weaken
the sentence to what `3707aad` deliberately chose to implement (cooperative
stop plus a leader-group kill), not to reinstate the `ps` scan that rounds 05
and 06 flagged as unrequested infrastructure.

### 4. issue — Tool annotations invert the risk ordering; `start_run` advertises itself as non-destructive

From the live `tools/list` response through the installed launcher:

```
get_pipeline_schema  readOnlyHint: true   destructiveHint: true
get_run_status       readOnlyHint: true   destructiveHint: true
validate_pipeline    readOnlyHint: true   destructiveHint: true
start_run            readOnlyHint: false  destructiveHint: false
steer_run            readOnlyHint: false  destructiveHint: false
stop_run             readOnlyHint: false  destructiveHint: true
```

Source: `cmd/tractor/mcp.go:186` sets
`mcp.WithDestructiveHintAnnotation(false)` on `start_run`; `:170`, `:179`, and
`:193` set only `ReadOnlyHint`, leaving `destructiveHint` at its serialized
default of `true`.

`start_run` re-execs `tractor run` (`:266-280`), which drives agent harnesses
that write the workspace, calls `freezeGitWorkspaceWithStop` /
`createBranchWorktreesWithStop` to create git worktrees (`engine/parallel.go:66-72`),
and executes arbitrary operator-supplied shell in `tool_command` nodes
(`engine/tool.go:64`). It is the single most destructive tool in the surface and
is the only one explicitly labelled non-destructive. Conversely the three pure
readers are labelled destructive.

Impact: hosts use these annotations to decide what may run without
confirmation. The annotation set tells a host to gate schema reads and to let
an unattended multi-agent pipeline that rewrites a git workspace through
unprompted.

### 5. nitpick — `llms.txt` omits the parts of the operational contract an installing agent will actually collide with

`llms.txt:37` states the tool contract as: "`validate_pipeline` and `start_run`
accept `pipeline_path` and an optional `workdir`." The implementation's contract
is larger and has failure modes an agent cannot discover from the document:

- `start_run` also accepts `logs_root` and `resume` (`:238-254`), neither
  mentioned.
- `start_run` rejects a `logs_root` that already has any entry
  (`requireFreshLogsRoot`, `:642-655`), so an agent that reuses a directory gets
  an opaque failure.
- `stop_run` is described as "request that a run stop" (`llms.txt:34`) with no
  mention that a repeat call escalates to `SIGSTOP` + `SIGKILL` after the fixed
  500 ms window (`:426-446`), i.e. that calling it twice is destructive.
- `steer_run` is described as "send text to the run's active steerable turn"
  (`llms.txt:33`) with no mention that it returns `accepted: false` with
  `http_status: 409` when no steerable turn is active (`:361-380`) — the case
  the project's own test asserts.

Impact: modest, but this is precisely the audience the artifact was written for.

## Outcome

material findings remain
