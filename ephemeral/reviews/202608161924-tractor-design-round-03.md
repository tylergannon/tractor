# Adversarial review — Tractor harness slice design, round 03

**Target:** `ephemeral/projects/tractor/design.md` (501 lines, complete current artifact, re-read in full)

**Authoritative sources:** `docs/spec.md` (§3.8, §3.9, §4.5, §5.4, §11.13, §12.1–12.4, Appendix D);
`/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/goal-objective.md`; repo instructions
(`lefthook.yml` — default linter configs, fix the code, no nolint; `go.mod` Go 1.26 + `tool` directives); the
user's acceptance context (minimal final-state Go; no over-engineering, belt-and-suspenders, unrequested
features or safety, non-free future-proofing, or unnecessary abstraction; idiomatic; actually works through
both harnesses).

No caller instruction narrowed scope. The whole artifact was re-reviewed, not only the round-02 deltas.

**Evidence inspected this round (live, this machine):**

- `claude 2.1.233`, `codex-cli 0.147.0`, SDK
  `github.com/roasbeef/claude-agent-sdk-go@v1.1.1-0.20260713164230-efdbecd88a98`.
- **Decisive Claude reproduction — a `--session-id` cannot be reused once materialized.**
  `claude --print --session-id b3791780-a1ff-45aa-85ac-e35d37525d31 --model haiku "reply with exactly: ok"`
  → `ok`. Re-running the *identical* command → exit nonzero with
  `Error: Session ID b3791780-a1ff-45aa-85ac-e35d37525d31 is already in use.`
  There is no tolerant fallback: passing `--session-id` for an existing conversation is a hard error, distinct
  from the unknown-`--resume` error reproduced in round 02.
- **SDK exit-status reachability.** `ErrSubprocessFailed` is constructed in exactly one non-test place,
  `transport.go:418`, on `runner.Start` failure — i.e. spawn failure only, never nonzero exit.
  `errors.go:97 ErrSessionNotFound` exists but is never constructed outside tests. `WaitForExit` is declared on
  the `Transport` interface (`transport.go:79`, impl `transport.go:627`); `Client` holds its transport in an
  unexported field (`client.go:23`) and exposes no accessor (`client.go:86–649`), and `Client.Close`
  (`client.go:540–560`) returns `transport.Close()`, not the child's wait status.
- **SDK compaction observability** confirmed adequate for §7.3: `messages.go:708/766–775/1545` parse
  `compact_boundary`; `messages.go:1425–1444` carry `SDKStatusCompacting` / `CompactResult` / `CompactError`.
- **`Stream.Send`** (`client.go:673–680`) still a single send into the unbuffered `sendCh` drained by
  `handleSends` (`client.go:728`) — the design's revised §7.3 wording now matches this.
- Codex generated schemas re-checked: `ThreadCompactStartResponse.json` has no properties;
  `ContextCompactedNotification.json` = `{threadId, turnId}`, marked "Deprecated: Use `ContextCompaction` item
  type instead".
- Spec anchors: 2006, 2042–2096 (scenarios), 2102 (excluded residual risks), 2215–2226, 2239, 2250, 2293,
  2301–2305, 2332.

**Round-02 findings status:** #1 (Codex compact completion signal) fixed — `design.md:296`/`332` now require a
`ContextCompaction` item or `ContextCompactedNotification` and state acknowledgement alone never completes;
#2 (no release path for the retained child) fixed — concrete `Close()`/`io.Closer` at `design.md:287`, CLI and
conformance cleanup at `design.md:298`; #3 (`Stream.Send` overclaim) fixed — `design.md:363` now states honest
best-effort handoff plus the residual race; #4 (unknown-session discriminator) addressed at `design.md:342`
plus a test at `design.md:433`; #5 (sequence continuation) fixed — `design.md:222` starts at zero.

---

## Findings

### 1. ISSUE — a mid-turn first-turn failure leaves a *materialized* Claude session marked `fresh`, and the retry hits a hard "already in use" error

`design.md:359`: "A fresh entry is removed once Claude reports the matching session ID **and** the first turn
reaches its terminal result; a failure before native session creation leaves it fresh for a later attempt."

Both clauses key removal on reaching a terminal result. Neither covers the interval in between: Claude has
reported the session ID (the conversation now exists on disk) but the turn dies before a terminal result —
caller context cancellation, `RunTurn` deadline expiry that force-closes the client (`design.md:365`), a
transport failure, or any SDK receive-loop error. The entry stays in `fresh`, so per `design.md:340` the next
`RunTurn` for that session passes the ID again as `--session-id`.

**Reproduction of the consequence** (2.1.233, above): a second `--session-id` on a materialized conversation
returns `Error: Session ID <uuid> is already in use.` — not a tolerant re-attach. The session is then
permanently unusable through this adapter: the ID is never demoted out of `fresh`, so every later attempt
repeats the same hard failure, and the classifier at `design.md:342` (which keys unknown-session on
`No conversation found with session ID`) will not even categorize it correctly.

**Impact:** the paths that reach this state are exactly the ones conformance exercises. Scenario 6 (spec:2078)
requires that after interrupt and after timeout "the same session completes a later normal turn" — a timeout on
the *first* turn force-closes the client (`design.md:365`), which is precisely a failure that may land after
materialization and before a terminal result. Scenario 2 (spec:2006) reconstructs the adapter, and
reconstruction is only sound if the ID has been demoted to resume.

