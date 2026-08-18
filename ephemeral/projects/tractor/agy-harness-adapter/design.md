# Design: Antigravity (`agy`) HarnessAdapter

Status: design complete, grounded in live probes against the installed CLI.
Authority for the native surface: https://antigravity.google/docs/cli/headless
(plus `agy --help`). Harness under adaptation: **Google Antigravity CLI
`agy` 1.1.14** at `~/.local/bin/agy`, authenticated via its own keyring
session (one-time interactive sign-in already done on this machine).

Goal: a small, production-useful `HarnessAdapter` that routes provider
`gemini` through `agy`, passes the adapter conformance program for every
scenario the harness can support, and is proven by a real `tractor run`
using a Gemini model with observable workspace effects. No provider-neutral
interface changes, no spec changes — every contract needed already exists in
`harness/contract.go` and docs/spec.md Sections 11.1 and 12.

---

## 1. Verified native behavior (live probes, 2026-08-18)

All probes ran the real binary in scratch directories; fixtures worth
keeping are re-captured in Milestone 1. These facts are the design's load-
bearing evidence — each maps to a design decision below.

| # | Probe | Result |
|---|-------|--------|
| P1 | `agy -p … --output-format stream-json --json-schema … --dangerously-skip-permissions` | NDJSON stream: one `{"event":"init","conversation_id":…,"init":{…}}` first line, many `{"event":"step_update","step_update":{…}}`, one final `{"event":"result","result":{…}}`. `result` carries `conversation_id, status, response, error, duration_seconds, num_turns, structured_output, json_schema, usage`. Exit code 0 on SUCCESS. |
| P1 | Workspace binding, bare invocation | **agy ignored the invocation cwd**: the agent operated in `~/.gemini/antigravity-cli/scratch` (its default project workspace), not the cwd. Files were created in the wrong place. |
| P2 | `--add-dir .` | Run failed fast: `status ERROR, error "timeout waiting for response"`, exit 1. Not a usable workspace mechanism in print mode. |
| P3 | `--new-project` with cwd = target dir | **Workspace correctly rooted at cwd.** Agent read `data.txt`, wrote `done.txt` beside it, returned the token through `structured_output`. Exit 0. |
| P3b | Resume: `--conversation <id>` (id from the init event) | Memory intact — returned the earlier token without re-reading files. Project/workspace association persists with the conversation. |
| P3b | Schema enforcement on resume | **agy does not strictly validate structured output.** New schema (`{"remembered"}`) was passed and echoed back in `init`/`result` `json_schema`, but `structured_output` came back in the *previous* turn's shape (`{"token"}`). Adapter-side validation and a bounded repair turn are mandatory. |
| P4 | SIGINT mid-turn (agent had written its started marker, was in `sleep 60`) | agy exits promptly; the stream ends with a `result` whose `status` is `ERROR` / `error: "timeout waiting for response"`. **The conversation survives**: a follow-up `--conversation` turn succeeded and recalled the file created before the interrupt. |
| — | `agy models` | Gemini slugs embed a reasoning tier: `gemini-3.7-flash-{high,medium,low}`, `gemini-3.1-pro-{high,low}`, etc. `--effort low\|medium\|high` also exists. |
| — | Flags confirmed in `--help` | `--print/-p`, `--output-format stream-json`, `--json-schema` (inline string or file path), `--conversation`, `--continue`, `--model`, `--effort`, `--print-timeout` (default 5m), `--new-project`, `--project`, `--dangerously-skip-permissions`, `--sandbox`, `--input-format`. |

Step_update variants observed: `step_type` ∈ `user_input`, `agent_response`
(with `text_delta` fragments across ACTIVE lines, terminal DONE line, and
per-step `usage`), `tool` (ACTIVE carries `tool_info{name, parameters}`,
DONE adds `tool_info.output` and `duration_seconds`), `checkpoint`,
`finish`, `unknown`. `state` ∈ `ACTIVE`, `DONE`. No thinking text is
streamed (thinking tokens appear only in usage counters).

## 2. Architecture: exec-per-turn over `--conversation`

One native process per operation, exactly like the Claude adapter's
process-per-turn model — no daemon, no app-server, no persistent stdin
protocol.

- **First turn of a session**: `agy -p <prompt> --output-format stream-json
  --new-project --dangerously-skip-permissions --model <m> [--effort <e>]
  --json-schema <schema-file> --print-timeout <t>` with `Cmd.Dir = workdir`.
  `--new-project` is what binds the agent's workspace to the workdir (P1 vs
  P3); it is non-negotiable.
