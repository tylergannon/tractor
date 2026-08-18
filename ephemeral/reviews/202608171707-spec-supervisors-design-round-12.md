# Adversarial review — spec migration and supervisors design, round 12

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (51 lines, md5
`d2f77495082eab02dabc08f19d1c97d3`), with its companion
`ephemeral/projects/tractor/upstream-spec-audit.md` (md5
`449ab707b045ac81d4f7375f8480674a`, unchanged since round 04).

Authorities, unchanged across all rounds:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`;
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `examples/README.md`, `lefthook.yml`;
- `ephemeral/worklog/202608171707-spec-supervisors.md`;
- the current implementation.

The launch prompt supplied only the target, the read-only boundary, and the
artifact path — no narrowing of defects, files, or subject matter, no predicted
findings, no requested verdict — so nothing was refused. Scope is the whole
current design.

## Round-11 findings status

Both round-11 findings are addressed:

1. Line 20 is a new decision — "Use one engine prompt-expansion operation for
   codergen, fan-in, and supervisor prompts so `$goal` has identical semantics"
   — which assigns the owner and also retires the duplicated expansion at
   `engine/codergen.go:41-42` and `engine/fan_in.go:69`.
2. Line 13 no longer restates the patrol-allocation rule; it survives once, on
   line 17, with the walk-side half that gives it meaning. No content was lost
   in the deduplication.

Nothing regressed.

## Evidence inspected

This round closed the last unexamined section of the authority and re-checked
the design end to end for internal consistency.

- Spec §11 Definition of Done in full: §11.1 adapter conformance (2037–2128),
  §11.2 `HarnessBackend` conformance and its integration proof (2129–2210),
  §11.3 in-graph supervision (2211–2222). Also re-read §2.5–§2.9, §3.1, §3.9,
  §3.10 in full, §5.3, §5.6, §7.2, §9, §10, §12.1–§12.4.
- `cmd/harness-conformance/main.go:1-3,26-40` (878 lines) — the repository's
  §11.1 vehicle, documented as "the shared live HarnessAdapter conformance
  scenarios … against one native harness"; and `harness/backend_test.go:23,111,
  151,202,288,297` — the de facto §11.2 scripted-adapter suite, including
  `TestHarnessBackendRecoversEventSequenceAcrossReconstruction` (`:151`), whose
  subject line 16 moves out of the backend into `internal/runlog`.
- `engine/{codergen,fan_in,runner,store,control,parallel}.go`,
  `harness/{contract,backend}.go`, `graph/{parse,graph}.go`,
  `lint/{rules,lint}.go`, `cmd/tractor/root.go`, `cmd/agent/main.go`.
- `ephemeral/projects/tractor/design.md:33` (the repository's precedent design
  and its `## Done` section) and prior rounds 01–11 under `ephemeral/reviews/`.

Coverage check performed this round: every decision on lines 7–24 traces to a
spec section or a recorded Tractor deviation; every §11.3 checklist item has a
claim (39–48); the §12.4 obligations are split correctly between lines 15–17;
and the two engine-side deviations from the pinned authority both have a durable
home in step 1. I found no contradiction between decisions, and no decision that
the sequence leaves unowned.

## Findings

No material findings remain. Two genuine nitpicks:

### 1. nitpick — the deterministic half of the backend's Definition of Done is never named

Claims 39–49 are, without exception, live-run claims, and step 5 buys them with
real CLI runs. §11.2 exists because part of the backend contract is not
reachable that way: it is proven "with **scripted adapters** injected through
the `adapters` map … so every claim below run[s] deterministically without a
provider" (spec 2131–2135). Several of its items are precisely the surface this
design adds — "Advisory supervisor turns (`run_supervisor`) are excluded from
the count and never receive steering", "a nonconforming result surfaces as a
terminal Error" for verdict conversion, "`interrupt_all()` signals every
in-flight adapter turn — walk and advisory alike", and the honest-logging-failure
split that line 17 decides. Producing two simultaneous live walk turns, or a
nonconforming verdict object, from a real harness on demand is impractical; with
a scripted adapter it is a few lines.

The vehicle already exists (`harness/backend_test.go`), so this is not missing
work so much as an unstated pointer: nothing in the artifact says that the new
backend surface is certified there rather than in step 5's live scenarios. Given
that every other proof obligation in this design is explicit, one clause in
step 4 would keep an implementer from reading the claim list as the whole
acceptance set.

### 2. nitpick — no single statement of done

The repository's precedent design closes with a `## Done` section
(`ephemeral/projects/tractor/design.md:33`: "Done means the real `agent` CLI,
through the backend, visibly completes non-trivial workspace tasks with both
Codex and Claude. Tests and linters are hygiene; live conformance … and fresh
independent use are the acceptance proof."). This design distributes the same
information across eleven claims, six sequence steps, and line 51. That is
complete but not summarizable; a two-line `Done` section would let a reader
judge, at a glance, whether the work is finished.

## Outcome

only nitpicks remain
