# Adversarial review — Tractor harness slice design, round 01

**Target:** `ephemeral/projects/tractor/design.md` (499 lines, complete current design)

**Authoritative sources used:** `docs/spec.md` (§3.8, §3.9, §4.5, §5.4, §11.13, §12.1–12.4, Appendix D);
`/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/goal-objective.md`; repo instructions
(`README.md`, `doc.go`, `go.mod`, `lefthook.yml` "no linter config, fix the code"); the user's acceptance
context (minimal final-state Go, no over-engineering / belt-and-suspenders / unrequested features or safety /
non-free future-proofing / unnecessary abstraction; idiomatic; actually works through both harnesses).

**Evidence inspected (live, this machine):**

- `codex-cli 0.147.0`; `codex app-server generate-json-schema` output: `v2/TurnStartParams.json` exists and is
  self-contained; methods spelled `model/list`, `thread/start`, `thread/resume`, `turn/start`, `turn/steer`,
  `turn/interrupt`, `thread/compact/start`; `TurnStartParams.model` is `string|null`.
- `claude 2.1.233`; flags `--session-id <uuid>`, `--resume`, `--json-schema`, `--output-format`,
  `--include-partial-messages`, `--permission-mode`, `--no-session-persistence`.
- `github.com/roasbeef/claude-agent-sdk-go@v1.1.1-0.20260713164230-efdbecd88a98` in the module cache:
  `options.go:1102` `WithExtraArgs`, `options.go:2477` `SessionOptions.SessionID`, `client.go:673` `Stream.Send`,
  `client.go:762/784` `Interrupt`/`InterruptWithReceipt`, `client.go:769-783` `InterruptReceipt.StillQueued`,
  `options.go:233` `IncludePartialMessages`. **No** output-schema / structured-output option anywhere in the SDK.
- `/Users/tyler/src/unified-llm/codingagent/cmd/agent/cli.go:68-73` — caller detection (`CLAUDE_CODE_SESSION_ID`
  first → Claude caller, then `CODEX_THREAD_ID`); confirms the design's detection and precedence are faithful.
- `/Users/tyler/src/attractor/internal/codexapp/client.go` — existing app-server client (initialize → thread/start
  → turn/start in **one** process; `ephemeral: true`).
- Two live reproductions (below).

No caller instruction narrowed the review scope; nothing was ignored.

---

## Findings

### 1. CRITICAL — Codex per-operation process lifecycle cannot resume a freshly created session (reproduced)

`design.md:19` and `design.md:291` fix the Codex lifecycle as: each of `CreateSession`, `RunTurn`, `Compact`
starts its own short-lived `codex app-server --stdio`; `CreateSession` calls non-ephemeral `thread/start` and
returns the thread ID; `RunTurn` (new process) calls `thread/resume`, then `turn/start`. The stated premise is
"Native thread storage is the durable state, so a resident Tractor daemon is unnecessary."

That premise is false for a thread that has had no turn. Codex writes the rollout on first turn, not at
`thread/start`.

Reproduction (codex-cli 0.147.0), process A: `initialize` → `initialized` → `thread/start {cwd:"/tmp"}` →
`{"thread":{"id":"01a00d3f-0258-7830-adf1-615000ffb31c","ephemeral":false,...}}`; process A exits; process B:
`initialize` → `thread/resume {threadId:"01a00d3f-..."}` →

```json
{"error":{"code":-32600,"message":"no rollout found for thread id 01a00d3f-0258-7830-adf1-615000ffb31c"}}
```

`find ~/.codex -name "*01a00d3f*"` yields only `~/.codex/thread-writer-locks/01a00d3f-...lock` — no
`~/.codex/sessions/.../rollout-*.jsonl`.

**Impact:** every fresh Codex session fails on its very first turn — i.e. conformance scenario 1 (spec:2042) and
the `agent` Codex smoke test (`design.md:466-467`), which is the acceptance criterion. Chunk 3's gate
(`design.md:491`) cannot pass as designed.

**Direction (minimal):** either keep the one app-server process alive for the session (as
`/Users/tyler/src/attractor/internal/codexapp/client.go` does: start + turn in one process), or make
`CreateSession` allocate the thread lazily so that `thread/start` and the first `turn/start` happen in the same
process and only subsequent turns `thread/resume`. Either is smaller than the current design, not larger.

### 2. CRITICAL — Claude `CreateSession` (empty initialized session) does not exist in Claude Code 2.1.233 (reproduced), and the design turns that into a hard block

`design.md:333` specifies: generate a UUID, pass it via `WithExtraArgs` as `--session-id`, open a bidirectional
stream, "wait for the native system/init message carrying the same ID", close without sending a user message,
and requires that "the installed Claude Code persists and can resume this empty initialized session"; if not,
"Claude conformance remains failing until a genuine native empty-session mechanism is identified."

Both halves fail on the installed CLI:

1. No `system`/`init` message precedes a user message. With `claude --print --verbose --input-format stream-json
   --output-format stream-json --session-id <uuid>` and the SDK's own handshake line
   `{"type":"control_request","request_id":"1","request":{"subtype":"initialize"}}`, the only output within 10 s
   is a `control_response` (command/agent inventory). Waiting for `system`/`init` blocks until timeout.
2. No session is persisted: `find ~/.claude/projects -name "*<uuid>*"` → nothing, and
   `claude --print --resume <uuid> "say hi"` → `No conversation found with session ID: <uuid>`.

**Impact:** as written, Chunk 4 (`design.md:497`) is designed to *stop* rather than deliver — which forfeits the
user's stated definition of done ("actually works through both harnesses").

