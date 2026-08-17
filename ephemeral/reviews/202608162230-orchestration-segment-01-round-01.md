# Adversarial review — orchestration segment 01, round 01

Date: 2026-08-16 (local)
Reviewer: Fable (adversarial-review skill)

## Target

Uncommitted implementation segment on branch `codex/orchestration`:

- Harness backend reconciliation (`harness/backend.go`, `harness/backend_test.go`):
  stale-workdir rebinding, run-log sequence recovery, sticky `current.jsonl`
  index pinning.
- New `graph` package (untracked): typed model (`graph.go`), parser
  (`parse.go`), generated schema + decoder (`jsonschema_gen.go`,
  `jsonschema/Graph.json`), generation support (`schema.go`,
  `internal/schemafix/main.go`), tests (`parse_test.go`).
- Supporting `go.mod`/`go.sum` additions (`tylergannon/go-gen-jsonschema`).

Authority reviewed against: `docs/spec.md` Sections 2 (2.1–2.9), 11.1, 11.14,
plus 12.1 and 12.4 (normatively referenced by 11.14), the repository
lefthook gates, and `ephemeral/projects/tractor/orchestration-plan.md`
slices 1–3.

No scope-narrowing instructions were present in the launch prompt; the review
covered the full working tree state.

## Evidence inspected

- Full `git diff` of tracked files; complete reads of every untracked `graph/`
  file and of `harness/backend.go` in final form.
- Spec Sections 2, 11.1, 11.2 (for the parse/lint boundary), 11.14, 12.1, 12.4.
- Plan slices 1–3, validation strategy, and the "leaves the tree green" rule;
  `lefthook.yml` gates.
- Software checks run:
  - `go vet ./harness/ ./graph/ ./graph/internal/schemafix` — clean.
  - `go test -race -count=1 ./harness/ ./graph/` — both pass.
  - `golangci-lint run` over the same packages — 0 issues.
  - `gofmt -l` — clean for harness/graph (flags only `lint/lint.go`, see F1).
  - `go build ./...` / `go test ./...` module-wide — **fails** (see F1).

## Spec conformance verification (summary)

Backend vs 11.14 / 12.1 / 12.4:

- Harness-change check precedes workdir-staleness classification
  (`backend.go:158-170`), exactly per 12.1's "checked BEFORE workdir
  staleness"; a stale workdir rebinds a fresh session in the turn's workdir
  with no Error, replacing the binding. New test
  `TestHarnessBackendStaleWorkdirRebindsAfterHarnessCheck` proves both arms,
  including that the wrong-harness path creates no session on the other
  adapter.
- Segment counter recovery at construction (`recoverEventSequence`,
  `backend.go:326-347`) scans `events/` for the highest `{seq}-*.jsonl`,
  skipping `index.jsonl`, non-`.jsonl` files, and directories; allocation is
  atomic under `b.mu`. `TestHarnessBackendRecoversEventSequenceAcrossReconstruction`
  proves resumption above the highest existing sequence across two
  reconstructions, including index continuity.
- `current.jsonl` pinning now matches 12.4's sticky semantics: pinned to
  `events/index.jsonl` the moment a second turn goes live, held until the live
  count returns to zero, and the next lone turn swaps back to its segment
  (`startTurnLog`/`finishTurnLog`, `backend.go:295-319`). The prior per-turn
  retargeting (repoint at the surviving segment when the count drops to one)
  was correctly removed — that was the "per-turn flapping" 12.4 forbids — and
  the concurrent test now asserts index pinning through drain plus the
  swap-back on the next lone turn.
- Honest logging failure preserved: a segment close failure after a successful
  adapter turn surfaces as a terminal Error (`Run`, `backend.go:122-129`).

Graph vs Section 2 / 11.1:

- Every 11.1 checklist item has a direct test: representative document with
  per-node edges and top-level field extraction; per-type field admission and
  cross-type rejection (schema `additionalProperties: false` per union arm);
  six-field `defaults` whitelist with parse-error on anything else and
  required-field non-satisfaction from defaults; duplicate members at every
  level (preflight scanner); duplicate/invalid node IDs; `null` rejected
  everywhere including inside `custom`; duration grammar
  `^[0-9]+(ms|s|m|h|d)$` with bare numbers rejected; custom node carried
  opaquely with catch-all discriminator arm.
- Defaults resolution honors the per-field, first-value-wins order and the
  "field the type lacks does not apply" rule (`applyDefaults`: LLM six-pack
  for codergen/fan-in, `timeout` only for tool, `max_retries` only for
  custom; nothing for start/exit/parallel) — matches 2.7 exactly.
- Schema/parser agreement is enforced two ways: a fixture table asserting
  both accept/reject in lockstep, and a regeneration drift test
  (`TestGeneratedSchemaIsCurrent`) that reruns the generator plus
  `schemafix` in a temp module and byte-compares all three artifacts —
  satisfying the plan's slice-3 drift requirement.
- `label`/`prompt` presence semantics (absent vs explicit empty) are
  preserved via `jsonschema.Optional`, keeping 2.5's "prompt falls back to
  label", "label defaults to node ID" resolvable downstream.

## Findings

### F1 (issue) — untracked half-written `lint/` package breaks the module-wide gates

