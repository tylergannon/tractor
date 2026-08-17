# Codex app-server compaction characterization

## Version and scope

- Native harness: `codex-cli 0.147.0` (`codex app-server`, stdio transport).
- Characterized only the public v2 lifecycle needed by design choice 2 and spec Sections 11.13 scenario 7 and 12.2.
- Prompts, remembered value, thread ID, turn IDs, and item IDs are redacted below.

## Steps

1. Started app-server in a temporary empty workdir, initialized the connection, called `thread/start`, and completed one `turn/start` that established an unpredictable remembered value.
2. Terminated that app-server process, started a fresh one, and called `thread/resume` with the returned thread ID. The response loaded the completed first turn from native history.
3. Called `thread/compact/start`, captured every public message through completion, then immediately called `turn/start` on the same thread. The assistant returned the exact remembered value even though it was absent from the revisit prompt and workdir.
4. Deleted the temporary native thread with `thread/delete`, stopped the app-server process, and removed the temporary workdir.

## Observed compaction ordering

For `thread/compact/start`, the public ordering was:

1. Start acknowledgement: `{"id":N,"result":{}}`.
2. `thread/status/changed`: `{"threadId":"<thread>","status":{"type":"active","activeFlags":[]}}`.
3. `turn/started`: a new compaction turn with `status:"inProgress"`.
4. `item/started`: `{"item":{"type":"contextCompaction","id":"<item>"},"threadId":"<thread>","turnId":"<turn>","startedAtMs":...}`.
5. `thread/tokenUsage/updated` telemetry.
6. `item/completed`: `{"item":{"type":"contextCompaction","id":"<same-item>"},"threadId":"<thread>","turnId":"<same-turn>","completedAtMs":...}`.
7. `thread/status/changed`: the thread returned to `{"type":"idle"}`.
8. `turn/completed`: the same turn had `status:"completed"`; its item list was not loaded (`items:[]`, `itemsView:"notLoaded"`).

No `thread/compacted` notification was emitted. The generated 0.147.0 public schema still declares that notification with params `{"threadId":"<thread>","turnId":"<turn>"}`, but marks it deprecated in favor of the `ContextCompaction` item type.

## Implementation-relevant conclusion

`thread/compact/start` is asynchronous: its empty response is acceptance, not completion. For 0.147.0, block `HarnessAdapter.compact` until the matching `item/completed` for the `contextCompaction` item, while treating a failed compaction turn as an error; do not wait for `thread/compacted`, and do not expect the completed compaction item to be embedded in `turn/completed`. The matching `turn/completed` is the final turn-lifecycle notification after item completion.

A first completed turn is sufficient to materialize resumable native history: after app-server process loss, a fresh process successfully loaded that turn with `thread/resume`, and the same thread retained the remembered value across native compaction. This check did not attempt to characterize loss during an in-progress first turn.