**Direction (minimal, and spec-conformant):** `CreateSession` returns a locally generated UUID with no harness
call at all; the first `RunTurn` passes `--session-id`, subsequent turns `--resume`. Spec §12.2 only requires
that `create_session` "establishes a new empty session and returns an id required by the other methods"; a
locally minted ID that the harness materialises on first use satisfies that and removes a process spawn.

**Related inaccuracy in the same paragraph:** `design.md:331` says the adapter "supplies the exact schema through
the SDK option and, where still necessary, the SDK's supported extra-argument mechanism." The pinned SDK has no
structured-output/schema option — only the consumption side (`messages.go:195 StructuredOutput`). `--json-schema`
must go through `WithExtraArgs`. The hedged wording should collapse to the one mechanism that exists. Likewise
`design.md:335` names 2.1.206 as the proof target while 2.1.233 is installed.

### 3. ISSUE — Claude steering designed as interrupt-then-resend: destructive, unproven, and heavier than the SDK's own path

`design.md:354-360` makes steering an abort boundary: interrupt the live turn, wait for the aborted terminal
boundary, resend the steering text on the same session, then continue waiting for "the corrected response as the
result of the original `RunTurn`". This requires a per-operation control channel, capability gating on
`interrupt_receipt_v1`, and an aborted-boundary state machine (`design.md:350`).

The SDK already documents the non-destructive path: `Stream.Send` (`client.go:668-680`) — "Messages are queued and
sent asynchronously" — and `InterruptReceipt.StillQueued` (`client.go:769-783`) exists precisely because *async
user messages coexist with a live turn* ("queued commands plus any batch already dequeued for the imminent turn").
The design asserts this is "the proved Claude stream control" but cites no characterization, and states "a later
SDK may expose a direct in-turn append" when `Send` is already present.

Spec intent is also against it: §12.2 note 7 — a steering message "is meant to be added to the message array
soon, without waiting for the current task to run to completion" — and §3.9 frames steering as guidance to work
already in progress, not a restart. Throwing away the in-flight turn's work is the opposite, and it makes
conformance scenario 5 (spec:2072) pass only by accident of the model redoing the task.

**Direction:** characterize `Stream.Send` mid-turn first; adopt abort-and-resend only if that characterization
actually fails, and record the evidence.

### 4. ISSUE (over-engineering) — run-log crash recovery, sequence continuation, and log-write-fails-the-turn

`design.md:223` ("reads the existing index to continue sequence allocation without overwriting an earlier
segment"), `design.md:265` ("Constructor recovery scans valid index lines… reports malformed lines rather than
guessing… a crash may leave an unindexed exclusive-create file; construction reports that collision, preserving
evidence"), and `design.md:267` ("An event-write failure … makes the turn fail terminally").

- Spec §12.2 note 11 (spec:2302-2305) explicitly declines disaster prevention/recovery and advises "occasional
  workflow retry as an acceptable fallback in place of difficult recovery logic."
- Spec §12.1 Construction (spec:2215-2226): a `HarnessBackend` is constructed **per run** with that run's
  `logs_root`; the `agent` CLI defaults `--logs-root` to a fresh temp dir (`design.md:393`). So index re-scanning,
  sequence continuation, malformed-line reporting, and exclusive-create collision reporting are machinery for a
  state this slice cannot reach.
- Spec §12.2 note 10 (spec:2301) puts event persistence outside the adapter contract; failing an otherwise
  successful, expensive real-harness turn because one JSONL line could not be written is unrequested safety.

**Direction:** create segment, append index, swap the symlink, start the sequence at 0. Nothing else.

### 5. ISSUE (over-engineering) — invented backend concurrency layer and unrequested CLI/validator/test surface

Distinct instances of the same category:

- `design.md:233` — a per-logical-key reservation held across compaction + `RunTurn`, with a **new** retryable
  "thread busy" error that appears nowhere in the spec's error vocabulary — sitting directly above
  `design.md:234`, which notes the adapter already enforces one live turn per native session (spec:2239). Belt
  and suspenders. Spec §3.8 (spec:613-623) makes traversal single-threaded, and branch turns run in distinct
  worktrees, so a genuine same-key concurrent visit is already terminal on the workdir check (spec:2164).
- `design.md:401` — the CLI pages `model/list` and picks the `isDefault: true` row to obtain a concrete model
  plus `defaultReasoningEffort`, coupling `cmd/agent` to the app-server client for an extra process round trip.
  `TurnStartParams.model` is `string|null` and `thread/start`'s model is optional, so Codex's own default is
  available for free; a constant is the other one-line option.
- `design.md:404` — rejecting "recursive self-selection" of the calling harness: an unrequested guardrail.
- `design.md:198-206` — explicitly rejecting non-fragment external `$ref`s. `jsonschema/v6` performs no remote
  loading unless a loader is installed, so this is code that can only restate the library's default.
- `design.md:433` — a `go list -deps` test failing on imports from three modules that are not (and would have to
  be deliberately added as) dependencies.
- `design.md:394` (`--output FILE`) — beyond the CLI shape the goal asks to replicate.

Each is small; together they are the "unnecessary abstraction / unrequested safety" surface the acceptance
context rules out, and each is deletable without touching a requirement.

---

## Notes on what is sound (not findings)

Caller detection and precedence match `unified-llm/codingagent/cmd/agent/cli.go:68-73` exactly; the
`agent`-only-through-`HarnessBackend` rule (`design.md:382`, `design.md:470`) is the right proof lever;
restricting generation to the self-contained `TurnStartParams.json` matches what the installed toolchain can
actually generate; the run-log layout, fidelity/binding rules, and event normalization follow §12.1–12.4
faithfully; the conformance executable's scope is spec-mandated by §11.13, not over-engineering.

## Outcome

`material findings remain`
