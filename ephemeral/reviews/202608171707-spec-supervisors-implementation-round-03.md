# Adversarial review — spec migration and in-graph supervisors (round 03)

## Review target

Worktree `/Users/tyler/src/.worktrees/tractor/spec-supervisors`, HEAD `06b8621`,
41 modified tracked files plus untracked `engine/supervisor.go`,
`engine/supervisor_test.go`, `internal/`, and the ephemeral project artifacts.
The tree changed again after round 02 (18:34): `engine/supervisor.go` 18:35,
`engine/supervisor_test.go` 18:36, `README.md` 18:36, and — new to this round —
`harness/codex/schema_compat.go` (new file), `harness/codex/adapter.go` 18:37,
`harness/codex/adapter_test.go` 18:39.

## Authorities

- `docs/spec.md`, re-verified byte-for-byte identical to
  `/Users/tyler/src/attractor` `origin/main:docs/spec.md`
  (`0aca8b748e6ecc23446fc690d2b66690b77fe0d3`).
- `README.md` (the enumerated Tractor deviations) and repository instructions.
- `ephemeral/projects/tractor/spec-supervisors-design.md` (decisions, sequence,
  proof gates) and `ephemeral/projects/tractor/upstream-spec-audit.md`.
- `ephemeral/worklog/*`, including this repository's recorded live discoveries
  and its established proof discipline for provider-facing behavior.

No caller instruction narrowed the subject matter. The read-only constraint and
the artifact path were honoured; scope was derived from the authorities above.

## Evidence inspected

- `harness/codex/schema_compat.go` in full (113 lines);
  `harness/codex/adapter.go` (`RunTurn`, `turnStartParams`) and its diff;
  `harness/codex/adapter_test.go` new test and the protocol test double
  (`newProtocolTestAdapter`, 243-255); `harness/result.go`
  (`NewResultValidator`, `Validate`); `harness/backend.go` (`RunSupervisor`,
  `decodeVerdict`).
- `engine/supervisor.go` in full (600 lines); `engine/runner.go` (`Stop`,
  `StopSignal`, `Run`, `saveCheckpoint`, `executeWithRetry`);
  `engine/codergen.go` (`choiceSchema`); `cmd/agent/main.go` (`outcomeSchema`).
- `engine/supervisor_test.go` in full (14 tests, including the new
  `TestStopPreventsNewPatrolWhileWorkDrains`).
- `cmd/harness-conformance/main.go` scenario table; `examples/` tree;
  `README.md`; `ephemeral/worklog/202608171707-spec-supervisors.md`;
  `ephemeral/projects/tractor/` artifact listing.
- `docs/spec.md` §§3.9, 3.10, 5.6, 10, 11.2, 11.14, 12.1, 12.2, 12.4.
- `go build ./...`, `go vet ./...`, `go test ./...`,
  `go test -race ./engine/... ./harness/...` — all pass.

Round-02 findings 2 (patrol clocks ignoring the operator stop signal) and 4
(stale README deviation text; undocumented `errors.jsonl`) are fixed:
`patrol`/`flush` now observe `s.runner.stop` (`engine/supervisor.go:197, 200,
325, 362`), `TestStopPreventsNewPatrolWhileWorkDrains`
(`engine/supervisor_test.go:297-344`) is a genuine repro-shaped test, and
`README.md:17-26` now describes the binding-record rule and `errors.jsonl`.
Round-02 nitpick 5's swallowed marshal error is fixed (`renderNudge` returns an
error, `engine/supervisor.go:412`). Round-02 finding 1 received a fix whose
proof is the subject of finding 1 below; round-02 finding 3 is unchanged.

## Findings

### 1. critical — the new Codex schema-compatibility layer is verified only against a mock, so the silent-supervision failure it was written to prevent may still be live

`codexCompatibleOutputSchema` (`harness/codex/schema_compat.go:12-60`) rewrites
the caller's forced output schema before dispatch: every optional root property
is appended to `required` and made nullable — `"type"` becomes
`["string","null"]` (`:62-79`) and any `enum` gains a literal `null` member
(`:81-96`). `omitOptionalNulls` (`:99-113`, called at
`harness/codex/adapter.go:177-181`) then strips those nulls from the result
before the caller's unmodified validator runs. For the spec §3.10 verdict schema
(`engine/supervisor.go:399-408`) this means Codex is shown a three-required
schema in which `target`'s enum is `[...scope..., null]`.

Every assumption in that design is about Codex's native behavior, and none of
them is checked against Codex:

1. that Codex accepts a type-array `["string","null"]` in a strict output schema;
2. that it accepts a `null` member inside an `enum` under
   `additionalProperties: false`;
3. that the model then emits an explicit `null` rather than omitting the key.

The only test, `TestAdapterCodexStrictSchemaCompatibilityPreservesCallerSemantics`
(`harness/codex/adapter_test.go:101-137`), runs against
`newProtocolTestAdapter` (`:243-255`) — a helper process that replays canned
JSON-RPC responses and never validates a schema. It asserts what Tractor *sends*
and what Tractor *strips*; it cannot assert what Codex *accepts*. Assumption 3
is not tested even against the mock in the "model omits the key" direction.

Failure scenario: if 1 or 2 is wrong, `turn/start` is rejected exactly as before,
`RunSupervisor` returns a terminal Error, and spec §3.10's advisory contract
absorbs it — `recordVerdict` writes `SupervisorVerdict{verdict:"error"}`
(`engine/supervisor.go:431-439`) and the run proceeds. Supervision is inert for
the entire run with no failure signal, no non-zero exit, and no test that can
tell the difference. The fix now also *looks* like it closed the hole, which is
worse than the round-02 state.

