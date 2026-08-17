# Adversarial review — Tractor harness slice design, round 06

**Target:** `ephemeral/projects/tractor/design.md` (178 lines, complete current artifact, re-read in full)

**Authoritative sources:** `docs/spec.md` (§3.8, §3.9, §4.5, §5.3–5.4, §11.13 Conformance Program and Required
Scenarios, §12.1–12.4, Appendix D); `/Users/tyler/.codex/attachments/4775d6ef-c6e7-474a-b938-11136a6d62ae/
goal-objective.md`; repo instructions (`lefthook.yml` default linters; `go.mod`, Go 1.26 with `tool`
directives); the user's acceptance context (minimal final-state Go; no over-engineering, belt-and-suspenders,
unrequested features or safety, non-free future-proofing, or unnecessary abstraction; idiomatic; actually works
through both harnesses); and the standing requirement that this artifact stay a concise, code-free record of
choices, decisions, sequencing, and proof, with local detail deferred to chunk-level design loops.

No caller instruction narrowed the defects, files, or subject matter considered.

**Evidence inspected this round (live, this machine):**

- **The new in-harness constants are real and usable.** `claude --print --model fable --permission-mode
  bypassPermissions "reply with exactly: ok"` → `ok`. `claude --help` lists `--effort (low, medium, high,
  xhigh, max)`, so `medium` (design line 60) is a valid selector, and `bypassPermissions` is an accepted
  `--permission-mode` choice (design line 44).
- **The Claude permission decision is implementable through the pinned SDK.**
  `options.go:1334 PermissionModeBypassAll = "bypassPermissions"`, emitted as `--permission-mode`
  (`transport.go:195-196`); the SDK also gates `--dangerously-skip-permissions` behind
  `AllowDangerouslySkipPermissions` (`options.go:68-70`, `transport.go:233-235`) — a chunk-level detail, not a
  design gap.
- **The named dependencies exist at the named versions:** `santhosh-tekuri/jsonschema/v6@v6.0.2` and
  `atombender/go-jsonschema@v0.23.1` are both in the local module cache (v0.24.1 is also cached).
- **Codex approval evidence from round 05 still stands:** a policy-free `turn/start` completed a tool-backed
  write only because `~/.codex/config.toml:4-5` sets `approval_policy = "never"` and
  `sandbox_mode = "danger-full-access"`; design line 44 now makes the per-turn policy explicit, which is what
  removes that host dependence.
- Earlier reproductions re-confirmed as load-bearing: Codex rollout materializes ~150 ms after `turn/start` and
  survives `kill -9` (new-process `thread/resume` succeeds); `thread/start` alone leaves no rollout; Claude
  2.1.233 rejects `--session-id` reuse after materialization; unknown `--resume` yields exit 1 plus stderr
  `No conversation found with session ID` with only a generic `error_during_execution` on stdout.
- Spec anchors: 2002–2007, 2042–2096, 2208–2226, 2462–2481.

**Round-05 findings status:** all five addressed — approval/sandbox/permission decisions recorded (line 44),
proof items 5 and 7 restored to the spec's assertions (lines 158, 160), the validator dependency named
(line 72), authentication scope stated (line 30), and per-turn timeout added to the contract summary (line 36).

---

## Findings

### 1. ISSUE — three of the seven proof items still paraphrase away the assertions that make them discriminating

Rounds 04 and 05 corrected items 1, 4, 5, and 7. Items 2, 3, and 6 were not corrected, and one of them is now
close to vacuous.

- **Item 6 (`design.md:159`)** — "prompt interruption and timeout followed by later reuse of the same session."
  Spec:2081–2086 requires that `run_turn` return an interrupted Error "rather than success **within a bound
  materially shorter than the task's known normal duration**," and, for the timeout half, "before normal
  completion." Without that bound the item is satisfied by an adapter that lets the long-running task run to
  completion and then labels the outcome interrupted — precisely the failure a control implementation is most
  likely to have, and the reason the spec states a timing bound rather than an outcome.
- **Item 2 (`design.md:155`)** — "continuation after discarding and rebuilding the adapter." Spec:2053–2057
  requires the revisit to use an **incompatible strict schema** and to return an unpredictable remembered value
  that appears in "neither the revisit prompt, schema, nor workdir." That is the only assertion in the whole
  program that proves *native session continuity* rather than a fresh session that happened to answer — and
  native session continuity is the headline claim of this document's Purpose (`design.md:5`).
