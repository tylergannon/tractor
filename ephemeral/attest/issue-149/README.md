# Issue 149 token accounting attestation

Proved code commit: `1f6b21a` (branch `claude/issue-148-plan-ae018a`). The
numbers below are the validator's own rerun; the developer's earlier run
(`20260912-204924.issue-149`, commit `96cecfc`) was overwritten because the
attest program clears `logs/` on every start.

Models, as the run log records them natively:

| scope | harness | model asked for | native model in `turn_ended` |
| --- | --- | --- | --- |
| `claude.1` | Claude Code | `haiku` | `claude-haiku-4-5-20251001` |
| `codex.1` | Codex | `gpt-5.6-luna` | `gpt-5.6-luna` |
| `agy.1` | Antigravity | `gemini-3.8-flash-low` | `gemini-3.8-flash-low` |

## What was run

`just build`, then `go run ./ephemeral/attest/issue-149 -port 8119 -hold 5m`
(`main.go` here). It clears `logs/`, starts `web.NewRuntime` with the web
application on 127.0.0.1:8119, and inside one `runtime.Run` opens three child
scopes named `claude`, `codex` and `agy` concurrently through an errgroup. Each
scope holds one `gimble.NewSession` on its adapter and takes two
`Generate[gimble.Text]` turns; the second turn resumes the same conversation, so
the resumed-turn accounting path is exercised on all three harnesses. Both
prompts are two-sentence questions about latency and throughput that need no
tools. After the run the process holds the server open so the page and the live
query can be read.

Run: `20260912-205306.issue-149`, port 8119, page
`http://127.0.0.1:8119/runs/20260912-205306.issue-149`. The run completed with
no error in 9.145 s; all six turns ended `ok`.

## What was seen

### Per session, final `session.usage.updated` (the session's running total)

| session | input | output | reasoning | cache read | cache write | cost |
| --- | --- | --- | --- | --- | --- | --- |
| `claude.1/claude.1` (haiku) | 20 | 130 | 220 | 66341 | 8269 | $0.0249421 |
| `codex.1/codex.1` (gpt-5.6-luna) | 15210 | 112 | 0 | 30208 | 0 | $0 |
| `agy.1/agy.1` (gemini-3.8-flash-low) | 30006 | 101 | 0 | 0 | 0 | $0 |

Claude reports a real cost and heavy cache read/write with almost no fresh
input; Codex reports fresh input plus cache read and no cost; Antigravity
reports fresh input only, no cache and no cost. That is what each harness
states, not a guess by Gimble. (In this run neither Codex nor Antigravity
reported any reasoning tokens; in the developer's earlier run on the same
models they did. That is model behaviour, not adapter behaviour — the claude
reasoning field is nonzero here and the codex/agy reasoning path is covered by
`codex/events_test.go` and `agy` unit tests.)

### The `query.live` remote function

`scopeUsage` is published as `1cqgzir/scopeUsage` in `web/skgo.remotes.json`.
It was streamed with curl at `/_app/remote/1cqgzir/scopeUsage?payload=…`, the
payload being devalue's flat form base64url-encoded, exactly as in
`web/observation_live_test.go`. Frames saved in `validator-live-root.txt`,
`validator-live-claude.1.txt`, `validator-live-codex.1.txt`,
`validator-live-agy.1.txt`. Decoded:

| scope | input | output | reasoning | cache read | cache write | cost |
| --- | --- | --- | --- | --- | --- | --- |
| `""` (root) | 45236 | 343 | 220 | 96549 | 8269 | $0.0249421 |
| `claude.1` | 20 | 130 | 220 | 66341 | 8269 | $0.0249421 |
| `codex.1` | 15210 | 112 | 0 | 30208 | 0 | $0 |
| `agy.1` | 30006 | 101 | 0 | 0 | 0 | $0 |

The root scope is exactly the sum of the three children in all five token
fields and in cost: 20+15210+30006=45236, 130+112+101=343, 220+0+0=220,
66341+30208+0=96549, 8269+0+0=8269, 0.0249421+0+0=0.0249421. Because the run
had already finished, each stream delivered its opening value and ended at
once.

### Arithmetic against the durable logs

Excerpts in `validator-arithmetic.txt` (every `session.usage.updated` and every
`session.step.ended`, all six `turn_ended` `usage` arrays, and the
`grep -c accounting` counts).

- Each session's final `usage.updated` tokens equal the sum of its two
  `step.ended` token sets, field by field. Checked for all three sessions; all
  three match. Claude 10+10 / 64+66 / 115+105 / 29157+37184 / 8027+242; Codex
  10808+4402 / 53+59 / 0 / 9984+20224 / 0; Antigravity 14866+15140 / 50+51 /
  0 / 0 / 0.