- **Every later turn**: same invocation with `--conversation <id>` instead
  of `--new-project`, still `Cmd.Dir = workdir`.
- **Durable state is the harness's** (spec 12.2): the conversation ID is
  agy's own, so a reconstructed adapter (new process, restored bindings)
  resumes with nothing but the ID — conformance scenario 2 falls out of P3b.

Rejected alternative: holding one `--input-format stream-json` process per
session. It would pin `--model`/`--effort`/`--json-schema` for the process
lifetime (they are CLI-session flags, but turns may change model and always
change schema), and the docs scope `--json-schema` under stream input to
"only the final result". Exec-per-turn keeps every per-turn knob per-turn
and makes interrupt/timeout a process kill, which P4 proves is safe.

### CreateSession: a real handshake turn

agy has no "create empty conversation" verb and no `--session-id` analog:
conversation IDs are minted by the service at first run. Since the returned
session ID must be durable across adapter reconstruction and adapters may
hold no private durable state, `CreateSession(model, workdir)` runs one
cheap real turn:

```
agy -p "Reply with the single word OK. Do not use any tools." \
    --output-format json --new-project --dangerously-skip-permissions \
    --model <model> --print-timeout 2m
```

in `workdir`, parses `conversation_id` from the JSON result, verifies
`status == "SUCCESS"`, and returns the conversation ID. Cost: one short
turn (system-prompt tokens are largely cache-read per P1 usage). Benefits:
the ID is native and durable; an invalid model fails here with a terminal
Error (agy exits non-zero on unknown model slugs — scenario 4's
create-path check); the project/workspace binding is established before the
first codergen prompt. The adapter records `{workdir}` in an in-memory
session state exactly like the Claude adapter (`states[id] =
{workdir, …}`) for workdir-immutability and active-turn checks.

This deliberately replaces the old plan's `pending:<uuid>` late-binding
scheme and its proposed provider-neutral `BindingUpdated` adoption hook —
machinery rejected below (§10).

## 3. Package layout and file ownership

New package `harness/agy` (module-internal, mirrors `harness/claude` and
`harness/codex` in shape and size — target ≈900 lines + tests):

| File | Owns |
|------|------|
| `harness/agy/adapter.go` | `Adapter` struct, `New()`, `SetStderr`, `CreateSession`, `RunTurn`, `Steer`, `Interrupt`, `Compact`, `Close`; session state map (`opMu`/`mu`, workdir, active turn); error categorization (`categorize`, `terminal`, `retryable`, `interrupted`). |
| `harness/agy/exec.go` | Process construction and lifecycle: argv assembly (never a shell), schema temp-file management, `Cmd.Dir`, stdout/stderr plumbing, kill (SIGINT then SIGKILL after grace), the per-turn run loop that feeds the decoder and applies timeout/interrupt. A `runnerConfig{binary string, baseArgs []string}` seam so tests substitute a fake binary. |
| `harness/agy/stream.go` | Hand-written NDJSON structs (`envelope`, `initPayload`, `stepUpdate`, `toolInfo`, `resultPayload`, `usagePayload`) and the line decoder. Tolerant of unknown fields and unknown `step_type`/`event` values (open set). |
| `harness/agy/events.go` | `eventProjector`: step_update → neutral events (§5), delta accumulation, at-most-once emission per step, synthetic `call_id`s. |
| `harness/agy/adapter_test.go` | Unit tests against a fake `agy` (a test helper binary via `go test -run TestHelperProcess` pattern or a generated script) replaying captured NDJSON fixtures; categorization, projection, validation-repair, timeout, interrupt tests. |
| `harness/agy/testdata/*.ndjson` | Sanitized captured fixtures from Milestone 1 probes (success, tool-heavy, error result, interrupted tail). |
| `harness/backend.go` | One-line change: `DefaultProviderRoutes()` gains `"gemini": "agy"`. |
| `cmd/tractor/root.go` | Register the adapter: construct `agy.New()`, add `"agy"` to the adapters map, `SetStderr`, `defer Close()`. `resolveHarness` needs no change — it already flows through `DetectProvider` (which returns `gemini` for `gemini*` models) and `DefaultProviderRoutes`. |
| `cmd/harness-conformance/main.go` | Add `case "agy"` to `selectAdapter` (`agy --version` for the version string); per-adapter scenario gating for steering (§8). |
| `examples/` (or `ephemeral/projects/tractor/agy-harness-adapter/`) | The live-proof pipeline JSON (§9). |

