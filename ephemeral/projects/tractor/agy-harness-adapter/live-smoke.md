# Native agy companion smoke

Date: 2026-08-18

Command (caller-detection variables removed so standalone Gemini selection is
not replaced by the surrounding Codex session):

```sh
env -u CODEX_THREAD_ID -u CLAUDE_CODE_SESSION_ID go run ./cmd/agent \
  --provider gemini \
  --model gemini-3.7-flash-low \
  --reasoning-effort low \
  --timeout 5m \
  /tmp/tractor-agy-native.j7AvG4 \
  'Read input.txt, create output.txt containing exactly the same text, then report concise notes.'
```

Result: exit 0; native session
`51e2ab94-7ca2-446b-b9db-3688240bbcc0`. `cmp input.txt output.txt` passed
for the unpredictable value `agy-native-4921`. The persisted neutral event log
contained the exact initial user parts, matched `step-7` and `step-9` tool
call/result pairs, one complete assistant item, and usage events.

The immediately preceding native attempt returned an ID-less transient service
failure. That exposed an ordering bug: unsuccessful result status/error must be
classified before successful-stream conversation ID invariants. The adapter and
regression suite now preserve that behavior, including the observed Gemini Code
Assist INTERNAL 500/connectivity signature as retryable.
