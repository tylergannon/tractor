# Adversarial review — spec migration and supervisors design, round 08

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (46 lines, md5
`617824ba11612e1258214f761b329854`), with its companion
`ephemeral/projects/tractor/upstream-spec-audit.md` (md5
`449ab707b045ac81d4f7375f8480674a`, unchanged since round 04).

Authorities, unchanged across all rounds:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`
  (`git show origin/main:docs/spec.md` from `/Users/tyler/src/attractor`,
  2532 lines);
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `examples/README.md`, `lefthook.yml`;
- `ephemeral/worklog/202608171707-spec-supervisors.md`;
- the current implementation.

The launch prompt supplied only the target, the read-only boundary, and the
artifact path — no narrowing of defects, files, or subject matter, no predicted
findings, no requested verdict — so nothing was refused. Scope is the whole
current design.

## Round-07 findings status

All three round-07 findings are addressed:

1. Step 5 (line 29) now names the live re-proof explicitly: "Run all three
   shipped steering, parallel, and YAML examples through the real `tractor`
   CLI, add a real supervised run".
2. Claim 37 is now stated at the CLI seam (`tractor validate`,
   `cmd/tractor/root.go:37-62`).
3. Line 18 now cites the audit by path.

Nothing regressed.

## Evidence inspected

- Spec §3.9 external steering in full (803–889), §3.10 in full (890–1115):
  scope by node ID "including inside parallel branch walks", the inbox and the
  four digest dispositions, the live-execution registry and its mandated
  allocate→create→index→publish→dispatch order, the patrol clock and its
  skip conditions, the verdict schema and engine-enforced conditional rules,
  the walk-target vs supervisor-target delivery split, and "advisory means
  advisory"; §11.3 conformance checklist (2211–2222); §11.2, §12.1–§12.4;
  §2.5–§2.9, §5.3, §5.6, §7.2, §9, §10.
- Implementation: `engine/control.go:118-155` (the `/steer` handler's
  parallel-active rejection) and `:157-179`; `engine/store.go`,
  `engine/runner.go`, `engine/parallel.go`; `harness/contract.go:146-152`,
  `harness/backend.go:27-44,157-195,210-252,283-320`; `graph/parse.go`,
  `graph/graph.go`, `graph/internal/schemafix/main.go`; `lint/rules.go`,
  `lint/lint.go`; `cmd/tractor/root.go:26-80`; `examples/examples_test.go`.
- Prior rounds 01–07 under `ephemeral/reviews/`.

One check that could have produced a finding did not, and is recorded so later
rounds need not repeat it. Line 20 reuses "the existing steering path" for
walk-target coaching, and `engine/control.go:118-155` rejects steering while a
parallel node is the active top-level execution — which would silence
supervisors during fan-out. That is not a defect: spec 1052–1056 requires
exactly this ("rejected while a parallel node is the active top-level
execution -- branch interiors are observe-only; coach at the fan-in"), with the
named-target exact-match refinement line 20 already records. Likewise, line 14's
single registry spanning top-level and branch dispatch, and line 15's move of
run-log allocation ahead of dispatch, match the ordering the registry paragraph
mandates (spec 968–984).

## Findings

### 1. issue — the claim set proves only that supervision acts, never that it stays out of the way

`skills/orchestrate-attractor-loops/SKILL.md:15` requires observable proof
claims, and the design's claims 38–43 otherwise track §11.3's checklist item for
item. What they omit is every negative half of those same items — the
boundaries §11.3 introduces as "integration boundaries inspection of one
component cannot prove":

- **Out-of-scope silence.** Spec 2216 requires that "out-of-scope attempts
  append nothing". No claim asserts it. An over-broad append predicate is an
  easy defect (scope membership is by node ID and must follow the node into
  branch walks, spec 913–915) and it silently pollutes every supervisor's inbox
  and nudge tallies with work it does not supervise.
- **Quiet-scope zero cost.** Spec 2219 requires that "a scope never alive at a
  tick costs zero turns and zero sessions", restating §3.10's "A quiet scope
  costs zero tokens" (spec 966). No claim asserts it. The failure mode is
  economic and unbounded: a supervisor that opens a session and runs a flush on
  every tick regardless of registry contents bills one turn per `interval` for
  the whole run, on every pipeline that declares a supervisor.
- **Flush non-overlap.** Spec 2218 requires that "flush turns for one supervisor
  never overlap and start only at a patrol tick whose registry snapshot holds an
  in-scope entry" — the skip condition is explicit at spec 990–993. No claim
  asserts it. Overlapping flushes violate §12.2's "at most one live turn per
  session" on the supervisor's own session, so the failure surfaces as adapter
  errors or a wedged supervisor rather than as anything the positive claims
  would catch.

Impact: the acceptance contract can be met in full while supervision
over-collects, bills a turn per interval on quiet runs, or double-dispatches on
its own session. These are precisely the properties that make an advisory
subsystem safe to enable by default, and they are the ones an implementer is
least likely to test unprompted.

### 2. nitpick — step 5 asks for one supervised run to carry six supervision claims

Line 29 adds "a real supervised run" (singular) to the live proof. Claims 38–43
span a delivered steer into a live turn, a dropped steer, a resume from the
briefing-delivery window, a resume preserving binding and backlog, stop and
finalize interrupt-and-await, and multi-level verdict plus coaching digests
reaching every listed recipient. No single run produces a crash-window resume
and a clean finalize and a multi-level org chart. The sequencing line understates
its own proof obligation by roughly a factor of three; naming the scenarios (or
saying "supervised runs") keeps step 5 honest about what advancing requires.

## Outcome

material findings remain
