# Issue 149: token accounting as a semantic port of OpenCode

Branch `claude/issue-148-plan-ae018a`. Issues #148 and #149 on GitHub carry the
problem statement. This file is the brief every agent on this work reads first.

## The rule

As simple as possible (`AGENTS.md`). Build only what the definition of done
below asks for. No availability flags, no price catalog, no per-field
provenance, no backwards compatibility. Delete what is replaced.

## The reference

The OpenCode source at the pinned revision is copied (untracked) under
`ephemeral/inspiration/opencode/`. Read the original before writing the port
and validate the port against it. The files that matter:

- `packages/core/src/session/usage.ts`: the five-field shape and zero-fill
  (`safe()`); `add` is the sum used for session totals.
- `packages/schema/src/token-usage.ts`: `TokenUsage.Info` is five required
  finite numbers: `input`, `output`, `reasoning`, `cache.read`, `cache.write`.
- `packages/schema/src/session-event.ts` (`UsageUpdated`): `session.usage.updated`
  carries `{sessionID, cost, tokens}` and is the session's running total.
- `packages/core/src/session/projector.ts` (`applyUsage`, `publishSessionUsage`):
  the total is the sum of every `step.ended`, every `step.failed` that carries
  usage, and `usage.recorded`; it is republished after each of them.
- `packages/ai/src/schema/events.ts` (Usage doc comment) and
  `packages/ai/src/protocols/{anthropic-messages,open-responses,gemini}.ts`:
  what each provider's raw usage means. `nonCachedInputTokens` is the fresh
  part of the prompt; `visibleOutputTokens` excludes reasoning.

Gimble's ported reducers are `internal/sessionstate` (Go) and
`web/src/lib/sessionstate` (TS). Both already fold `session.usage.updated`
into session info by setting `cost` and `tokens`.

## Field meanings (one meaning each, every adapter)

| field | meaning |
| --- | --- |
| `input` | uncached prompt tokens: provider input minus cache read (and minus cache write where the provider folds it into input) |
| `output` | visible output tokens: provider output minus reasoning |
| `reasoning` | reasoning or thinking tokens |
| `cache.read` | prompt tokens served from cache |
| `cache.write` | prompt tokens written to cache |

A field the provider does not report is `0`. Zero is a number. Never omit a
field, never add a flag saying whether it was reported.

`cost` is a number in USD. It is what the harness stated, or `0`.

## Definition of done

1. No `accounting` map in any adapter's native ref. `normalizeUsage` in
   `claude/`, `codex/`, and `agy/` returns the five fields per the table above
   and nothing else. `session.step.ended` carries `tokens` (five fields) and
   `cost`. The Claude projector handles the `result` message and the Codex
   adapter handles `thread/tokenUsage`, so each harness's own turn report is
   captured rather than dropped.
2. `session.usage.updated` is emitted per session as a running total: tokens
   summed over every `step.ended` and every `step.failed` that carries usage,
   cost summed from what the harness stated. It is republished after each
   step and after each harness turn report. A parity fixture covers it in both
   reducers.
3. `TurnEnded` in the run log carries the turn's usage per model in the same
   five fields plus cost, replacing the opaque list of per-step JSON.
   `jsonschema/` is regenerated.
4. A `query.live` remote function implemented in Go streams the token usage
   (five fields plus cost) for a run and a scope: the sum over every session
   whose scope is that scope or is nested under it. It updates live as events
   arrive. The run page shows message tokens and session totals without any
   "unavailable" or "partial" wording.
5. Proof: one live run per harness on the cheap tier (Claude Haiku, Codex
   `gpt-5.6-luna`, Gemini flash). The page shows tokens for all three and
   dollars for Claude. Saved as an attest run under `ephemeral/attest/issue-149/`
   with a README naming the models, the commit, and what was seen. A validator
   agent other than the developer confirms it.

Anything beyond this is filed as a GitHub issue, not built.

## Probe questions (answered in FINDINGS.md)

1. Claude Code `result`: are `total_cost_usd`, `usage`, and `modelUsage`
   per-turn or cumulative across a resumed conversation?
2. Codex `thread/tokenUsage`: the payload on a first turn and on a resumed
   turn; is `total` cumulative for the thread and `last` the latest call?
3. Antigravity: does `input_tokens` include `cache_read_tokens` on a warm cache?

Raw JSONL from the probes lives beside this file and is untracked.
