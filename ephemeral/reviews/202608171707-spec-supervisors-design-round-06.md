# Adversarial review — spec migration and supervisors design, round 06

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (45 lines, md5
`e4f0a97b25e8bb3e232c19537a649e0d`), with its companion
`ephemeral/projects/tractor/upstream-spec-audit.md` (md5
`449ab707b045ac81d4f7375f8480674a`, unchanged since round 04).

Authorities, unchanged across all rounds:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`
  (`git show origin/main:docs/spec.md` from `/Users/tyler/src/attractor`,
  2532 lines), the document this design pins and will copy byte-for-byte;
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `lefthook.yml`;
- `ephemeral/worklog/202608171707-spec-supervisors.md` (skeletal-design
  correction; decisions on authority pinning, no compatibility layer, recording
  upstream defects without repairing the authority, the binding-open callback
  and at-least-once briefing);
- the current implementation.

The launch prompt supplied only the target, the read-only boundary, and the
artifact path — no narrowing of defects, files, or subject matter, no predicted
findings, no requested verdict — so nothing was refused. Scope is the whole
current design, not only the round-05 deltas.

## Round-05 findings status

All three round-05 findings are addressed:

1. Briefing evidence is now specific and fails safe: line 18 requires "only a
   completed supervisor turn containing the briefing suppresses resend; missing
   or inconclusive evidence resends". That is consistent with
   `harness/backend.go:283-320`, where the segment and its `events/index.jsonl`
   entry exist before dispatch and therefore cannot serve as delivery evidence.
2. The two Tractor-side deviations from the pinned authority now have a home:
   step 1 (line 25) records "Tractor's YAML input, binding callback, and
   at-least-once briefing deviations in the README".
3. The new resume behaviour has an observable claim, line 40.

Nothing regressed. This round's findings are new.

## Evidence inspected

- Spec §2.7 defaults (269–302), §2.5/§2.8 node union, §3.9 steering, §3.10
  in-graph supervision in full (890–1115, including multi-level supervision at
  1100–1113 and sessions/resume at 1085–1098), §4.3 `CodergenBackend` interface
  block (1272–1360: `CodergenTurn`, `SupervisorTurn`, `Verdict`, the five
  interface functions, and the conversion rules), §5.3 checkpoint, §5.6 run
  directory structure (1806–1837), §7.2 built-in lint rules in full
  (1871–1902), §7.3 validation API, §9 extensibility (1952–1983), §10 events,
  §11.3 supervision acceptance checklist (2211–2222), §12.1–§12.4 including the
  `HarnessAdapter` interface and its notes (2316–2388).
- Implementation: `harness/contract.go:146-152` (`HarnessAdapter`),
  `RunTurnInput` and `CodergenTurn` structs; `harness/backend.go:27-44`,
  `:157-195` (`prepareBinding`), `:210-244` (`Steer`, `InterruptAll`),
  `:246-252` (`Bindings`), `:283-320` (`startTurnLog`), `:323-…`
  (`finishTurnLog`); `engine/store.go`, `engine/runner.go`, `engine/control.go`,
  `engine/parallel.go`; `graph/parse.go`, `graph/graph.go`,
  `graph/internal/schemafix/main.go`; `lint/rules.go`, `lint/lint.go:41,141`,
  `lint/lint_test.go:245`; `cmd/agent/main.go`; `README.md:1-10`.
- Prior rounds 01–05 under `ephemeral/reviews/`.

Two checks that could have produced findings did not, and are recorded so the
next round need not repeat them: the existing `HarnessAdapter` already carries
`OutputSchema` (`harness/contract.go:146-152`), so supervisor turns need no
adapter-interface change beyond the backend work line 17 names; and
`prepareBinding` (`harness/backend.go:157-195`) releases `b.mu` before
`CreateSession`, so the line-17 binding-open callback cannot deadlock against a
checkpoint save that reads `Bindings()`.

## Findings

### 1. issue — the proof claims cover only accepted inputs; nothing observably proves the rejections that make the new language safe

`skills/orchestrate-attractor-loops/SKILL.md:15` requires observable proof
claims, separated from hygiene. Sequence steps 1–2 (lines 25–26) are half the
work — spec replacement, generated schema, parser, routing normalization, lint
rules, examples, CLI tests — and the entire enforcement half of that work is
rejection behaviour:

- line 9's strictness ("unknown types become parse errors"), which per §9 is
  the normative rule ("a `type` string the running implementation does not
  define remains a parse error") and which today is defeated by
  `graph/internal/schemafix/main.go:129-150` rewriting the decoder's `default:`
  branch to `CustomNode`;
- §2.7's exactly-six-field `defaults` and structural requiredness, both parse
  errors;
- six new ERROR lint rules in §7.2 — `start_target`, `terminal_reachable`,
  `edge_target_unique`, `supervises_valid`, `supervisor_not_targeted`,
  `supervisor_cycle`.

Claim 36 (line 36) is the only claim touching this work and it exercises a
valid pipeline through `success` and `failure`. No claim asserts that any
invalid document is rejected, and the design never mentions the failure modes
those rules exist to prevent. `supervisor_cycle` is the sharpest case: §7.2
spells out the runtime pathology ("in a cycle, each member's flush turn counts
as live activity for the next member's patrol, so the participants could keep
one another running indefinitely"), and §3.10's multi-level paragraph
(spec 1100–1113) makes the cycle reachable purely through `supervises`
declarations. That rule is the sole guard against a run that burns supervisor
turns forever, and the design neither names the hazard nor claims the guard.

Impact: the acceptance contract can be satisfied end-to-end while every new
strictness rule is missing or wrong, and the one runtime pathology the spec
calls out by name has no owner, no claim, and no mention anywhere in the
artifact.

### 2. nitpick — the durable record of the deviations points into ephemeral work

Line 25 correctly routes the YAML, binding-callback, and at-least-once-briefing
deviations into the README. But line 18's rationale for the third one is
"documented in the spec audit", and that audit lives at
`ephemeral/projects/tractor/upstream-spec-audit.md` — under the path
`skills/orchestrate-attractor-loops/SKILL.md:16` reserves for designs, reviews,
transcripts, and proof. A README that names a deviation from the repository's
own normative `docs/spec.md` while its justification lives only in ephemeral
work leaves the durable tree self-contradictory in the same way line 8 sets out
to fix. One sentence deciding what the README itself must state — or that the
audit becomes durable — closes it.

### 3. nitpick — removing the custom-node catch-all silently breaks an exported lint surface

Line 9 removes the generic custom-node catch-all. That deletes the reason for
`lint.Options.KnownCustomTypes` (`lint/lint.go:41`, consumed at `:141`, covered
by `lint/lint_test.go:245`) and for the `type_known` rule that reads it. Line 21
("no compatibility shims") implies the field simply goes, which is very likely
right, but it is an exported API change in a package the design otherwise
treats as an internal migration target, and neither the decisions nor step 2
mentions retiring rules and options that the new union makes unreachable.

## Outcome

material findings remain