This is the one class of claim this repository has consistently insisted on
proving live: the motivating fact itself is a live discovery
(`ephemeral/worklog/202608161836-implement-attractor.md:32`, "Codex 0.147.0
rejects native structured-output schemas unless the top-level `required` array
includes every declared property"), and
`ephemeral/worklog/202608170739-live-examples.md` records repeated rejections of
non-live proof for provider-facing behavior. One real Codex `run_supervisor`
turn — schema accepted, verdict returned, `ok` and `steer` both — settles all
three assumptions and belongs in the worklog as a `proof:` line. The same turn
against Claude would close the symmetric, equally unproven assumption that
Claude accepts the spec schema as written.

### 2. issue — supervision still has no live proof, no example, and no backend-seam conformance scenario (unchanged from round 02)

`ephemeral/projects/tractor/spec-supervisors-design.md` step 5 requires live CLI
proof and supervised scenarios; spec §11.14 (`docs/spec.md:2215`) routes
supervisor backend-seam conformance to §11.2, whose **Verdict conversion** item
(`docs/spec.md:2182-2186`) is a live-harness obligation.

Nothing moved: `cmd/harness-conformance/main.go:147-157` still registers the
same seven scenarios with no supervisor case (`grep -rn RunSupervisor cmd/`
returns nothing, and the file's mtime predates the entire supervisor effort);
`examples/` still holds only `parallel`, `steering`, and `yaml`;
`ephemeral/projects/tractor/` has no supervision proof directory; and
`ephemeral/worklog/202608171707-spec-supervisors.md` still carries no `proof:`
line, unlike every comparable feature in this repository.

Impact beyond the missing gate: this is the mechanism that would have caught
finding 1 before the compat layer was written, and the mechanism that would
catch it now. Adding a `run_supervisor` scenario to the existing conformance
controller is small — it already owns session creation, turn dispatch, and
evidence capture — and it converts finding 1 from an open question into a
recorded fact.

### 3. issue — rewriting the provider-facing output schema is a fourth deviation, recorded nowhere

`README.md:6` states "Tractor has three documented implementation choices around
that contract" and enumerates YAML input, the binding-open callback, and the
idempotent briefing. The Codex adapter now silently changes the schema the model
is shown: properties the spec deliberately left optional become required, enums
gain a `null` member, and the result is edited before validation
(`harness/codex/schema_compat.go:39-59`, `:99-113`;
`harness/codex/adapter.go:177-181`). Spec §3.10 is explicit that only `verdict`
is structurally required and that the conditional rules are engine-enforced
after decoding; the model on Codex is now told otherwise.

That may well be the right engineering answer, but the repository's own decision
is to record it: `ephemeral/worklog/202608171707-spec-supervisors.md` says
"Record upstream specification defects and ambiguities as findings with section
references". `ephemeral/projects/tractor/upstream-spec-audit.md` (4170 bytes,
last written 17:58, before this change) contains no entry for "the normative
verdict schema is rejected by a supported provider" — the strongest upstream
defect this migration has surfaced. The README's deviation list, the audit, and
the worklog are all silent, so a future reader sees a spec-exact
`verdictSchema()` in the engine and no hint that the wire schema differs.

### 4. issue — the compat layer newly rejects caller schemas the harness seam accepts, and only normalizes the root

`codexCompatibleOutputSchema` returns an error when the root has no `properties`
object (`harness/codex/schema_compat.go:17-20`), and `turnStartParams` turns
that into a terminal Error (`harness/codex/adapter.go:429-432`).
`NewResultValidator` (`harness/result.go:22-43`) requires only that the schema
be a JSON object with root `"type": "object"` — `properties` is not required,
and neither are the `$ref`/`allOf`/`patternProperties` forms a compiled JSON
Schema may legitimately use.

Reproduction: call `codex.Adapter.RunTurn` with a valid
`OutputSchema` of `{"type":"object","additionalProperties":false}`. Before this
change the schema was forwarded and the turn ran; now the adapter fails locally
with terminal `decode output schema: output schema root has no properties
object`. The Claude adapter still accepts the same input
(`harness/claude/adapter.go:110-114`), so the two adapters now disagree about
which caller inputs are valid — a divergence spec §12.1 does not contemplate,
and one §11.2's `invalid_inputs_and_public_errors` scenario does not cover.

Related and unhandled: only root-level properties are normalized. A nested
object with optional properties — legal for any caller of this public seam —
still reaches Codex in the form the worklog says it rejects. No Tractor-owned
schema is nested today (`engine/codergen.go:184-208` and
`cmd/agent/main.go:26-31` list every property in `required`, so the layer is a
no-op for them), which means the entire mechanism exists for the verdict schema
alone while being written as a general one.

### 5. nitpick — an aborted flush leaves a stray run-log segment and an index line for a turn that never ran

`flush` allocates the segment first (`engine/supervisor.go:328`), then rotates,
appends `SupervisorFlushed`, reads the briefing record, renders the nudge, and
builds the turn — any of which can return early (`:330-347, 351-357`), as can
the new stop checks (`:325, 362`). Each early return leaves a created segment
file and its `events/index.jsonl` line describing a supervisor turn that was
never dispatched. Spec §12.4's ordering requires allocation before dispatch, so
this is compliant, but an observer reading the index cannot distinguish an
allocated-then-abandoned segment from a turn that produced no events. Deferring
allocation to just before `beginExecution`, or recording the abandonment in
`errors.jsonl` alongside the existing entries, would keep the index honest.

## Outcome

material findings remain