No changes to `harness/contract.go`, `harness/validate.go`,
`harness/result.go`, `engine/`, `graph/`, `lint/`, or `docs/spec.md`.

## 4. RunTurn control flow

```
RunTurn(input, onEvent):
  ValidateRunTurnInput; NewResultValidator(input.OutputSchema)
  state := states[input.SessionID]          // unknown ID → terminal Error
  state.opMu.Lock()                          // serializes turns per session (scenario 3)
  enforce workdir immutability (terminal Error on change, like claude)
  attempt := runOnce(resumeArgs, input.Parts prompt, onEvent)
  if attempt.result nonconforming:
      repair := runOnce(resumeArgs, repairPrompt(validationError), onEvent)  // one bounded retry
      validate repair or return terminal Error
  return validated Result
```

`runOnce` builds argv:

- `-p <joined parts>` — parts joined with `\n\n` (same as claude's
  `joinParts`); the projector emits the `user` event itself before spawn.
- `--output-format stream-json`
- `--dangerously-skip-permissions` — parity with claude's bypass-all and
  codex's `danger-full-access`; Tractor pipelines are unattended.
- `--conversation <sessionID>` (CreateSession already burned the
  `--new-project` first turn, so RunTurn always resumes).
- `--model <input.Model>`; `--effort <input.ReasoningEffort>` when
  non-empty (Gemini slugs already encode a tier; passing `--effort` too is
  accepted by the CLI and keeps the contract uniform).
- `--json-schema <path>` — the exact `input.OutputSchema` bytes written to
  a 0600 temp file under the session's state dir, removed after the turn.
  A file avoids argv-length and quoting hazards for large enum schemas.
- `--print-timeout`: `input.Timeout + 30s` when `input.Timeout > 0`, else
  `24h`. **The adapter's own timer is the enforcement authority** (spec
  12.2 note 8): on expiry it signals the process (SIGINT, then SIGKILL
  after a 5s grace), and `RunTurn` returns `interrupted`. agy's own
  print-timeout is only a backstop margin so the two never race.

The turn loop reads stdout line-by-line through `stream.go`, hands each
decoded item to the projector, and finishes when the `result` line arrives
or the process exits:

- `result.status == "SUCCESS"` with `structured_output` present → marshal
  `structured_output`, validate against the exact schema, return.
- `SUCCESS` without `structured_output`, or validation failure → the repair
  path (below), then terminal Error.
- `CANCELED` / `INTERRUPTED`, or the adapter's interrupt/timeout flag is
  set → `interrupted` Error.
- `ERROR` → categorize `result.error` (§6).
- Process exit without a `result` line → `interrupted` if we signaled it,
  else `retryable` ("agy stream ended without a result").
- Stream decode failure on a line → skip unknown shapes silently (open
  set); a malformed final state surfaces as the no-result case.

### The repair turn

P3b proved agy will happily return non-schema JSON. One bounded repair per
`RunTurn`: re-invoke with `--conversation`, same schema file, prompt:

> Your previous structured output did not conform to the required JSON
> schema: `<validation error>`. Respond again. Output only a JSON object
> conforming exactly to the schema. Do not run any tools.

Validate again; on second failure return
`terminal("agy result does not conform to the supplied schema: …")`. The
repair turn streams into the same `onEvent` (it is part of the same logical
turn) and respects the remaining timeout budget. This satisfies spec 12.2
note 5's "follow-up evaluation turn" option with the simplest reliable
mechanism.

## 5. Event normalization (native → spec 12.3)

The projector keys accumulation by `step_index` and emits each logical item
at most once:

| Native | Neutral event |
|--------|---------------|
| (adapter, before spawn) | `user {parts}` — exact copy of the turn's parts array; a repair turn emits its own `user` with the repair prompt. Segment stays a self-contained transcript. |
| `init` | Consumed internally (conversation_id sanity check: mismatch with the resumed session ID → terminal Error). Not emitted. |
| `step_update` `agent_response`: `text_delta` fragments on ACTIVE/DONE lines | Concatenate per `step_index`; on the DONE line emit one `assistant {text}` (skip if empty). Never emit deltas (spec 12.3 note 1). |
| `step_update` `agent_response` DONE with `usage` | `usage {…}` verbatim per step (optional event, provider counters as-is). |
| `step_update` `tool` first line carrying `tool_info{name, parameters}` | `tool_call {call_id: "step-<step_index>", name, args: parameters}`. Synthetic call_id — agy has no native one; `step_index` is unique within a turn and pairs the result. |
| `step_update` `tool` DONE | `tool_result {call_id: "step-<step_index>", output: tool_info.output}` (include `tool_info.error` in the output object when present). If the DONE line is the first sighting of the step, emit the `tool_call` first, then the result. |
| `step_update` `user_input`, `checkpoint`, `finish`, `unknown` | Dropped. (`user_input` duplicates the adapter-emitted `user`; the rest are agy bookkeeping.) |
| `result.usage` | Final `usage {…}` verbatim. |