`lint/lint.go` and `lint/analysis.go` are present (untracked) and do not
compile: `lint/analysis.go:50-58` calls ~10 undefined `analysis` rule methods
(`a.startNode`, `a.reachability`, …), and `gofmt -l` flags `lint/lint.go`.
Consequently `go build ./...` and `go test ./...` fail at module scope, and
`lefthook.yml` runs `golangci-lint run ./...` and `go test ./...` — so the
pre-commit gates are hard-blocked. The plan's slice rule is explicit: "Each
slice … leaves the tree green." These files look like slice-4 work-in-progress
rather than part of the named segment, and the segment's own packages are
fully green in isolation — but as the tree stands, this segment cannot be
committed through the hooks. Resolution is trivial (finish, stash, or exclude
the `lint/` files before landing slices 1–3), but until then the tree is not
green and the finding is material to landing.

### F2 (nitpick) — `Duration.Parse` is laxer than the published grammar

`graph/graph.go:46-60` delegates to `time.ParseDuration` for non-`d` units, so
the exported `Duration.Parse` accepts `"1h30m"`, `"1.5s"`, and `"-1s"` (and
`"-1d"` via `ParseInt`), all of which the schema pattern and Section 2.2
reject. Unreachable through `Parse()` of a validated document today, but
`Duration` values constructed programmatically in later slices (engine
timeouts, resolved defaults) would silently accept out-of-grammar strings.
Tightening `Parse` to the same `^[0-9]+(ms|s|m|h|d)$` grammar would keep the
two validators from diverging.

### F3 (nitpick) — `OpaqueObject.Values()` aliases internal state

`graph/graph.go:187` returns the internal `map[string]any` by reference; a
handler that mutates the returned map mutates the parsed graph. `maps.Clone`
(shallow) or a documented "read-only" contract would seal it. Low stakes while
the only consumers are tests.

### F4 (nitpick) — dead `case "__custom__"` arm in the generated decoder

`graph/jsonschema_gen.go:160-165` keeps the generator's `"__custom__"`
sentinel case, now byte-identical to the `default` arm `schemafix` installs.
Harmless (a literal `"type":"__custom__"` is a legal custom type and decodes
identically either way), but a one-line dedup in `schemafix` — or a comment
noting the sentinel is intentionally left — would stop a reader from hunting
for a semantic difference.

## Non-findings worth recording

- The removal of the wrong-workdir terminal-error test was checked against the
  spec rather than assumed a regression: 11.14's workdir matrix and 12.1
  changed the semantic to stale-rebind, so the deletion is correct, and the
  replacement test covers more of the matrix than the original did.
- Holding `b.mu` across segment-file creation, index append, and symlink swap
  in `startTurnLog` is the simple way to satisfy 12.4's "allocation is atomic
  across concurrent branch turns"; the critical section is short and I found
  no lock-ordering hazard against `bindingLock`.
- An empty or exotic custom `type` string parses; per 2.4/7.2 that is
  deliberately a lint (`type_known`) concern, not a parse error.
- No over-engineering observed: `schemafix` is a small deterministic
  normalizer covering exactly the three constraints the generator cannot
  express, and it is itself covered by the drift test; the preflight scanner
  is the narrow duplicate/null check the plan called for, nothing more.

## Outcome (round 01)

**material findings remain** — solely F1 (the non-compiling untracked `lint/`
package blocks the module-wide build/test/lint gates); the reviewed segment
code itself carries only nitpicks (F2–F4).

---

# Adjudication — round 02 (same day, later)

Scope of this round: re-adjudicate F1 now that the concurrent lint slice is
complete, verify the focused F2 repair, and re-run the module-wide gates F1
reported blocked. The lint/engine packages themselves get their own review
round and were not content-reviewed here beyond confirming they no longer
break the gates; that deferral does not narrow the segment-1 verdict below.

## F1 — RESOLVED

The `lint` package is now complete (`lint.go`, `analysis.go`, `rules.go`,
`lint_test.go`); new `engine/` files (`state.go`, `store.go`,
`store_test.go`) have also appeared. Module-wide gates, re-run in full:

- `go build ./...` — passes.
- `go vet ./...` — clean.
- `go test -count=1 ./...` — all packages pass, including
  `tractor/lint` and `tractor/engine`.
- `golangci-lint run ./...` (the exact lefthook gate) — 0 issues.
- `gofmt -l .` — no Go files flagged.

The tree is green under every pre-commit gate; nothing blocks landing.

## F2 — REPAIR VERIFIED

`Duration.Parse` (`graph/graph.go`) now enforces the published grammar
directly: it isolates the unit suffix (`ms` checked before the single-letter
units), requires a non-empty all-ASCII-digit number, and only then delegates
to `strconv.ParseInt` (days) or `time.ParseDuration`. Traced against the
round-1 counterexamples: `"1h30m"` (number `"1h30"` contains a non-digit),
`"1.5s"`, `"-1s"`, `"-1d"`, `"1"`, and `""` are all rejected;
`"250ms"`/`"900s"`/`"15m"`/`"2h"`/`"1d"` still parse to the correct values.
The test's rejection loop now asserts `Duration.Parse` itself (not only the
schema) rejects each out-of-grammar string, so the two validators can no
longer diverge silently. `TestDurationSyntaxAndParsing` passes.

## F3, F4 — unchanged nitpicks

`OpaqueObject.Values()` still aliases the internal map, and the generated
decoder retains the dead `"__custom__"` arm. Both remain nitpicks; neither
requires action to land segment 1.

## Current verdict (segment 1)

**only nitpicks remain** — F1 resolved (all module-wide gates green), F2
repaired and verified with test coverage; F3/F4 are genuine nitpicks.
