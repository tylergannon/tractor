# Adversarial Review — Implementation Round 01

Date: 2026-08-16 (local)
Reviewer: Fable (adversarial-review skill, performed directly)

## Review target

The complete Tractor harness-slice implementation on branch `codex/implement-attractor`
(HEAD `64d45ca`): package `harness` (contract, validation, exact-schema result
validation, `HarnessBackend`, run log), `harness/claude` and `harness/codex`
adapters, generated `harness/codex/schema`, `cmd/agent`, and
`cmd/harness-conformance`, judged against:

- `/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/goal-objective.md`
- `docs/spec.md` (Sections 4.5, 11.13, 12, Appendices C/D primarily; whole spec surveyed)
- repository instructions (`lefthook.yml` header: default linter configs, no config files/nolint)

Acceptance concerns applied: over-engineering, belt-and-suspenders, unrequested
features/safety, non-free future-proofing, unnecessary abstraction, idiomatic Go,
and whether it actually works. The full implementation and surrounding code were
read; scope was not narrowed beyond the read-only/artifact operating constraints.

## Evidence inspected

- All 17 Go source files (~5,700 lines) read in full or near-full; tests skimmed.
- Retained proof: `ephemeral/projects/tractor/design.md`, both characterization
  records, `claude-live-proof.md` with its three run-log trees, and both
  conformance evidence files (`conformance/codex-0.147.0.json`,
  `conformance/claude-2.1.233.json` — all seven Section 11.13 scenarios passed
  for each harness).
- Worklog `ephemeral/worklog/202608161836-implement-attractor.md` and the six
  design-round reviews.
- Cross-check of caller detection against
  `~/src/unified-llm/codingagent/cmd/agent/cli.go` (`detectAgentCaller` uses the
  same `CLAUDE_CODE_SESSION_ID`-then-`CODEX_THREAD_ID` order and env names).
- Hygiene run this session: `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), `go test ./...` (all pass). Lefthook
  pre-commit hook is installed in the shared `.git` of the worktree.

## Live execution performed this session

I ran the built `agent` binary against both required providers with fresh
unpredictable workspace data and verified every effect independently:

1. **Codex (openai / gpt-5.6-sol, standalone flags).** Seed
   `review-nonce-bdc7bbbc`; the agent wrote `derived.txt` with the seed reversed
   (`cbbb7cdb-ecnon-weiver`) and its SHA-256. I recomputed the hash myself:
   `16cb7185be0c…a47e` — exact match. Exit 0, conforming
   `{"outcome":{"next":"done",…}}`, run log contains 5 matched tool calls.
2. **Claude (anthropic / fable, standalone flags).** Seed
   `review-nonce-78d1d5f5`; the agent wrote the uppercased seed and character
   count `21` (independently correct). Exit 0, conforming outcome, 5 tool
   calls logged.
3. **Caller detection + native continuation.** With `CLAUDE_CODE_SESSION_ID`
   set and no provider/model flags, the CLI selected the Codex harness
   (opposite-harness adversary), resumed session
   `01a00daf-2421-76a2-95e6-02bc7c9b00e2` after the seed and derived files were
   deleted, and recalled `review-nonce-bdc7bbbc` from native session history
   into `recalled.txt`. Exit 0.

The software works, end to end, through both harnesses, including session
reuse, caller-aware routing, run-log emission, and exact-schema structured
output. Scope discipline is good: no graph engine, no credential plumbing, no
model discovery, no speculative recovery; the generated Codex surface is one
request type; the conformance program is spec-mandated (11.13), not gold-plating.

## Findings

### 1. issue — belt-and-suspenders: the output schema is compiled three times per turn

Evidence: `harness/validate.go:40` (`ValidateRunTurnInput` ends with
`NewResultValidator(input.OutputSchema)` and discards the validator),
`harness/validate.go:100` (`ValidateCodergenTurn` does the same),
`harness/claude/adapter.go:107-113` and `harness/codex/adapter.go:118-124`
(each adapter calls `ValidateRunTurnInput` and then immediately compiles the
same schema again with `NewResultValidator`).

A backend-routed turn therefore compiles the identical schema three times
(backend gatekeeper, adapter input check, adapter's real validator), two of the
three thrown away. Within one adapter `RunTurn` the duplication is in the same
call frame. Impact is cognitive rather than performance: two validation layers
that can drift, and a reader must discover they enforce the same thing. Fix by
having `ValidateRunTurnInput` not compile the schema (let the adapter's single
`NewResultValidator` be the schema check), and/or by returning the compiled
validator instead of discarding it.

### 2. issue — belt-and-suspenders: two competing timeout mechanisms in Claude `RunTurn`

Evidence: `harness/claude/adapter.go:130-137` arms a `context.WithDeadline` on
the whole native session for `input.Timeout`; `harness/claude/adapter.go:163-168`
recomputes the residual timeout after `Send`; `harness/claude/adapter.go:392-413`
(`waitTurn`) then enforces the same deadline a second time with a manual
`time.Timer`, whose expiry path performs a native `InterruptWithReceipt` and a
5-second grace window.

Both clocks fire at the same instant, so the session context can cancel the SDK
stream exactly when the graceful native interrupt begins, making the 5-second
grace window largely illusory and creating a race between the two shutdown
paths (conformance passes because both paths classify as `interrupted`).
One mechanism should own timeout: either drop the context deadline and let
`waitTurn`'s interrupt-then-grace path enforce it, or set the context deadline
to `timeout + grace` as a backstop that cannot preempt the graceful path. The
Codex adapter needs only one mechanism (`harness/codex/adapter.go:455-478`),
which shows the simpler shape.

### 3. issue — belt-and-suspenders: `HarnessBackend.logMu` is never used independently

Evidence: `harness/backend.go:37-39` declares `logMu` guarding
`nextSeq`/`latestSegment`; its only two acquisitions,
`harness/backend.go:270-272` (`startTurnLog`) and `harness/backend.go:314-316`
(`finishTurnLog`), both occur while `b.mu` is already held, and the fields it
guards are touched nowhere else. The second mutex adds no concurrency and no
safety — it only obscures which lock protects `live`, `nextSeq`, and
`latestSegment` (all effectively under `mu`). Delete `logMu` and fold its
fields under `mu`.

### 4. nitpick — dead special case for `account/rateLimits/updated`

Evidence: `harness/codex/adapter.go:490` exempts
`account/rateLimits/updated` notifications from the thread/turn match filter,
but the following `switch` has no case for that method, so the exempted
message falls through and is ignored identically to a filtered one. The clause
changes nothing; either emit usage from it or remove the exemption.

### 5. nitpick — `copy` shadows the builtin

Evidence: `harness/backend.go:386` and `harness/backend.go:392` name the local
result `copy` inside `copyAdapters`/`copyStrings`, shadowing the `copy`
builtin. Harmless here, but unidiomatic Go; rename (e.g. `out`).

## Verdict

No critical findings. No missing requirement: adapters, backend, caller-aware
`agent` CLI, Go, hooks, orchestration skill, live conformance, and real
functional proof are all present and verified — including by my own fresh runs
through both providers this session. The three issues are all simplifications
under the mandated belt-and-suspenders/idiomatic-Go criteria, not correctness
defects.

**Outcome: material findings remain**
