# Independent validation: Antigravity (`agy`) HarnessAdapter

Commit reviewed: `0b24d9eb95b21975f021d8df07163b569519abc9` ("Add Antigravity
agy harness adapter"), branch `codex/agy-harness-adapter`.

Native harness: `agy 1.1.14` at `~/.local/bin/agy` (matches design.md's
pinned version). Model identity observed directly in native `init`/`result`
stream lines during probing: `"model":"gemini-3.7-flash-low"`, e.g.
conversation `79c9ca9c-943e-4c54-9701-f1f09f705822`.

**Verdict: PASS.** The adapter is a real, working 90-95%-complete
integration: it builds, its own tests pass, and it drives a real Gemini
model through real `agy` processes to produce a correct observable
workspace effect via both `cmd/agent` and a full `cmd/tractor` graph run,
with correct checkpoint bindings and spec-shaped neutral events. One
material completeness gap was found (below) — it does not block the
adapter's functional correctness but does block the project's own stated
Definition of Done for this feature.

## What was run

- `go build ./...`, `go vet ./...` — clean.
- `go test ./harness/agy/... -v` — all 8 tests pass (categorization,
  repair-turn, timeout/interrupt, conversation-ID mismatch, event
  projection).
- `go test ./...` — full repo suite green (agent, tractor, engine,
  examples, graph, harness, harness/claude, harness/codex, internal/runlog,
  lint).
- Direct native probes of `agy` (outside the adapter) to check the
  `--add-dir` + `--new-project` / `--add-dir` + `--conversation` combination
  the adapter always sends. Both worked: workspace correctly rooted at the
  supplied dir, and a resumed turn found and extended a file the first turn
  wrote. (design.md's own P2 probe recorded `--add-dir .` *alone* failing
  fast; that finding does not apply to what the shipped code actually sends,
  and the project's own worklog documents the correction — `--add-dir` is
  required alongside `--new-project`/`--conversation` for reliable resumed
  writes. No bug here — investigated because it looked like a contradiction
  and turned out not to be one.)
- Real `go run ./cmd/agent --provider gemini --model gemini-3.7-flash-low`
  smoke in a fresh disposable workspace
  (`/private/tmp/.../scratchpad/agy-agent-smoke`): read `input.txt`
  containing an unpredictable nonce, wrote `output.txt`. Exit 0.
  `cmp input.txt output.txt` — byte-identical. Session ID
  `970d3d81-20eb-443a-b487-8e7822f3e69f`. Event segment
  `events/000001-agent.jsonl` contains exact-copy `user`, a matched
  `tool_call`/`tool_result` pair (`step-7`, `view_file`), a final
  `assistant`, and multiple `usage` events.
- Real `go run ./cmd/tractor run` of a minimal but complete graph
  (`codergen` → `tool`) in a fresh disposable git repo
  (`/private/tmp/.../scratchpad/agy-tractor-proof`), `defaults.llm_model:
  "gemini-3.7-flash-low"` (provider auto-detected as `gemini` → routed to
  `agy`):
  ```json
  {
    "name": "agy-proof-min", "start": "write",
    "nodes": [
      { "id": "write", "type": "codergen",
        "prompt": "Create a file named proof.txt ... exactly one line: AGY_PROOF_OK ...",
        "edges": [{ "to": "check", "condition": "The file has been written" }] },
      { "id": "check", "type": "tool",
        "tool_command": "grep -qx 'AGY_PROOF_OK' proof.txt",
        "on_success": "success", "on_error": "failure" }
    ]
  }
  ```
  `tractor validate` passed first; `tractor run` printed `COMPLETED`, exit 0.

## Live tractor-run evidence

- **Workspace effect**: `proof.txt` = exact bytes `AGY_PROOF_OK\n` (verified
  with `xxd` and `grep -qx`, matching the `check` tool node's own passing
  assertion in `stages/000002-check/tool.log` / `outcome.json`:
  `{"next":"success","notes":"exit 0: "}`).
- **Checkpoint bindings** (`checkpoint.json`): `sessions.write =
  {"harness":"agy","session_id":"5082190b-dcd3-4ed7-9e38-dde3e3216e8c","workdir":".../agy-tractor-proof"}`
  — exactly the `{harness, session_id, workdir}` shape spec 12.1 and
  design.md §7 require. `completed_nodes: ["write","check"]`.
- **Event log** (`events/000001-write.jsonl`, `events/index.jsonl`): opens
  with a `user` event carrying the exact initial prompt parts, a
  `tool_call`/`tool_result` pair (`step-7`, `write_to_file`), an `assistant`
  event, and `usage` events, each stamped with `node_id` and `ts` per spec
  12.4. (Two `assistant` events appear for the one turn — a narrative
  sentence plus the raw choice JSON echoed as text by agy itself. This
  matches design.md §13's documented residual risk, "`structured_output`
  extraction quirks... duplicated in `response` text" — harmless, not a
  bug, since only `structured_output` is consumed for routing.)
- `timeline.jsonl` shows a clean `PipelineStarted` → 2 `StageStarted`/
  `StageCompleted` pairs → `PipelineCompleted`, ~11.3s total.

## Material finding

**The spec §11.1 HarnessAdapter conformance program cannot run against
`agy` and no evidence for it was committed, though design.md commits to it
as Milestone 5's ship criterion.** `cmd/harness-conformance/main.go`'s
`selectAdapter` only has `case "codex"` and `case "claude"`; no `case
"agy"` was added in this commit (confirmed: the file does not appear in
`git show --stat HEAD`).

Reproduction:
```
$ go run ./cmd/harness-conformance -adapter agy -model gemini-3.7-flash-low -evidence /tmp/x.json
harness-conformance: unsupported adapter "agy"
```
No `conformance-agy.json` exists anywhere under
`ephemeral/projects/tractor/agy-harness-adapter/`, and design.md §9/
Milestone 6's live-proof pipeline (`proof-pipeline.json` + run-directory
evidence checked into the project dir) was likewise never committed —
`live-smoke.md` documents only the `cmd/agent` companion smoke, not a full
`tractor run`. The project's own worklog states plainly: "Completion
requires live proof through `tractor run` with an actual Gemini model...
tests alone are not proof," and design.md's Milestone 5/6 make both the
conformance evidence file and the tractor-run evidence file explicit ship
gates. Neither shipped in this commit.