**Direction (minimal, and already present on the other side):** key removal on **materialization**, not on
terminal result — the moment Claude reports the matching session ID, drop the fresh entry, whatever the turn
does afterwards. That is the exact rule `design.md:294` already states for Codex ("every first `RunTurn` return
path removes and closes its retained client, whether the public result is success, interrupted, or another
error"); §7.2 should mirror it rather than invent a second, weaker condition.

### 2. ISSUE — §6.2's blanket "terminal fresh-session error" contradicts the §8 classification table

`design.md:291`: "**Any** first-turn failure before materialization also removes and closes the retained client
and returns a terminal fresh-session error."

`design.md:377` classifies "rate limit with retry signal, transient service unavailability, transport failure
before a provider terminal verdict" as `retryable`, and `design.md:379` says EOF/child-exit/malformed protocol
"is retryable [when] the native error explicitly identifies a transient provider failure." A rate-limited or
service-unavailable first `turn/start` is by definition a failure before materialization, so the two rules give
opposite categories for the same observation and an implementer will pick arbitrarily.

**Impact:** conformance scenario 4 (spec:2065–2069) asserts on the *public error categories*, and Appendix D's
categories are what the engine's retry decision consumes. Reporting a transient provider failure as `terminal`
denies retry to a case the spec explicitly makes retryable; the reverse would promise a retry the design cannot
honour, since the retained client is closed and no rollout exists.

**Direction:** decide once and write it once. If the fresh path genuinely cannot be retried on the same ID (it
cannot — the thread ID has no rollout and the client is gone), state that as a *named exception* in §8 rather
than as a §6.2 sentence that silently overrides the table, and say what the caller is expected to do instead
(create a new session). Note the asymmetry with Claude, where `design.md:359` deliberately keeps a
pre-materialization failure retryable on the same ID because the ID is local.

### 3. NITPICK — runtime `io.Closer` assertion in a factory that constructs the concrete types

`design.md:298` and `design.md:441`: the conformance factory "returns an adapter plus a cleanup function which
calls `Close` when the concrete value implements `io.Closer`", "and otherwise supplies a no-op cleanup."

The factory has exactly two arms and constructs `*codex.Adapter` and the Claude adapter itself, so it statically
knows which one is closable. A dynamic type assertion for a two-case closed set is the "unnecessary abstraction"
the acceptance context rules out, and it hides a real regression: if `*codex.Adapter` ever loses `Close`, the
assertion silently degrades to the no-op arm and the leak returns with every test still green. Returning the
cleanup explicitly from each arm is smaller and fails loudly.

### 4. NITPICK — both Codex compaction completion shapes are specified for implementation before Chunk 3 characterizes which one exists

`design.md:296`/`332` require the client to accept "either a completed `ContextCompaction` item on
`item/completed` **or** `ContextCompactedNotification`", and `design.md:432` mandates tests proving "either
matching completion shape releases the call" — while `design.md:489` still lists "characterize whether real
compaction completes with the `ContextCompaction` item, `ContextCompactedNotification`, or both" as work to be
done first. The notification is marked deprecated in its own generated schema. Ordering the tests before the
characterization guarantees that one of the two branches is dead code with a test that only exercises a fixture.
Let Chunk 3's characterization pick the shape and implement that one; the second branch is a two-line addition
if the evidence demands it.

### 5. NITPICK — the Claude unknown-session discriminator names a signal the pinned SDK does not expose

`design.md:342`: "an unknown session is specifically **exit status 1 plus** stderr containing `No conversation
found with session ID`; that pair maps to terminal."

Through the SDK path the design uses (`Client` + `WithStderr`), the exit status is not observable:
`ErrSubprocessFailed` is produced only on spawn failure (`transport.go:418`); `WaitForExit` lives on the
`Transport` interface (`transport.go:79`) and `Client` neither exposes its transport (`client.go:23`) nor
returns a wait status from `Close` (`client.go:540–560`); `ErrSessionNotFound` (`errors.go:97`) is never
constructed. So the adapter has the stderr line and the generic `error_during_execution` result, and nothing
else, unless it injects its own `SubprocessTransport` via the transport option. The design should say which —
stderr alone, or the injected transport — so `design.md:433`'s "exit-1 plus stderr" test is written against a
signal that exists.

---

## Verified sound (not findings)

Codex retained-start-plus-first-turn lifecycle with concrete `Close` and cleanup on every first-turn return path
(`design.md:287`–`298`); compaction now blocked on a real completion signal per spec:2250; `Stream.Send`
described with honest semantics and the residual race explicitly matched to spec:2102's exclusion
(`design.md:363`); Claude compaction wait implementable against the SDK's `compact_boundary` / compaction-status
parsing; sequence allocation starting at zero (`design.md:222`); adapters — not the backend — enforcing one live
turn (`design.md:232`, spec:2239); in-harness model/effort as plain constants with no `model/list` round trip
(`design.md:401`–`407`), with `gpt-5.6-sol` confirmed real and `fable` matching the replicated CLI; caller
detection and Claude-first precedence faithful to `unified-llm/codingagent/cmd/agent/cli.go:68-73`; `agent`
reaching the harness only through `HarnessBackend` (`design.md:383`, `design.md:471`); conformance scope
tracking §11.13 exactly.

## Outcome

`material findings remain`
