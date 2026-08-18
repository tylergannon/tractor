# Adversarial review — spec migration and supervisors design, round 11

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (50 lines, md5
`2a8d409b0699a3e9cb84c27fe9920194`), with its companion
`ephemeral/projects/tractor/upstream-spec-audit.md` (md5
`449ab707b045ac81d4f7375f8480674a`, unchanged since round 04).

Authorities, unchanged across all rounds:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`;
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `examples/README.md`, `lefthook.yml`;
- `ephemeral/worklog/202608171707-spec-supervisors.md`, including the standing
  correction that designs stay skeletal — decisions, ownership, and sequencing
  only;
- the current implementation.

The launch prompt supplied only the target, the read-only boundary, and the
artifact path — no narrowing of defects, files, or subject matter, no predicted
findings, no requested verdict — so nothing was refused. Scope is the whole
current design.

## Round-10 findings status

Both round-10 findings are addressed:

1. Line 22 is a new decision — "Serialize external and supervisor-originated
   writes to the active stage's steering audit" — and line 50 adds
   "steering-audit serialization" to the required concurrency checks.
2. Claim 41 now covers the `current.jsonl` stickiness a supervisor flush
   induces, including the return to single-turn behaviour.

Nothing regressed.

## Evidence inspected

- Spec §2.5 supervisor node and its example (903–912), §3.1, §3.9 (803–889),
  §3.10 in full (890–1115) with attention to the briefing composition at
  1001–1005 ("The first flush of a session prepends the briefing: the expanded
  `prompt`, the run's `goal`, the verdict contract below, and the supervisor's
  directory location"), §5.3, §5.6, §7.2, §9 extensibility (1952–1983,
  including "**Variable expansion** … `$goal` in node prompts is replaced with
  the top-level `goal` field … applied by the codergen handler at prompt build
  time (Section 4.3)"), §10, §11.3, §12.1–§12.4.
- `ephemeral/projects/tractor/upstream-spec-audit.md`, Nits section, first item.
- Implementation: `engine/codergen.go:41-42` and `engine/fan_in.go:69` — the two
  existing `$goal` expansion sites, both handler-side, both reading
  `scope.Goal` from `ExecutionScope`; `engine/store.go:95-140`;
  `engine/control.go:118-179`; `engine/runner.go:400-460`; `engine/parallel.go`;
  `harness/backend.go:27-44,78-87,157-195,210-252,283-320,338`;
  `graph/{parse,graph}.go`; `lint/{rules,lint}.go`; `cmd/tractor/root.go`;
  `cmd/agent/main.go`.
- Prior rounds 01–10 under `ephemeral/reviews/`.

## Findings

### 1. issue — nobody owns `$goal` expansion for supervisor prompts

§3.10 requires the briefing to carry "the expanded `prompt`" (spec 1002), and
the spec's own supervisor example uses it: `"This run pursues $goal. Watch the
build/verify loop…"` (spec 907). §9 assigns expansion to a place supervisors
never reach: "`$goal` in node prompts is replaced with the top-level `goal`
field — simple string replacement, applied by the codergen handler at prompt
build time (Section 4.3)". Design line 11 keeps supervisors "outside the walk
and handler registry", so no handler runs for them, and the decision list never
says who expands the supervisor prompt.

The code shows why this does not resolve itself. Expansion today is not a shared
operation but two copies, each inside a handler and each reading
`ExecutionScope.Goal`: `engine/codergen.go:41-42` and `engine/fan_in.go:69`. A
supervisor turn has no `ExecutionScope` at all (spec 1310: its workdir is always
the run's top-level workspace, and §3.10 states it produces "a run-log segment …
and no stage directory"), so following the existing pattern means either a third
copy in the supervision service or nothing at all. The project's own audit
already recorded this as a nit against upstream and proposed the fix — "define
one engine-level expansion operation" — but the design that implements Tractor
never picks that up, so the recommendation has no owner either.

Impact: the default outcome is a supervisor briefed with the literal string
`$goal`, which degrades exactly the instruction that tells it what the run is
for — and no claim would catch it. Claim 40 asserts a patrol "receives durable
digests, runs on its own harness session, and records an `ok` or targeted
`steer` verdict"; all of that passes with an unexpanded briefing. This is the
lowest-severity issue of the series, and one clause on line 11 or 18 closes it.

### 2. nitpick — the patrol-side allocation rule is now stated twice

Line 13 ends "a patrol allocation failure skips that tick", and line 17, added
in response to round 09, ends "patrol-side allocation failure only skips that
tick". The second half of line 17 is what makes the split legible, so the
duplication is defensible — but the worklog's standing correction keeps this
document to decisions, ownership, and sequencing, and the artifact has grown
from 27 lines to 50 across ten rounds. Folding the patrol clause into one line
keeps the split intact and the decision list free of restatement.

## Outcome

material findings remain