- Claude published `usage.updated` four times (after each `step.ended` and
  again after each Claude Code `result`); Codex and Antigravity twice each.
- The Claude session's cost is the sum of its two turn reports:
  0.0198747 (turn 1) + 0.0050674 (turn 2) = 0.0249421, which is the session
  total and the root-scope cost. The `step.ended` events carry cost 0 for every
  harness; the money comes from the harness's own turn report.
- `turn_ended` carries a `usage` array of `{model, cost, tokens}` — the five
  fields per model — and no `tokens` key at all, so the opaque per-step JSON
  list is gone.
- `grep -c accounting` is 0 in all three session JSONLs, in `run.jsonl` and in
  `observation.json`.

### The run page

The Chrome extension was not reachable in the validator's session (the
claude-in-chrome MCP tool timed out at its pre-tool hook), so no browser
screenshot was taken; the page was fetched with curl after the run
(`page-validator.html` / `page-validator.txt`, run status `completed`).

What the server-rendered page shows:

- A session header per session carrying that session's running total:
  claude `20 in · 130 out · 220 reasoning · 66341 cache read · 8269 cache
  write · $0.0249421`, codex `15210 in · 112 out · 0 reasoning · 30208 cache
  read · 0 cache write · $0`, agy `30006 in · 101 out · 0 reasoning · 0 cache
  read · 0 cache write · $0`.
- Per-message tokens for every assistant message on all three harnesses, in the
  five fields: e.g. Claude turn 1 `10 in · 64 out · 115 reasoning · 29157 cache
  read · 8027 cache write · $0`, Codex turn 1 `10808 in · 53 out · 0 reasoning
  · 9984 cache read · 0 cache write · $0`, Antigravity turn 1 `14866 in · 50
  out · 0 reasoning · 0 cache read · 0 cache write · $0`.
- No "unavailable" or "partial" wording anywhere on the page (case-insensitive
  grep over the HTML: 0 matches).
- The "Run usage" line is present. In server-rendered HTML it reads
  `Run usage · connecting`, because its value arrives over the client-side
  `scopeUsage` stream; the value that line renders is the root-scope frame
  above, $0.0249421 with 45236/343/220/96549/8269.

## What looked wrong

1. **Fixed in `1f6b21a`: the per-session running total on the page was always
   zero.** The developer's earlier run showed every session header as
   `0 in · 0 out · …` because both reducers fold `session.usage.updated` only
   into an already-existing `info[sessionID]` record and nothing in Gimble
   creates one. `1f6b21a` changed `SessionTimeline.svelte` and
   `web/src/lib/observation/index.ts` to read the observation store's
   per-session usage keyed by placement instead. Confirmed fixed: in this run
   all three session headers carry the correct nonzero totals, and the Claude
   header carries $0.0249421.
2. **Dollars still never appear on a message line.** Claude's cost is stated by
   the harness in its turn report, so `step.ended` and therefore every rendered
   assistant message shows `$0`. The money is visible on the session header and
   on the "Run usage" line. The dollars themselves are correct and nonzero:
   $0.0249421. Not a blocker for the definition of done, which asks for message
   tokens and session totals.
3. Not a code defect, noted for the record: the server-rendered HTML cannot show
   the live "Run usage" value, and no browser was available here, so that line's
   rendered text was not seen — only the frame the browser would render, taken
   from the same endpoint with the same payload encoding.

## Files

- `main.go` — the attest program.
- `README.md` — this file.
- `logs/runs/20260912-205306.issue-149/` — the validator's durable run:
  `run.jsonl`, `observation.json`, `sessions/{claude.1,codex.1,agy.1}/*.jsonl`.
  (The developer's `20260912-204924.issue-149` was cleared by this rerun; it is
  still in git at commit `1f6b21a`.)
- `page-validator.html`, `page-validator.txt` — the run page after the run.
- `validator-live-root.txt`, `validator-live-claude.1.txt`,
  `validator-live-codex.1.txt`, `validator-live-agy.1.txt` — the `scopeUsage`
  SSE response (headers and frame) per scope.
- `validator-arithmetic.txt` — every `session.usage.updated` and `step.ended`,
  the `turn_ended` usage arrays, and the `grep -c accounting` counts.
- `page-midrun.html`, `page-midrun.txt`, `page-after.html`, `page-after.txt`,
  `page-after-fix.txt`, `live-*.txt`, `usage-log-excerpts.txt`,
  `turn-ended-records.txt` — the developer's earlier evidence, kept as recorded.
- `workspace/` — the (empty) agent working directory the sessions were given.

## Attestation

