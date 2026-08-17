# Adversarial Review — Implementation Round 02

Date: 2026-08-16 (local)
Reviewer: Fable (adversarial-review skill, performed directly)

## Review target

The Tractor harness-slice implementation at HEAD `214854f` ("Simplify harness
implementation after review") on `codex/implement-attractor`, judged against the
same authoritative sources as round 01:

- `/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/goal-objective.md`
- `docs/spec.md` (Sections 4.5, 11.13, 12, Appendices C/D primarily)
- repository instructions (`lefthook.yml`: default linter configs, no config files/nolint)

## Evidence inspected

- The full diff `64d45ca..214854f` (the round-01 remediation commit), plus
  re-reading the changed regions in context: `harness/backend.go`,
  `harness/validate.go` + `validate_test.go`, `harness/claude/adapter.go`
  RunTurn/waitTurn, `harness/codex/adapter.go` readTurn. The rest of the
  implementation was read in full during round 01 and is unchanged.
- Refreshed conformance evidence at HEAD:
  `ephemeral/projects/tractor/conformance/codex-0.147.0.json` and
  `claude-2.1.233.json`, both regenerated 2026-08-17T03:12–03:15Z (minutes
  before the HEAD commit at 21:15:38 local), both `passed: true` across all
  seven Section 11.13 scenarios — including `interruption_and_timeout`, which
  exercises the reworked Claude timeout path against the live harness.
- Hygiene re-run this session at HEAD: `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), `go test ./...` (all pass).

## Round-01 findings: all verified fixed

1. **Triple schema compilation** — `ValidateRunTurnInput` and
   `ValidateCodergenTurn` no longer compile the output schema
   (`harness/validate.go:37-40`, `:96-99`); each adapter turn now compiles it
   exactly once via its own `NewResultValidator`
   (`harness/claude/adapter.go:110`, `harness/codex/adapter.go:121`), still
   before any native activity.
2. **Dual Claude timeout mechanisms** — the session context deadline is now a
   backstop at `turnDeadline + controlTimeout`
   (`harness/claude/adapter.go:134`), so it can no longer preempt the graceful
   native interrupt-plus-grace window; an already-expired residual timeout is
   clamped to 1ns (`:163-167`) so it routes through the same graceful interrupt
   path instead of returning without interrupting. Verified live by the
   refreshed `interruption_and_timeout` conformance pass.
3. **Redundant `logMu`** — deleted; `mu` alone guards `nextSeq`,
   `latestSegment`, and `live` (`harness/backend.go:267-268`, `:308-309`).
4. **Dead `account/rateLimits/updated` exemption** — removed
   (`harness/codex/adapter.go:490`).
5. **`copy` shadowing the builtin** — renamed to `out`
   (`harness/backend.go:379-388`).

No new defects were introduced by the remediation: the schema is still rejected
terminally before any native turn starts, and the timeout path is now
single-owner with a true backstop.

## Live execution performed this session (at HEAD)

I rebuilt `agent` from HEAD and ran one fresh tool-backed turn per provider
with new unpredictable seeds, verifying workspace effects myself:

- **Codex (openai / gpt-5.6-sol):** seed `r2-57631e` → `out.txt` contains
  `r2-57631e:ok`; exit 0, conforming outcome, session
  `01a00db8-d75b-70f0-9b93-d426a5fa774a`.
- **Claude (anthropic / fable):** seed `r2-f84ece` → `out.txt` contains
  `r2-f84ece:ok`; exit 0, conforming outcome, session
  `cc5a10b3-3956-4dc3-855f-33ae170122b7`.

Combined with round 01's deeper live proof (independently recomputed SHA-256
via Codex, character-count verification via Claude, caller detection, and
native session continuation across processes), the current code demonstrably
works through both required providers.

## Findings

### 1. nitpick — `ValidateResult` is exported but has no production caller

Evidence: `harness/result.go:66-73`; its only uses are in its own unit tests
(`harness/result_test.go:28`, `:90`). Every production path constructs
`NewResultValidator` directly. A one-call convenience wrapper with no consumer
is a small unrequested API surface; inline it into the tests or drop it when
next touching the file. Not worth a dedicated change round.

## Verdict

All round-01 material findings are fixed, hygiene is clean, live conformance
was regenerated at HEAD for both harnesses, and fresh live CLI runs through
both providers succeeded with verified workspace effects. The single remaining
finding is a genuine nitpick.

**Outcome: only nitpicks remain**