This does not indicate a functional defect — the live proof I ran directly
above (this report's "Live tractor-run evidence" section) satisfies exactly
what Milestone 6 asked for, and scenario 1 of the conformance program
(real workspace turn) is what both my `cmd/agent` and `cmd/tractor` runs
exercised successfully. It is a real gap against the project's own
Definition of Done: nobody has run the actual required certification
program end-to-end against the real binary and captured that evidence, so
the adapter is not yet formally certified per spec 11.1.

**Smallest practical fix** (not applied — no code was changed by this
review): add an `agy` branch to `selectAdapter` in
`cmd/harness-conformance/main.go`, mirroring the existing `codex`/`claude`
cases —
```go
case "agy":
    command = "agy"
    args = []string{"--version"}
    construct = func() harness.HarnessAdapter { return agyharness.New() }
```
(plus the import and the scenario-5 `skipped_unsupported` steering gate
design.md §8 already specifies), then run `go run ./cmd/harness-conformance
-adapter agy -model gemini-3.7-flash-low -evidence
ephemeral/projects/tractor/agy-harness-adapter/conformance-agy.json` and
commit the evidence file plus a `proof-pipeline.json` + run-directory
excerpt for Milestone 6.

## Other things checked, no issues found

- Wiring: `harness/backend.go` `DefaultProviderRoutes()` adds `"gemini":
  "agy"`; `cmd/agent/main.go` and `cmd/tractor/root.go` both construct
  `agy.New()`, wire `SetStderr`, register it in the adapters map, and
  `defer Close()` — consistent with the claude/codex pattern.
- Error categorization table (`categorize`) matches design.md §6 and is
  exercised by `TestCategorize`, including the real observed "Eligibility
  check failed: INTERNAL (code 500)... can't connect to Gemini Code Assist"
  signature as retryable.
- Repair-turn, workdir immutability, conversation-ID mismatch rejection,
  and timeout→interrupt paths are all covered by fake-binary unit tests and
  behave as design.md describes; nothing in the live runs contradicted
  them.
- README.md's harness-choices list and the Antigravity paragraph accurately
  describe the shipped behavior (steering no-op, service-managed
  compaction, local structured-output validation with one repair turn).

## Evidence paths

- `/private/tmp/claude-501/.../scratchpad/agy-agent-smoke/` (input.txt,
  output.txt) and `agy-agent-smoke-logs/` (events, index).
- `/private/tmp/claude-501/.../scratchpad/agy-tractor-proof/`
  (proof-pipeline.json, proof.txt, git repo) and `agy-tractor-proof-logs/`
  (checkpoint.json, events/, stages/, timeline.jsonl, manifest.json).
- These paths are under this session's ephemeral scratchpad, not the repo,
  and are not committed evidence — see the Material Finding above for what
  the repo itself is still missing.