No `thinking` events: agy streams no reasoning text (only token counts).
Spec 12.3 marks the type set open and consumers ignore what they don't
see; nothing depends on thinking events.

## 6. Error categorization

Single `categorize(message, wasInterrupted)` mirroring the claude adapter:

| Signal | Category |
|--------|----------|
| Adapter-initiated interrupt/timeout, `context.Canceled/DeadlineExceeded`, `status CANCELED/INTERRUPTED` | `interrupted` |
| Message markers: `rate limit`, `quota`, `resource exhausted`, `overloaded`, `unavailable`, `temporarily`, `connection reset`, `broken pipe`, `unexpected eof`, `timeout waiting for response`* | `retryable` |
| Markers: `unknown model`, `invalid`, `not found`, `permission`, `unauthorized`, `unauthenticated`, `schema` | `terminal` |
| Unmatched | `retryable` (consistent with claude's default; a re-run is cheaper than a wrongly killed pipeline) |

\* `timeout waiting for response` is agy's own print-timeout wording. When
the adapter's timer fired we already classified `interrupted` before
reading the result; when agy produced it spontaneously (P2 showed it can,
within seconds) a retry is the right response.

Unknown session ID → `terminal` before any spawn (state-map miss), so
scenario 4 sees no task activity. Nonexistent model → terminal at
`CreateSession` (agy exits non-zero, stderr captured into the message).

## 7. Contract semantics: sessions, steer, compact, interrupt

- **Sessions/resume.** Binding is `{harness: "agy", session_id:
  <conversation_id>, workdir}` via the unchanged backend. Checkpoint resume
  restores bindings; a reconstructed adapter lazily accepts a session ID it
  has never seen *only* via its state map — which starts empty — so
  restored sessions need re-registration. Resolution: `RunTurn` on an
  unknown ID creates state on demand **iff** the turn can prove the session
  is real — the same approach the claude adapter uses (`state()` creates
  missing entries; a bogus ID then fails naturally when `--conversation`
  rejects it, mapping to terminal via the `not found` marker). Workdir
  immutability enforced as in claude: first turn's workdir sticks, change →
  terminal Error (the backend's staleness logic opens fresh sessions
  instead — spec 12.1 already handles worktree rebinding above the
  adapter).
- **Steer: documented no-op.** Print mode has no channel into a live turn —
  stdin is not connected to the running conversation, and a concurrent
  `--conversation` invocation would collide with the active turn rather
  than inject into it. `Steer(sessionID, parts)` validates parts and
  returns doing nothing, which the void best-effort contract permits (spec
  12.2 note 6: the adapter MUST NOT deliver to a non-running session; a
  no-op never violates a MUST). Consequences and mitigations: supervisors
  and `POST /steer` degrade to fire-and-forget messages that never arrive
  on gemini-routed nodes; the backend still returns `accepted` (it cannot
  know). This is the accepted 90% cut for v1 — pipelines wanting
  supervision-with-teeth route those nodes to claude/codex.
- **Compact: successful no-op.** Spec 5.4 delegates the meaning of
  compaction entirely to the harness; agy exposes no compaction verb and
  manages conversation context service-side (P3b: resumes work with full
  memory; long conversations are its problem, not ours). `Compact` returns
  `nil` after enforcing the checkable guards: unknown session → terminal;
  active turn on the session → terminal ("cannot compact an active
  session"); workdir change → terminal. This keeps the engine's default
  `fidelity: compacted` working unmodified for gemini nodes — the
  compact-then-prompt sequence is a no-op followed by the resume turn.
  (The old plan's "reject compacted fidelity + terminal-error Compact"
  would have made every default-fidelity gemini loop fail; rejected.)
- **Interrupt.** `Interrupt(sessionID)` signals the active turn's process
  (SIGINT; SIGKILL after 5s if still alive) and returns immediately. The
  blocked `RunTurn` observes the flag and returns `interrupted`. P4 proves
  prompt exit and post-interrupt session usability. Timeout enforcement
  reuses the same kill path (spec 12.2 note 8). `Close()` interrupts all
  active turns.

## 8. Conformance program

Add `-adapter agy` (`agy --version` → native version). Scenario mapping:

| Scenario | Expectation for agy |
|----------|--------------------|
| 1 real_workspace_turn | Pass as-is (P3 is a manual dry run of exactly this). |
| 2 continuation_and_reconstruction | Pass as-is (P3b; conversation ID is native and durable). |
| 3 isolation_and_serialization | Pass: distinct conversations = distinct processes/workdirs; per-session `opMu` serializes same-session turns. |
| 4 invalid_inputs_and_public_errors | Pass: unknown session → terminal pre-spawn or via `--conversation` rejection; bad model → terminal at CreateSession. |
| 5 steering | **Not supportable** (no live-injection channel). The program gains a per-adapter `unsupported` set: for agy, the steering scenario runs only its second half (steering a never-run session leaves the next turn's stream free of queued steering events — a real assertion the no-op must satisfy) and the report marks the scenario `"skipped_unsupported": true` with the reason string. The executable still exits 0 for agy when everything else passes, and the report is explicit that full 11.1 certification is not claimed — the steering row is the documented gap. This is a change to our own test harness's bookkeeping, not to the spec or the adapter contract. |
| 6 interruption_and_timeout | Pass (P4 + timer design). Bounds: interrupt return < 1s, `run_turn` interrupted return well under the long task's duration, later normal turn succeeds. |
| 7 compaction | Pass with no-op semantics: remembered value survives (trivially — same conversation), compact-during-active-turn → terminal without disrupting the turn, unknown session → terminal. |
| 8 backend_supervisor | Pass: pure backend orchestration over the adapter; supervisor turns are ordinary resumed turns. |

Run command (Milestone 5):
`go run ./cmd/harness-conformance -adapter agy -model gemini-3.7-flash-low
-evidence ephemeral/projects/tractor/agy-harness-adapter/conformance-agy.json`
— flash-low keeps the run cheap; a spot re-run with `gemini-3.1-pro-low`
guards against model-specific structured-output drift.

## 9. Live proof: a real tractor run

Pipeline (checked into the project dir as `proof-pipeline.json`), run in a
disposable git repo:

```json
{
  "name": "agy-proof",
  "goal": "Prove the Antigravity adapter end to end",
  "defaults": { "llm_model": "gemini-3.7-flash-low", "timeout": "10m" },
  "start": "implement",
  "nodes": [
    { "id": "implement", "type": "codergen", "max_visits": 3,
      "prompt": "Create greet.sh in the workspace: a bash script that prints exactly 'hello from antigravity'. chmod +x it.",
      "edges": [
        { "to": "check", "condition": "Script written; verify it" },
        { "to": "failure", "condition": "Cannot complete the task" } ] },
    { "id": "check", "type": "tool",
      "tool_command": "./greet.sh | grep -qx 'hello from antigravity'",
      "on_success": "report", "on_error": "implement" },
    { "id": "report", "type": "codergen",
      "prompt": "Summarize what was built and how it was verified.",
      "edges": [ { "to": "success" } ] }
  ]
}
```

`tractor run proof-pipeline.json --workdir <tmp-repo> --logs <tmp-logs>`.
Acceptance evidence, captured into the project dir:

- Run completes `completed`; `greet.sh` exists, is executable, and the
  tool node's `tool.log` shows the grep passing — **observable workspace
  effect produced by a Gemini model through agy**.
- `implement` loops through `check` at least once in total (revisit path
  exercises `--conversation` resume + no-op compact under default
  `compacted` fidelity).
- `{logs}/events/*.jsonl` segments contain `user`, `assistant`,
  `tool_call`/`tool_result` pairs, and `usage` events with `node_id` and
  `ts` stamps; `checkpoint.json` `sessions` shows
  `{"harness":"agy","session_id":"<uuid>",…}` per thread.
- One kill-and-resume: SIGINT the tractor process mid-`implement`, then
  `tractor run --resume` completes using the same conversation ID.

## 10. Rejected machinery (cautionary review of the old plan)

From `…/antigravity-integration-plan/ephemeral/projects/antigravity-integration/plan.md`:

- **`pending:<uuid>` session IDs + provider-neutral `BindingUpdated`
  adoption hook** — a contract change to solve ID late-binding. Rejected:
  the handshake turn (§2) gets a durable native ID through the existing
  contract for the price of one short cached-prompt turn.
- **Terminal-error `Compact` + capability preflight rejecting `compacted`
  fidelity** — would break every default-fidelity gemini node. Rejected for
  no-op compact (§7), justified by spec 5.4's delegation and P3b.
- **Provider-neutral capability report** — new interface surface with one
  consumer. Rejected: the conformance report's per-adapter skip row and
  this document are the capability record.
- **Auth doctor probe, env allowlist stripping, sandbox profile design** —
  authentication is explicitly out of scope (spec 12.2 note 7); agy uses
  its keyring session, and Tractor already runs claude/codex with
  permission bypass. `--sandbox` stays off for parity. If a user runs with
  API-key env vars set, that is their configuration, as it is for the
  other two harnesses.
- **Porting SDK/protobuf/localharness protocol** — nothing needs it; the
  documented CLI covers the contract.

## 11. NDJSON structs: hand-written; go-gen-jsonschema assessment

**Hand-write the structs** in `stream.go` (~80 lines). The event grammar is
three envelope variants and ~15 fields, verified against live captures; a
code generator would add a build step to protect against a schema Google
does not publish as machine-readable anyway.

**go-gen-jsonschema: not appropriate here.** It generates JSON Schemas
*from* Go types for LLM structured-output enforcement (as `graph/` uses it
for the pipeline schema). The inbound agy stream is the opposite direction:
third-party JSON we must decode *tolerantly* — unknown fields, unknown
`step_type` values, an open event set. Strict schema validation of the
inbound stream would turn benign CLI evolution into hard failures, exactly
what spec 12.3's open-set rule tells consumers not to do. Documentation of
the stream lives in this file and in the committed `testdata/*.ndjson`
fixtures, which double as decoder regression tests. (No change to the
adapter's *outbound* schema handling either — the choice schema is already
produced by the engine and validated by `harness.NewResultValidator`.)

## 12. Milestones

Each milestone compiles, passes `go test ./...`, and is independently
reviewable.

1. **Fixtures + decoder** (`stream.go`, `testdata/`). Re-capture sanitized
   NDJSON fixtures with the real CLI: plain success, tool-heavy success,
   schema mismatch, ERROR result, interrupted tail (no result line).
   Decoder unit tests over every fixture; unknown-field/unknown-type
   tolerance tests.
2. **Event projection** (`events.go` + tests). Fixture-driven: exact
   neutral event sequences asserted, delta coalescing, tool pairing,
   at-most-once, usage passthrough.
3. **Adapter core** (`adapter.go`, `exec.go` + tests with fake binary).
   CreateSession handshake, RunTurn happy path, schema temp file, repair
   turn, workdir immutability, per-session serialization, error
   categorization table, unknown-session/model failures.
4. **Interrupt/timeout/compact/steer** (+ tests). Kill path with grace
   escalation, adapter-timer authority over `--print-timeout`, interrupt
   flag → `interrupted`, Compact guards, Steer no-op, `Close()`.
5. **Wiring + conformance** (`backend.go` route, `root.go`,
   `harness-conformance` agy target with steering skip). Run the live
   conformance suite against agy 1.1.14 with `gemini-3.7-flash-low`;
   commit the evidence JSON to the project dir.
6. **Live proof.** §9's pipeline run, including the kill-and-resume pass;
   evidence (run dir listing, tool.log, checkpoint sessions excerpt) into
   the project dir. This is the ship gate.

Estimated total: ~900 lines of adapter code, ~700 of tests, ~40 of wiring.

## 13. Residual risks (accepted)

- **Steering gap** on gemini-routed nodes (§7, §8) — documented, not
  worked around.
- **agy CLI evolution**: the stream grammar is observed, not versioned.
  The tolerant decoder + pinned-version conformance evidence
  (`agy 1.1.14`) bound the blast radius; re-run conformance on upgrade.
- **Handshake turn cost**: one short turn per new session (heavily
  cache-read). Acceptable; only `fidelity: none` nodes multiply it.
- **`structured_output` extraction quirks**: agy sometimes leaves the JSON
  duplicated in `response` text (P1) — harmless; only `structured_output`
  is consumed. Nonconformance is covered by validate + one repair + terminal.
- **Print-timeout wording overlap** (`timeout waiting for response` both
  for agy-side stalls and our backstop) — mitigated by making the adapter
  timer authoritative and classifying its own expiry before reading the
  result.
