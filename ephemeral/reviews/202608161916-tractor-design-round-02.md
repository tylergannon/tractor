# Adversarial review — Tractor harness slice design, round 02

**Target:** `ephemeral/projects/tractor/design.md` (498 lines, complete current artifact, re-read in full)

**Authoritative sources:** `docs/spec.md` (§3.8, §3.9, §4.5, §5.4, §11.13, §12.1–12.4, Appendix D);
`/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/goal-objective.md`; repo instructions
(`lefthook.yml` — default linters, fix the code; `go.mod` Go 1.26 + `tool` directives); the user's acceptance
context (minimal final-state Go; no over-engineering, belt-and-suspenders, unrequested features or safety,
non-free future-proofing, or unnecessary abstraction; idiomatic; actually works through both harnesses).

No caller instruction narrowed scope; the whole artifact was re-reviewed, not only round-01 deltas.

**Native behavior inspected this round (live, this machine):**

- `codex-cli 0.147.0`, `claude 2.1.233`, SDK `roasbeef/claude-agent-sdk-go@v1.1.1-0.20260713164230-efdbecd88a98`.
- **Codex retained-process lifecycle verified working.** `thread/start` (cwd `/tmp/tractor-probe`) → `turn/start`
  (`gpt-5.6-sol`, effort `low`, `approvalPolicy:"never"`, `sandboxPolicy:{"type":"dangerFullAccess"}`) writes
  `~/.codex/sessions/2026/08/16/rollout-…-01a00d48-ce4d-70f1-a989-203dcb23f28d.jsonl` **during** the turn;
  `turn/interrupt` → `{"result":{}}`; after killing that process, a new app-server `thread/resume` on the same
  thread **succeeds**. Design §6.2 and Chunk 3's characterization item (line 485) are therefore sound, including
  the interrupted-first-turn case.
- **`model/list` data:** `gpt-5.6-sol` exists and is `isDefault: true` (`defaultReasoningEffort: "low"`), so the
  §9 constant is real; `medium` matches the replicated CLI's documented default, not the native one — acceptable.
- **`TurnSteerParams`** requires `threadId`, `expectedTurnId`, `input` — matches §6.3's "active turn ID".
- **`ThreadCompactStartParams`** = `{threadId}` only; **`ThreadCompactStartResponse`** = `{"type":"object"}` with
  **no properties**; a separate `ContextCompactedNotification` (`{threadId, turnId}`, "Deprecated: Use
  `ContextCompaction` item type instead") exists alongside `item/completed`.
- **Claude unknown-session resume:** `claude --print --verbose --output-format stream-json --resume <fresh-uuid>`
  → exit **1**, stderr `No conversation found with session ID: …`, stdout
  `{"type":"result","subtype":"error_during_execution","is_error":true,…}`.
- **SDK `Stream.Send`** (`client.go:673-680`) writes to the unbuffered `sendCh` drained by `handleSends`
  (`client.go:728`); `client.go:769-783` `InterruptReceipt.StillQueued` confirms async user messages coexist with
  a live turn.

**Round-01 findings status:** #1 (Codex rollout) fixed and now empirically confirmed; #2 (Claude empty session)
fixed via locally minted UUID + first-turn `--session-id`; #3 (interrupt-and-resend steering) replaced with
`Stream.Send`; #4 (run-log recovery) reduced to `max(seq)`; #5 (busy reservation, `model/list` discovery,
`--output`, external-`$ref` rejection, import test) removed.

---

## Findings

### 1. ISSUE (release-gate blocking) — Codex `Compact` waits for a completion signal that does not exist in 0.147.0

`design.md:330`: "`Compact` … resumes a durable native thread, waits for the native compact completion response,
and returns only then", and `design.md:296`: "`Compact` likewise uses a short-lived initialize/resume/
`thread/compact/start` process".

There is no compact completion *response* in 0.147.0. The generated schema for the method's reply,
`v2/ThreadCompactStartResponse.json`, is:

```json
{"$schema":"http://json-schema.org/draft-07/schema#","title":"ThreadCompactStartResponse","type":"object"}
```

— an object with no properties, consistent with the `.../start` naming. Completion is reported out-of-band, by
`ContextCompactedNotification {threadId, turnId}` (or the `ContextCompaction` item it defers to, delivered via
`item/completed`).

**Failure it creates:** the adapter returns as soon as the empty acknowledgement arrives and then closes the
short-lived app-server, i.e. it kills the process that is still performing compaction. Spec §12.2 (spec:2250)
requires `compact` to *block until harness-native compaction completes*, and conformance scenario 7 (spec:2090) —
"call `compact`, wait for it to return, and immediately revisit" — is a Chunk 3 release gate (`design.md:488`).
The failure mode is also the worst kind to debug: on a short conformance history compaction is fast and the race
will pass intermittently, and spec:2102 already warns that a behaviourally equivalent no-op is indistinguishable.