Validated by an agent other than the developer, on 2026-09-12, in the worktree
`.claude/worktrees/issue-148-plan-ae018a` at commit `1f6b21a`. I did not write
this code or the developer's proof, and I did not commit anything.

### What I ran

- Read `claude/events.go`, `codex/events.go`, `agy/events.go` and compared each
  usage mapping against the OpenCode originals in
  `ephemeral/inspiration/opencode/` and against the probe rules in
  `ephemeral/research/issue-149/FINDINGS.md`.
- `grep -rn 'accounting|tokensAvailable|fieldAvailability' claude/ codex/ agy/
  session.go internal/observation/ web/src`.
- `just build` (exit 0), then `go run ./ephemeral/attest/issue-149 -port 8119
  -hold 5m`.
- `curl` of the SSR run page and of `/_app/remote/1cqgzir/scopeUsage` for the
  scopes `""`, `claude.1`, `codex.1`, `agy.1`.
- Arithmetic over `logs/runs/20260912-205306.issue-149/`.
- `go test ./claude/ ./codex/ ./agy/ ./internal/... ./web/` and
  `cd web && pnpm test`.
- `git diff 2a52971 -- jsonschema/`.

### What I saw

- **Item 1 (translation).** `claudeTokens` passes Anthropic's `input_tokens`
  through unchanged, which is right: OpenCode's `anthropic-messages.ts` sets
  `nonCachedInputTokens` to the same raw field and *builds* its inclusive total
  from it. Output is `max(0, output - reasoning)`, the same expression as
  OpenCode's `visibleOutputTokens` getter. Codex subtracts `cachedInputTokens`
  from `inputTokens` and `reasoningOutputTokens` from `outputTokens`, uses
  `tokenUsage.last` and never `total`, as FINDINGS requires. Antigravity does
  not subtract `cache_read_tokens` from `input_tokens`, which contradicts
  OpenCode's `gemini.ts` but matches the probe finding that the agy CLI already
  reports the uncached remainder; the deviation is stated in the code comment.
  No `accounting` map, `tokensAvailable`, or `fieldAvailability` survives: the
  only `accounting` hits are three prose comments and one test that asserts the
  sidecar is gone. `claude/events.go:87` dispatches `result`;
  `codex/codex.go:314` dispatches `thread/tokenUsage/updated`.
- **Item 2 (running total).** For every session the final
  `session.usage.updated` equals the field-wise sum of its `session.step.ended`
  events — checked by arithmetic, all three match. Claude republished four
  times (after each step and after each `result`), Codex and Antigravity twice.
  `web/src/lib/sessionstate/fixtures/usage-running-total.json` is replayed by
  `internal/sessionstate/usage_test.go` (`TestUsageUpdatedIsTheSessionRunningTotal`,
  PASS) and by `web/src/lib/sessionstate/index.test.ts` in both the
  every-cut restore loop and a dedicated assertion (`pnpm test`: 13 pass, 0
  fail).
- **Item 3 (turn report).** All six `turn_ended` records carry
  `usage: [{model, cost, tokens{input,output,reasoning,cache{read,write}}}]`
  and no `tokens` key. The Claude session's $0.0249421 is exactly
  0.0198747 + 0.0050674 from its two turn reports.
  `git diff 2a52971 -- jsonschema/` shows `LifecycleRecord.json` replacing
  `tokens: [string]` with the per-model `usage` array and regenerating the
  `.sum`.
- **Item 4 (live query and page).** The Go `scopeUsage` live query answered all
  four scopes over SSE at the URL and devalue payload encoding the browser
  uses. Root = 45236 / 343 / 220 / 96549 / 8269 / $0.0249421 = the field-wise
  sum of the three children, exactly. The SSR page shows three session headers
  with nonzero tokens, the Claude header with $0.0249421 and the other two with
  $0, per-message tokens in the five fields for every assistant message, and
  zero occurrences of "unavailable" or "partial".
- **Item 5 (proof).** One live run per harness on the cheap tier (Haiku,
  gpt-5.6-luna, gemini-3.8-flash-low), two turns each so the resume path is
  exercised, six turns all `ok` in 9.145 s.
- The session-total defect the developer recorded is fixed in `1f6b21a`; I saw
  the correct totals on the page rather than zeros.

### Caveats

No browser was available (the claude-in-chrome MCP tool timed out at its
pre-tool hook), so `page-validator.png` does not exist and the client-rendered
"Run usage" line was never seen painted. Its data source was verified directly
at the same endpoint and payload the browser calls.

### Verdict

**VALIDATED.** All five items of the definition of done in
`ephemeral/research/issue-149/README.md` are implemented and were seen working
on a live run on real models at commit `1f6b21a`.
