# Adversarial review — Tractor harness slice design, round 04

**Target:** `ephemeral/projects/tractor/design.md` (170 lines — the artifact was rewritten from 501 lines into a
decisions record since round 03; re-read in full)

**Authoritative sources:** `docs/spec.md` (§3.8, §3.9, §4.5, §5.4, §11.13 Conformance Program and Required
Scenarios, §12.1–12.4, Appendix D); `/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/
goal-objective.md`; repo instructions (`lefthook.yml` default linters, `go.mod` Go 1.26 + `tool` directives);
the user's acceptance context (minimal final-state Go; no over-engineering, belt-and-suspenders, unrequested
features or safety, non-free future-proofing, or unnecessary abstraction; idiomatic; actually works through
both harnesses).

**Caller-supplied additional requirement (accepted, not a scope narrowing):** the artifact must stay a concise,
code-free record of choices, decisions, sequencing, and proof rather than a specific requirements or
implementation document, with local detail deferred to chunk-level design loops. This is a criterion the
artifact is judged against, and it is applied below; it does not restrict which defects were considered, and
the whole artifact was reviewed. No instruction narrowed subject matter, predicted findings, or declared safe
areas.

**Evidence inspected this round (live, this machine):**

- **New Codex reproduction — materialization is effectively immediate and survives a hard kill.** With
  `codex app-server --stdio` (0.147.0): `thread/start {cwd:/tmp/tractor-probe}` →
  thread `01a00d5b-5805-7930-b44f-5345c5276381`; `turn/start` (`gpt-5.6-sol`, effort `low`); **150 ms later**
  `~/.codex/sessions/2026/08/16/rollout-2026-08-16T19-34-45-01a00d5b-….jsonl` already exists. `kill -9` on the
  app-server at that moment, then a **new** process: `thread/resume {threadId}` →
  `{"result":{"thread":{"id":"01a00d5b-…","ephemeral":false,…}}}` — resume succeeds after an aborted,
  never-completed first turn whose owning process was destroyed.
- Round-02/03 reproductions still standing: `thread/start` alone writes no rollout; Claude Code 2.1.233
  `--session-id` reuse after materialization → `Error: Session ID … is already in use.`; unknown `--resume` →
  exit 1 + stderr `No conversation found with session ID` with only a generic
  `error_during_execution` on stdout.
- SDK `v1.1.1-0.20260713164230-efdbecd88a98`: no output-schema option; `ErrSubprocessFailed` constructed only at
  `transport.go:418` (spawn); `WaitForExit` only on the `Transport` interface (`transport.go:79`), `Client`'s
  transport unexported (`client.go:23`) — consistent with design lines 86–87; `compact_boundary` and compaction
  status parsing present (`messages.go:766-775`, `messages.go:1425-1444`) — consistent with line 88.
- `ThreadCompactStartResponse.json` still has no properties; `ContextCompactedNotification` still marked
  deprecated — consistent with lines 72–73.
- Spec anchors used: 2002–2007, 2042–2096, 2098–2107, 2462–2481.

**Round-03 findings status:** all five addressed. Claude fresh demotion now keyed on materialization
(lines 80–82); the Codex category exception is now stated explicitly instead of contradicting the table
(lines 70, 109); the runtime `io.Closer` assertion is gone in favor of static cleanup (lines 92–99); both
compaction shapes are no longer pre-committed (line 73); the exit-status discriminator is dropped (lines 86–87).
The rewrite as a whole meets the new concision requirement well: it is code-free, decision-shaped, and roughly
a third of its previous length without losing the sequencing or proof gates.

---

## Findings

### 1. ISSUE — the Codex "pre-materialization is terminal" rule rests on a premise this round disproved, and it overrides the spec's `interrupted` category

`design.md:70`: "A failure before the first rollout materializes is terminal **even when its underlying cause
would normally look transient or interrupted**. Closing the owning process leaves that thread ID unusable, so
the caller must create a new session." `design.md:109` promotes this to an override of the general categories,
and `design.md:152` quietly narrows the conformance obligation to "later reuse of the same **materialized**
session."

Two problems, in order of severity.

**(a) The stated cause is false as written.** "Closing the owning process leaves that thread ID unusable" was
disproved above: `kill -9` on the app-server 150 ms into the first turn — no turn completion, no graceful close
— left a thread that a *new* process resumed successfully, because the rollout is written essentially at
`turn/start`. The genuinely unusable window is not "before the first turn finishes" but "before `turn/start`
reaches the server at all," which is a much narrower and differently-shaped rule. As written the design licenses
reporting an ordinary rate-limit or transport failure on a first turn as `terminal` — the exact
misclassification round 03 flagged, now legitimized by a rationale the harness does not support.

**(b) Category is defined by cause, not by session survivability.** Appendix D (spec:2470) defines
`interrupted` as "the turn was deliberately stopped: an operator stop via the StopSignal, an interrupt request,
or timeout enforcement," and spec:2478–2479 makes terminal and interrupted fail the run **identically**. So
relabeling a deliberate stop as `terminal` buys nothing operationally while destroying the one thing the
category carries — that an operator stop "is simply a failed run with a 'stopped' reason" (spec:2470). Under
this rule, Ctrl-C during the first turn of a fresh Codex session is reported as a failure rather than a stop.