**Direction:** wait for the `ContextCompaction` item / `ContextCompactedNotification` for that thread before
closing, and characterize the actual completion signal in Chunk 3 alongside the rollout characterization
(`design.md:485` currently characterizes only start-plus-first-turn). The same paragraph's Claude counterpart
(`design.md:363`, "waits for the native compact status/boundary confirmation … Merely seeing a successful generic
result is insufficient") already states the correct discipline; §6.2/§6.3 should match it.

### 2. ISSUE — no release path for the retained Codex app-server child; `HarnessAdapter` has no `Close`

`design.md:287-289` has `CreateSession` spawn `codex app-server --stdio` and **retain the running process** in
`fresh` until the first `RunTurn`. `design.md:289` claims "that method's context bounds startup … After success,
the first `RunTurn` or adapter-process exit ends the transient lifetime", and `design.md:293` adds "A setup
failure leaves a healthy retained client available for another attempt."

The public interface (`design.md:157-163`) has no `Close`, `Shutdown`, or `io.Closer`. Go has no destructor, so a
retained child is released only by the first `RunTurn` or by the whole Tractor process exiting (stdin EOF). Two
in-scope paths never reach either:

- **Conformance scenario 2** (spec:2006) *requires* the program to "discard the adapter instance and create a new
  one" inside one process. Any session still in `fresh` at that moment leaks its app-server child for the rest of
  the conformance run.
- **Conformance scenario 4** (spec:2065-2069) creates a session with a deliberately nonexistent model; if
  `create_session` accepts it, `run_turn` must reject it. Per `design.md:293` the failure "leaves a healthy
  retained client available for another attempt" — an attempt that never comes, so this scenario leaks one child
  per execution by design.

**Impact:** orphaned `codex app-server` processes during exactly the runs that are the release evidence, plus a
design statement (`design.md:289`) that is only true for whole-process exit. Note this is not the disaster
recovery the spec waives (spec:2302) — it is ordinary resource ownership for state the design deliberately
introduced.

**Direction (minimal):** add `Close() error` to the adapter implementations (not necessarily to the spec-derived
interface; the executables and the conformance factory construct concrete types and can `defer Close()`), and
close the retained client when `RunTurn` fails before `turn/start` rather than retaining it for a hypothetical
retry.

### 3. ISSUE — Claude `Steer` states a delivery guarantee the SDK does not provide

`design.md:359`: "`Steer` … calls `Stream.Send` with the ordered steering text. **A nil return means the SDK
accepted the message into the active stream**; the adapter then emits exactly one additional `user` event. If the
session is not live, or the live turn wins the completion race, steering is a no-op".

`Stream.Send` (`client.go:673-680`) does not do that. It performs a single channel send into the unbuffered
`sendCh`, drained asynchronously by `handleSends` (`client.go:728`); its own doc says "Messages are queued and
sent asynchronously". A nil return means only that an SDK goroutine took the string — not that the CLI received
it, and not that the turn was still live when it did.

**Failure it creates:** (a) the run log records a `user` event for an instruction the harness may never apply,
which contradicts §12.3 note 2's "a steering instruction appears when the adapter hands it to the harness"
(spec:2332); (b) in the completion race the adapter cannot make it "a no-op" as claimed — if `handleSends` writes
the line after the turn's terminal boundary but before the stream is closed, Claude Code has a queued user
message on a session that is no longer running a prompt task, which §12.2 note 7 forbids delivering to
(spec:2293). Spec:2102 does exclude "steering's narrow completion race" from conformance, which is why this is an
issue rather than critical — but the design should describe what it actually knows instead of asserting SDK
acceptance.

**Direction:** state the honest semantics (best-effort handoff; §12.2 note 6 already permits exactly that), emit
the `user` event on that basis, and let Chunk 4's characterization (`design.md:492`) record what `Send` does at
the terminal boundary rather than assuming it is a no-op.

### 4. NITPICK — Claude unknown-session terminality rests on an undocumented discriminator

`design.md:338` states Claude Code's "'conversation not found' response becomes the required terminal
unknown-session error", and §8 (`design.md:372`) classifies unknown session as terminal while classifying
"transport failure before a provider terminal verdict" as retryable.

Reproduced on 2.1.233: an unknown `--resume` exits **1** with the distinguishing text only on **stderr** (`No
conversation found with session ID: …`); stdout carries a *generic* `{"type":"result","subtype":
"error_during_execution","is_error":true,…}`. Nothing in that structured result identifies the cause, so the
adapter's classifier must key on the exit status plus stderr text or it will land conformance scenario 4's
unknown-session assertion (spec:2065) in the retryable bucket. The design should name that discriminator, since
`design.md:429`'s Claude test list does not include an unknown-session classification test.

### 5. NITPICK — remaining non-free future-proofing for a layer this slice excludes

`design.md:13` ("The backend accepts restored bindings so that the future engine can checkpoint them") and
`design.md:222` (constructor "reads the greatest sequence in an existing index … because checkpoint resume
constructs a new backend for the same run"). Checkpoints are explicitly out of this slice (`design.md:13`), the
`agent` CLI always defaults `--logs-root` to a fresh temp directory (`design.md:390`), and the only in-slice
consumer of restored state, `--session` (`design.md:408`), needs the bindings map but never a continued sequence.
The index rescan and its dedicated Chunk 2 gate (`design.md:481`) and test (`design.md:426`) therefore exist
solely for an unimplemented consumer. It is now two lines of behavior rather than the round-01 recovery
subsystem, so this is a nitpick — but starting at zero and deleting the rescan, its gate, and its test would be
smaller and lose nothing this slice can exercise.

---

## Verified sound (not findings)

Codex retained-start-plus-first-turn lifecycle, including resume after an interrupted first turn (reproduced);
Claude locally minted UUID with first-turn `--session-id` and later `--resume` (matches reproduced 2.1.233
behavior); `--json-schema` via `WithExtraArgs` as the only mechanism the pinned SDK offers; `Stream.Send` as the
non-destructive steering primitive; `gpt-5.6-sol` as a real default model id; `turn/steer` requiring the active
turn id; the backend's single-live-turn delegation to adapters; run-log segment/index/`current.jsonl` ownership;
conformance scope tracking §11.13 exactly.

## Outcome

`material findings remain`
