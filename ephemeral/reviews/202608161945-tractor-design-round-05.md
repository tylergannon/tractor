# Adversarial review — Tractor harness slice design, round 05

**Target:** `ephemeral/projects/tractor/design.md` (174 lines, complete current artifact, re-read in full)

**Authoritative sources:** `docs/spec.md` (§3.8, §3.9, §4.5, §5.3–5.4, §11.13 Conformance Program and Required
Scenarios, §12.1–12.4, Appendix D); `/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/
goal-objective.md`; repo instructions (`lefthook.yml` default linters; `go.mod`, Go 1.26 with `tool`
directives); the user's acceptance context (minimal final-state Go; no over-engineering, belt-and-suspenders,
unrequested features or safety, non-free future-proofing, or unnecessary abstraction; idiomatic; actually works
through both harnesses); and the user's standing requirement that this artifact remain a concise, code-free
record of choices, decisions, sequencing, and proof, with local detail deferred to chunk-level design loops.

No caller instruction narrowed the defects, files, or subject matter considered; the whole artifact was
re-reviewed against all of the above.

**Evidence inspected this round (live, this machine):**

- **New Codex probe — unattended tool use currently succeeds only because of ambient developer config.**
  `thread/start {cwd:/tmp/tractor-probe2}` then `turn/start` with **no** `approvalPolicy` and **no**
  `sandboxPolicy` (`gpt-5.6-sol`, effort low), prompt "run a shell command to write hello into
  /tmp/tractor-probe2/out.txt": no server→client approval request arrived, `turn/diff/updated` and
  `turn/completed` followed, and the file was written. The reason is in `~/.codex/config.toml:4-5` —
  `approval_policy = "never"`, `sandbox_mode = "danger-full-access"`, plus per-project
  `trust_level = "trusted"` entries — not in the adapter's request.
- Prior rounds' reproductions re-confirmed as still-load-bearing: rollout materializes ~150 ms after
  `turn/start` and survives `kill -9` with a later `thread/resume` succeeding; `thread/start` alone leaves no
  rollout; Claude Code 2.1.233 rejects `--session-id` reuse after materialization (`Error: Session ID … is
  already in use.`) and reports unknown `--resume` as exit 1 + stderr `No conversation found with session ID`
  with only a generic `error_during_execution` on stdout.
- SDK `v1.1.1-0.20260713164230-efdbecd88a98`: no output-schema option; `ErrSubprocessFailed` only at
  `transport.go:418`; transport unexported on `Client` (`client.go:23`); `compact_boundary` and compaction
  status parsed (`messages.go:766-775`, `messages.go:1425-1444`) — design lines 88, 91–93 remain accurate.
- `/Users/tyler/src/unified-llm/codingagent/cmd/agent/cli.go:68-73,141` — caller detection order and
  `--session` flag confirm design lines 56, 58, 60 replicate the intended CLI rather than inventing surface.
- Spec anchors: 1370–1375 (backend constructed with the restored bindings map — design line 46 is
  spec-shaped, not speculative), 2214–2226 (`run_turn` signature and per-run construction), 2042–2096
  (required scenarios), 2462–2481 (Appendix D).

**Round-04 findings status:** all five addressed. The Codex category rule is now cause-based with the boundary
explicitly deferred to Chunk 3 characterization (lines 74, 76, 112, 134); in-harness model/effort constants and
caller precedence are recorded (lines 56, 58); two of the three chosen dependencies are named with versions
(line 68); the open-ended "may capability-probe" clause is gone (line 66); and the proof list restores the
exact-parts-copy, no-late-callback, and every-Error-categorized assertions (lines 150, 153).

---

## Findings

### 1. ISSUE — the approval, sandbox, and permission-mode decision is absent, so unattended operation depends on ambient host configuration

`grep -Ei "approv|sandbox|permission"` over the current artifact returns nothing. The document decides models
(line 56), lifecycle (73–76), cleanup (95–104), and failure categories (106–112), but never decides how either
harness is told to run tools without a human present. The previous artifact recorded exactly this decision —
turns issued with `approvalPolicy: "never"` and a danger-full-access sandbox because the harness operates in
the caller-supplied workdir, permission-bypass mode on the Claude side, and unexpected native approval requests
answered with a protocol error and made terminal rather than silently approved.

**Why the omission is not benign:** this round's probe shows a policy-free `turn/start` completing a tool-backed
write — but only because `~/.codex/config.toml:4-5` sets `approval_policy = "never"` and
`sandbox_mode = "danger-full-access"` machine-wide with the project marked trusted. On a host without that
configuration the same request draws Codex's ordinary on-request approval flow, and the adapter — which has no
human on the other end and, per spec:2012–2014, may not expose an approval hook to the shared conformance
controller — has nothing recorded about what to answer. The observable symptom is a conformance scenario 1 or 3
long-running task that never returns until the bounded deadline, or workspace writes silently denied by a
workspace-write sandbox, on any machine but this one.