**Impact:** conformance scenario 6 (spec:2081–2086) requires an interrupted Error and later reuse of "the same
session"; `design.md:152`'s "materialized" qualifier is a paraphrase edited to fit this exception rather than
the spec's obligation. Worse, `design.md:129` says Chunk 3 will *characterize* the retained lifecycle
"including interruption" — so the document has decided the outcome of its own characterization step, in the
direction the live evidence contradicts.

**Direction:** state only what is known — the thread ID is unusable only when `turn/start` never reached the
server — keep Appendix D's cause-based categories everywhere else, and let Chunk 3's characterization set the
boundary rather than pre-committing it. Restore the unqualified scenario-6 wording.

### 2. ISSUE — the in-harness model and effort source is no longer decided anywhere, and discovery is out of scope

`design.md:54`: "When invoked from one supported harness it selects the other; standalone use requires an
explicit provider and model." That decides the *harness*, never the *model*. Meanwhile `design.md:23` puts
"model discovery or a model catalog" out of scope, and `design.md:56` describes the command as accepting a
workdir and prompt.

So for the only invocation the goal names as the definition of done — the caller-aware form with nothing but
caller identity, workdir, and prompt ("when you can actually USE the `agent` CLI, for both chatgpt and claude
models, then you're done") — the document leaves no answer to where the model and reasoning effort come from,
and has closed the two alternatives it does mention (discovery; required explicit flags). The previous artifact
recorded this as a decision: fixed in-harness constants replicating the existing command
(`gpt-5.6-sol`/medium for the Codex direction, `fable`/medium for the Claude direction) with no provider
process or catalog. Also lost is the precedence decision when both `CLAUDE_CODE_SESSION_ID` and
`CODEX_THREAD_ID` are inherited — the nested-caller case that
`/Users/tyler/src/unified-llm/codingagent/cmd/agent/cli.go:68-73` resolves Claude-first, and which the goal
explicitly asked to be copied from that CLI.

This is not local implementation detail that a chunk loop can absorb: "constants, not discovery" and "Claude
detection wins" are choices with alternatives, and one of them is closed by the scope boundary two sections
earlier. Two sentences restore them without adding requirements-document altitude.

### 3. NITPICK — the load-bearing external dependencies are anonymized

`design.md:62` ("the selected Claude Go SDK revision"), `design.md:78` ("the selected Claude Go SDK"), and
`design.md:74` ("the chosen generator"). Native CLI versions are pinned precisely in the same paragraph
(0.147.0, 2.1.233), but the two *chosen* dependencies are not named — even though several decisions are
properties of those exact choices: "the SDK lacks a first-class output-schema option" (line 83), the
unknown-session discriminator (line 86), and "does not add a custom transport merely to recover an unexposed
child exit status" (line 87) are all true of `github.com/roasbeef/claude-agent-sdk-go` at the pinned
pseudo-version and are not guarantees of "a Claude Go SDK" in general. Likewise the generator choice is the
goal's explicit "strongly consider code generation" item and already appears as a `tool` directive in `go.mod`.
Naming a module and a pin is one clause each and is precisely the kind of choice a decisions record exists to
retain.

### 4. NITPICK — "Later implementation may capability-probe" is unbounded latitude that cuts against the document's own restraint rule

`design.md:62`: "Later implementation may capability-probe, but release evidence records the exact versions
actually proved." Nothing in this slice needs a capability probe; `design.md:23` puts discovery out of scope,
`design.md:26` excludes "protocol support not exercised by this slice," and `design.md:168` asks reviewers to
check that "speculative recovery, compatibility, discovery, and future-engine features [are] still absent." A
standing permission to probe is exactly the non-free future-proofing the acceptance context rules out, and it
gives a chunk-level loop cover to add one. Delete the clause and keep the second half, which is the real
decision.

### 5. NITPICK — the common-proof list paraphrases away assertions the spec makes mandatory

`design.md:147` reduces scenario 1 to "meaningful normalized events," dropping spec:2048–2051's specific
obligations: the event stream "opens with an exact copy of that parts array" and "produces no callbacks after
`run_turn` returns." `design.md:150` reduces scenario 4 to "honest categories for … representative operational
failures," where spec:2069–2070 requires that **every** observed Error have an allowed category and non-empty
message — "representative" invites sampling. Since this section is the release gate rather than prose, the
paraphrase is the thing implementers will build to; keeping the spec's binding verbs costs no length.

---

## Verified sound (not findings)

The rewrite's core decisions hold up against live behavior: Codex retained start-plus-first-turn with resume
afterwards (reproduced again this round, including after a killed process); explicit concrete close with no
cleanup method on the neutral contract (lines 69, 92–99); compaction acknowledgement ≠ completion with the
shape deferred to live characterization (lines 72–73); Claude local UUID minting, immediate fresh demotion at
materialization, and resume thereafter (lines 79–82, matching the reproduced "already in use" behavior);
native schema flag through the SDK's extra-argument path (line 83); honest best-effort steering with the
residual race explicitly outside conformance (lines 84–85, matching spec:2101–2102); no custom transport for an
unexposed exit status (line 87); adapters — not the backend — serializing one native session (line 40,
spec:2239); sequence starting at zero with no index rescan (line 48); the agent command reaching providers only
through the backend (line 16); chunk sequencing that puts live characterization before implementation in both
provider chunks (lines 129, 137); and the seven-scenario common flow tracking §11.13's set.

## Outcome

`material findings remain`