- **Item 3 (`design.md:156`)** — "concurrent independent sessions and rejection of simultaneous turns on one
  session." Spec:2059–2063 requires different workdirs and nonces and that results, events, callbacks, and
  workspace changes "do not mix"; cross-contamination between concurrent sessions is exactly what a shared
  event emitter or a session-keyed map bug produces, and nothing in the current wording would catch it.

**Impact:** this section is the release gate (`design.md:164`, `design.md:177`). An assertion absent here is an
assertion no chunk owes, and items 2 and 6 are the two that certify the properties the slice exists to deliver.

### 2. ISSUE — the harness-consistency invariant is missing, and the in-slice CLI can reach it

`design.md:48` gives the backend "logical thread bindings, **workdir** consistency"; `design.md:114` lists
"bad workdir" among terminal failures; `design.md:132`'s Chunk 2 gate lists "workdir invariants." Harness
consistency appears nowhere.

Spec:2208–2213 is explicit: "Session threads cannot be transported between harnesses; ... A shared thread whose
turns route to different harnesses is both a lint error and a **terminal runtime Error**," and spec:2214–2222
pairs the two invariants directly — a workdir mismatch is terminal "exactly like a harness change."

This is not an out-of-slice engine concern. `design.md:50` keeps restored bindings for the command's
continuation path, `design.md:64` accepts "an exact native session for continuation," and `design.md:58` lets
standalone use name an explicit provider — so `agent --provider anthropic --model … --session <codex-thread-id>`
is a reachable in-slice invocation. With no harness check on the binding, that request routes a Codex thread ID
into the Claude adapter, which (per `design.md:88-90`) treats an ID it did not mint as a native resume
candidate and reports a misleading unknown-session error instead of the spec's terminal harness mismatch. One
clause beside "workdir consistency" and one word in the Chunk 2 gate closes it.

### 3. NITPICK — the command's result contract is unstated, so "notes" has no recorded origin

`design.md:38` requires every result to validate against "the caller's exact schema," and `design.md:64` says
the command "reports useful notes." Nothing says what schema the command supplies. The previous artifact
decided this: a fixed single-successor object with one required `notes` string and `additionalProperties:
false`, deliberately not an arbitrary caller-supplied schema, because the backend returns an Outcome. That is a
choice with alternatives (arbitrary `--schema` flag, raw text passthrough), and it is the link between the
strict-schema rule and the human-facing output the smoke tests assert on (`design.md:140`, `design.md:148`).
One sentence.

### 4. NITPICK — the two proof layers are never distinguished

`design.md:16` states the agent entry point "reaches providers only through the backend," which is the right
lever and is exercised by the smoke tests. But spec:2002–2007 requires the opposite arrangement for
conformance: "the conformance executable calls the adapter **directly** in its own process," with
reconstruction via the ordinary constructor or factory. `design.md:17` and the cleanup section
(`design.md:99-106`) imply direct adapter use without saying so. Since the whole point of having both proofs is
that they cover different layers — adapters directly, and the command only through the backend — the document
should say it in one line rather than leave a reader to infer that conformance also goes through the backend.

---

## Verified sound (not findings)

The artifact now satisfies the concision requirement well and its decisions match the installed tools: the
per-turn approval/sandbox/permission policy that removes host-config dependence (44); in-harness constants
verified live this round (60, 62); named dependencies at versions that exist (72); the retained
start-plus-first-turn lifecycle with the resumability boundary honestly deferred to Chunk 3 (77–80, 136);
cause-based categories with session survivability explicitly not overriding them (80, 116); static cleanup
ownership with no runtime interface assertion (99–108); Claude local UUID minting with demotion at
materialization (88–91), matching the reproduced "already in use" behavior; native schema flag through the
SDK's extra-argument path (92); honest best-effort steering with the residual race excluded (93–94,
spec:2101–2102); no custom transport for an unexposed exit status (96); authentication left to host CLI
configuration (30); restored bindings matching the spec's own restored-map construction (50, spec:1374);
sequence starting at zero (52); and chunk sequencing that puts live characterization before implementation in
both provider chunks (136, 144).

## Outcome

`material findings remain`
