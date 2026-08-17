# Claude Code lifecycle characterization

## Version and probe

- Claude Code `2.1.233`; `github.com/roasbeef/claude-agent-sdk-go@v1.1.1-0.20260713164230-efdbecd88a98`; real `fable` turns with bypass permissions in disposable workdirs.
- The external Go probe used `Stream`, locally minted UUIDs, `WithResume`, and the SDK extra-argument path for `--session-id`. The pinned SDK's `SessionOptions.SessionID` populates outbound messages but does not add the CLI flag itself.

## Session identity and materialization

- The first incoming message after the first `Stream.Send` was `SystemMessage{subtype:"init"}` and carried the exact locally minted session ID.
- Contradiction: `init` is identity acknowledgement, not durable materialization. At that boundary the native session store still had no entry; hard-killing Claude immediately made `--resume` fail with `No conversation found`. The next observed matching-ID message was `RateLimitEventMessage`, by which time the native transcript existed; hard-killing there and resuming recovered an unpredictable remembered value.
- After ordinary materialization during tool activity, `InterruptWithReceipt` acknowledged in 1 ms and the turn ended as `ResultMessage{subtype:"error_during_execution", terminal_reason:"aborted_tools"}`. A fresh process resumed the same ID and used a remembered value in a workspace write.
- A separate `Client.Close` during a materialized tool call reached the SDK's five-second force-kill boundary (5.002 s); a fresh process again resumed the exact ID and used the remembered value. The tool child outlived that process close and later wrote its planned finish marker, consistent with the spec's exclusion of detached-tool cleanup.

Implementation conclusion: validate the minted ID on `system/init` but keep the session fresh. For this pinned version, promote it to resumable on the first later matching-ID native message (the first observed was `rate_limit_event`); interruption or process loss after that point preserves native resume.

## In-turn steering

- During one live Bash call (`started` marker, 12-second sleep, `finished` marker), `Stream.Send` returned nil for an unpredictable instruction. The original Bash call completed, Claude performed the requested workspace write with the exact value, and only then emitted the turn's single successful `ResultMessage`.
- No interrupt, restart, intermediate result, or inbound echo of the steering text occurred. The SDK stream did emit the resulting assistant tool call and tool result.

Implementation conclusion: `Stream.Send` is the required non-destructive active-turn path. Because Claude does not echo the steering user message, the adapter must emit the spec's normalized `user` event when it hands the instruction to `Stream.Send`; nil means SDK queue acceptance, not a stronger delivery acknowledgement.

## Native compaction boundary

Sending `/compact` on an idle resumed stream produced:

1. `StatusMessage{status:"compacting"}`.
2. `StatusMessage{status:null, compact_result:"success"}` after native work completed.
3. A new `SystemMessage{subtype:"init"}`.
4. `CompactBoundaryMessage{subtype:"compact_boundary", trigger:"manual"}`.
5. `ResultMessage{subtype:"success"}`.

Implementation conclusion: use the terminal `StatusMessage` and its `compact_result` / `compact_error` as the native completion result. `CompactBoundaryMessage` records the new history boundary and the following `ResultMessage` closes the slash-command turn; neither should replace the explicit compaction result.