It is also the one decision in the slice with a genuine safety dimension (running a model's shell commands
unattended with full filesystem access in the caller's workdir), which is precisely the sort of choice a
decisions record must retain even when it defers detail. Two sentences — the per-turn policy for each provider,
and that unexpected approval requests are refused rather than auto-approved — restore it without adding
implementation altitude.

### 2. ISSUE — the common-proof list still trims spec-mandated assertions in scenarios 5 and 7

Round 04 corrected the same defect in items 1 and 4; items 5 and 7 were not corrected.

- `design.md:154` — "active steering without queuing inactive steering." Spec:2072–2079 requires more than
  that: the live turn's event stream must contain **exactly one** additional `user` event carrying the
  distinctive text, and the requested workspace effect must contain the unpredictable value. "Active steering"
  can be claimed by an adapter that emits two events, or none, or that never affects the workspace.
- `design.md:156` — "native compaction that blocks to a real completion boundary, followed immediately by a
  remembered-context revisit." Spec:2093–2096 adds two assertions that appear nowhere in the current document:
  compacting an **active** session returns a terminal Error *without disrupting that turn*, which later
  succeeds and writes its finished marker; and compacting an **unknown** session returns a terminal Error.
  Item 4 (line 153) covers unknown sessions for `run_turn`, not for `compact`.

**Impact:** this section is the release gate — line 160 makes both live conformance targets part of the final
gate, and line 173 makes them the definition of done. Assertions omitted here are assertions no chunk is
obliged to implement, and the compact-active case is the one that would catch a `compact` implementation that
disturbs a live turn (the exact failure the Codex adapter's single-live-operation rule at line 40 exists to
prevent).

### 3. NITPICK — the named-dependency list omits the schema validation library

`design.md:68` names `claude-agent-sdk-go` at its pseudo-version and `go-jsonschema` at v0.23.1. It does not
name the JSON Schema validator, although `design.md:36` ("validate the returned object locally against the
caller's exact schema") and Chunk 1's entire proof gate (line 122) rest on it, and its behavior — supported
drafts, strictness, and whether remote `$ref` resolution is off by default — is a property of that specific
choice. `go.mod` currently carries neither the SDK nor either schema library, so nothing else in the repository
records the decision. One clause, symmetric with the two already there.

### 4. NITPICK — nothing states that credentials remain host CLI configuration outside Tractor

`design.md:110` classifies "authentication or configuration problems" as terminal, but the artifact never says
that Tractor performs no authentication of its own and inherits whatever the installed Codex and Claude Code
CLIs are logged into. That is a scope decision with alternatives (env-var passthrough, a credential flag, an
API-key path), it was recorded in the previous artifact, and it belongs beside the scope boundary at lines
19–28 — one line, and it forecloses a chunk loop inventing credential plumbing.

### 5. NITPICK — the contract summary omits the per-turn timeout that later sections depend on

`design.md:34` enumerates the shared contract as "session creation, turns, steering, interruption, compaction,
events, structured results, and categorized failures." The spec's `run_turn` signature carries an explicit
`timeout` argument (spec:2214–2216) whose enforcement is a required scenario (spec:2085–2086) and which the
document itself relies on at lines 112, 136, 144, and 155. Adding the word keeps the one-line contract summary
consistent with the obligations the rest of the artifact assumes.

---

## Verified sound (not findings)

The rewrite continues to hold up against live behavior and against the concision requirement: it is code-free,
decision-shaped, and free of the requirements-document altitude the user rejected. Specifically sound —
retained start-plus-first-turn with resume afterwards and the resumability boundary honestly deferred to Chunk
3 (73–76, 132); cause-based categories with session survivability explicitly not overriding them (76, 112),
which resolves the round-04 defect at its root; static cleanup ownership with no runtime interface assertion
(95–104); Claude local UUID minting with demotion at materialization (84–87), matching the reproduced
"already in use" behavior; native schema flag through the SDK's extra-argument path (88); honest best-effort
steering with the residual race excluded (89–90, spec:2101–2102); no custom transport for an unexposed exit
status (92); in-harness constants with no discovery and Claude-first caller precedence (56, 58) matching the
replicated CLI; restored bindings limited to the CLI continuation path, which matches the spec's own
restored-map construction (46, spec:1374); sequence starting at zero (48); the agent entry point reaching
providers only through the backend (16); and chunk sequencing that puts live characterization before
implementation in both provider chunks (132, 140).

## Outcome

`material findings remain`
