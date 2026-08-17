# Claude `agent` live proof

- Native harness: Claude Code `2.1.233`, authenticated with the first-party Max subscription.
- Adapter SDK: `github.com/roasbeef/claude-agent-sdk-go@v1.1.1-0.20260713164230-efdbecd88a98`.
- Caller: `CODEX_THREAD_ID` was present, so `agent` selected the Claude adapter and `fable` adversary.

## Fresh turn

Command:

```text
go run ./cmd/agent --logs ephemeral/projects/tractor/claude-live/logs-verified ephemeral/projects/tractor/claude-live/workspace-verified "Use tools to read input.txt. Create result.txt containing exactly two lines: first the input value uppercased, second its character count as a base-10 integer. Verify the file, then return next=done and notes summarizing the verified values."
```

Exit status: `0`.

```json
{"outcome":{"next":"done","notes":"Read input.txt (value: harbor-quartz-3d90ac). Wrote result.txt with line 1 = HARBOR-QUARTZ-3D90AC and line 2 = 20. Verified by reading the file back: line 1 length is exactly 20, matching the recorded count."},"session_id":"83c4c4ce-b61b-4d38-aa79-75e8f852154c","logs_root":"/Users/tyler/src/.worktrees/tractor/implement-attractor/ephemeral/projects/tractor/claude-live/logs-verified"}
```

The workspace result was:

```text
HARBOR-QUARTZ-3D90AC
20
```

The public run log contains four matched `tool_call` / `tool_result` pairs.

## Native continuation after adapter reconstruction

The second command was a new `agent` process with `--session 83c4c4ce-b61b-4d38-aa79-75e8f852154c`. Its prompt did not contain the original value and explicitly prohibited re-reading either source file.

Exit status: `0`.

```json
{"outcome":{"next":"done","notes":"Created continuation.txt from conversation memory without re-reading input.txt or result.txt. Contents verified byte-by-byte via od: \"harbor-quartz-3d90ac|continued\" plus one trailing newline (31 bytes total)."},"session_id":"83c4c4ce-b61b-4d38-aa79-75e8f852154c","logs_root":"/Users/tyler/src/.worktrees/tractor/implement-attractor/ephemeral/projects/tractor/claude-live/logs-continuation"}
```

The independently inspected workspace result was:

```text
harbor-quartz-3d90ac|continued
```

The continuation run log contains three matched `tool_call` / `tool_result` pairs and retains the same native session ID.

After the final lifecycle fix, a third independent `agent` process resumed the same native session under the caller-selected `fable` / medium defaults. Without reading any existing file, it created and byte-verified:

```text
harbor-quartz-3d90ac|current
```

It exited `0` with `next=done`; its public transcript is under `claude-live/logs-current-code`.

## Unknown session

The final build was also invoked with `--session 00000000-0000-4000-8000-000000000001`. Claude Code reported `No conversation found`, and `agent` returned the same message as a terminal categorized error with exit status `1` in 1.26 seconds. The public run log contained only the initial user event and no assistant or tool activity.
