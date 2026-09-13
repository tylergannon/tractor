# Probe findings (2026-09-12, cheap tier)

Raw JSONL and the full write-ups are in FINDINGS-claude.md, FINDINGS-codex.md,
FINDINGS-agy.md beside this file (untracked). These are the rules each adapter
follows as a result.

## Claude Code (Haiku, claude-haiku-4-5-20251001, Claude Code 2.1.259)

- `result.usage`, `result.total_cost_usd`, and `result.modelUsage[model].costUSD`
  are per-turn. Nothing in `result` is a conversation running total.
- For a single-call turn `result.usage` equals the last `message_delta.usage`.
  Steps carry the per-call usage; `result` is the turn report. Never add both
  into the same total.
- Stream order: `message_start` (prompt-side usage final) ... `message_delta`
  (final output and thinking) ... `message_stop`, `rate_limit_event`, `result`.
- `modelUsage` is keyed by native model id and carries `costUSD` plus tokens.
- Adapter rule: steps from stream events as today, minus the sidecar. The
  turn report is `modelUsage` mapped to `map[model]Usage` with cost.

## Codex (gpt-5.6-luna)

- The notification is `thread/tokenUsage/updated` (not `thread/tokenUsage`),
  carrying `threadId` and `turnId`; it arrives after the last
  `rawResponse/completed` and before `turn/completed`.
- Field names are camelCase: `totalTokens`, `inputTokens`, `cachedInputTokens`,
  `cacheWriteInputTokens`, `outputTokens`, `reasoningOutputTokens`.
  `cachedInputTokens` is a subset of `inputTokens`; `reasoningOutputTokens` is
  a subset of `outputTokens`. So `input = inputTokens - cachedInputTokens`,
  `output = outputTokens - reasoningOutputTokens`. (Whether cache write is
  also inside inputTokens is unverified; it was 0 in every sample. Do not
  subtract it.)
- `last` equals the most recent model call's `rawResponse/completed` usage.
  `total` is cumulative per app-server process, not per thread, and grows
  across calls within a turn. Only `last` or per-call usage is safe to sum.
- A resumed turn (new process, `thread/resume`) emits no `rawResponse/completed`
  at all because `experimentalRawEvents` is not passed on resume. So on a
  resumed turn `thread/tokenUsage/updated.last` is the only usage source.
- Adapter rule: per-step usage from `rawResponse/completed` as today; pass
  `experimentalRawEvents: true` on `thread/resume` too so resumed turns get
  it. Handle `thread/tokenUsage/updated`: use `last` as the step usage when
  the step has none yet. Codex states no cost; the turn report is nil.

## Antigravity (gemini-3.8-flash-low, agy 1.2.1)

- `input_tokens` is already the uncached remainder. On warm turns
  `cache_read_tokens` exceeds `input_tokens`. Do not subtract.
- `thinking_tokens` is a subset of `output_tokens`:
  `output = output_tokens - thinking_tokens`.
- `total_tokens == input_tokens + output_tokens` always.
- `cache_write_tokens` never appears. `cache.write` is 0.
- The `result` envelope's usage is cumulative over the whole conversation;
  `step_update` usage is per-step. Steps are the source; the result is not
  added. Antigravity states no cost; the turn report is nil.
