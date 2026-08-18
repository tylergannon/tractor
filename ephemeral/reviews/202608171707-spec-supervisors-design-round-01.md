# Adversarial review — spec migration and supervisors design, round 01

**Target:** `ephemeral/projects/tractor/spec-supervisors-design.md` (31 lines, re-read in full at
md5 `43b0622f0fc99080025237e57787a93b`; the artifact grew from 27 to 31 lines mid-review — decisions
about the custom-node catch-all and the `agent` CLI allocator were added while I was reading, and every
finding below is stated against the 31-line version).

**Authoritative sources used:** upstream `tylergannon/attractor` `origin/main:docs/spec.md` at
`0aca8b748e6ecc23446fc690d2b66690b77fe0d3` (read in full: §2.1–2.9, §3.1–3.10, §4.1–4.7, §5.3, §5.6,
§7.1–7.3, §9, §10, §11.2–11.3, §12.1–12.4, Appendix A); the repository's own normative spec
`docs/spec.md`; `skills/orchestrate-attractor-loops/SKILL.md`; `README.md`; `lefthook.yml`;
`ephemeral/worklog/202608171707-spec-supervisors.md` (including the standing correction that designs
for this project stay skeletal — decisions, ownership, sequencing, no code); and the current
implementation in `graph/`, `lint/`, `engine/`, `harness/`, `cmd/`, `examples/`.

No caller instruction narrowed the defects, files, or subject matter considered; the caller supplied
only a target, a read-only boundary, and an artifact path, all valid operating constraints.

**Evidence inspected (this machine, read-only):**

- Upstream spec anchors: §2.4/§2.8 strict discriminated union; §2.5 `supervisor` fields; §3.2 walk with
  `success`/`failure` finalization; §3.3 offered successors; §3.9 steering (engine-side parallel
  rejection before backend handoff); §3.10 in-graph supervision (inbox, batch rotation, registry,
  patrol, verdict, delivery, sessions/resume, multi-level); §5.3 checkpoint + `sessions`; §5.6
  `supervisors/{id}/inbox*.jsonl`; §7.2 lint table (`supervises_valid`, `supervisor_not_targeted`,
  `supervisor_cycle`, and the tool carve-outs in `edge_target_unique` / `edge_condition_missing`); §9
  exhaustive extension points; §10 `SupervisorFlushed` / `SupervisorVerdict`; §11.3 the six supervision
  DoD items; §12.1 backend invariants including "distinguishing walk turns (steerable) from advisory
  supervisor turns"; §12.4 engine-allocated segments and `current.jsonl` stickiness.
- Repo spec: `docs/spec.md:1-9` (self-declared normative document, derived from a
  `docs/attractor-spec.md` and an `ephemeral/projects/spec-rebuild/north-star.md` that are both absent
  from this worktree); `docs/spec.md:829-836` ("**Supervision is external.** This specification defines
  no in-graph supervisor handler"); `docs/spec.md:902-925` (§4.2 registry, unknown type is a lint-level
  error); `docs/spec.md:1418-1450` (§4.10 Custom Handlers, deleted upstream); §7.4 Custom Lint Rules,
  also deleted upstream. Only `docs/spec.md` exists under `docs/`.
- Implementation: `graph/graph.go:105-194` (`start`/`exit`/`CustomNode` still present, `ToolNode` still
  carries `on_fail` and no `on_success`, no top-level `start`, no pseudo-targets, no `supervisor`);
  `graph/parse.go:15-31,104-168` (strict preflight: nulls, duplicate members, multi-doc — the §2.8
  duplicate-member requirement is already met); `lint/rules.go:395,407,127` (`edge_condition_missing`,
  `type_known`, `dead_end` all read `NodeBase.Edges` uniformly); `engine/control.go:28,118-155,157-179`
  (single `r.active` top-level tracker; engine-side `nodeType == "parallel"` rejection already exists;
  `steering.jsonl` audited into the stage dir); `engine/store.go:65-92` (checkpoint save, atomic
  replace, called only from the walk goroutine today); `engine/parallel.go:110-208,246-291` (branch
  walks, segment attribution by index scan); `harness/backend.go:32-42,209-229,257-326`
  (`startTurnLog` allocates the segment, appends `events/index.jsonl`, swaps `current.jsonl`, and
  registers the turn in the single `live` map that `Steer` uses for cardinality and `startTurnLog` uses
  for index pinning at `:307`).
- `grep -rn 'supervis' --include='*.go' .` → no matches: supervision is entirely greenfield here.

---

## Findings

### 1. The repository's own normative specification is left out of the migration — critical

`docs/spec.md:1-9` declares itself "the normative specification for this project," and
`skills/orchestrate-attractor-loops/SKILL.md` opens every downstream loop with "Read the complete
governing specification and repository instructions" and "Copy or pin external authority locally when
the task requires a stable reference." The design pins authority to an external commit in prose
(design line 3) and its Sequence (lines 19–22) enumerates only code artifacts — generated schema,
parser, routing normalization, lint rules, examples, CLI tests, traversal, checkpoints, run logs,
backend turn, supervision service. Nothing disposes of the in-repo spec, and line 24 addresses only the
*upstream* document ("do not mutate the authority document").

The in-repo document does not merely lag; it contradicts the central decision of this design.
`docs/spec.md:829-836` states "**Supervision is external.** This specification defines no in-graph
supervisor handler," and the same file still defines `start`/`exit` node types, `on_fail` tool routing,
§4.10 Custom Handlers, and §7.4 Custom Lint Rules — all of which upstream deleted and this design
removes. Its header also requires every divergence from upstream to be derivable from
`ephemeral/projects/spec-rebuild/north-star.md`, and neither that file nor the `docs/attractor-spec.md`
baseline it names exists in this worktree, so the stated divergence discipline cannot be executed.

Failure scenario: an implementer or reviewer launched per the orchestration skill reads repository
instructions first, finds `docs/spec.md`, and is told in normative language that supervisor nodes do
not exist and that unknown node types are a registry/lint concern. Either the work is built against the
wrong contract, or every subsequent agent burns a loop rediscovering that the repo's normative document
is dead. The design must record a decision on the in-repo spec — replaced by the pinned upstream text,
retired, or explicitly annotated as superseded with the divergence baseline restored — and place it in
the sequence ahead of implementation.

### 2. Proof claims do not cover resume, stop/finalize, or multi-level supervision — issue

Upstream §11.3 lists six supervision integration items. The design's proof claims (lines 28–31) cover
roughly two and a half of them: language/routing, one live patrol with digests and a verdict, and steer
delivery plus a recorded miss. Three are unclaimed, and they are the ones that cannot be inferred from
the others:

- §11.3 item 4 — "Session open triggers the checkpoint re-save; on resume the patrol clock restarts
  with the walk and the first live patrol flushes the pre-crash backlog, reclaiming the session or
  resending the briefing." Design line 13 correctly assigns the re-save ("checkpointed when first
  bound"), but nothing proves it, and this is the one place where the migration introduces a *second*
  writer of `checkpoint.json`: `engine/store.go:65-92` is today reached only from the walk goroutine
  (`engine/runner.go:298-346`), whereas a flush-triggered re-save arrives from the supervision service's
  goroutine while the walk may be saving. Upstream §3.10 requires it be "serialized with ordinary
  saves." An unproven claim here means a lost or clobbered `sessions` snapshot is discovered by a user,
  not by the loop.
- §11.3 item 5 — stop and Finalize interrupt in-flight supervisor turns and await them before closing
  sessions; turn Errors are consumed without retry.
- §11.3 item 6 — verdict digests reach every listing supervisor and coaching digests reach their target
  (multi-level supervision), with `SupervisorFlushed`/`SupervisorVerdict` in `timeline.jsonl`.

The orchestration skill makes proof claims the phase gate ("State observable proof claims… Passing
tests never overrides failed live proof"), so an unclaimed contract is an unproven one.

### 3. The backend's in-flight-turn tracking is not split, and the design moves the code that maintains it — issue

Upstream §12.1 requires the backend to keep "in-memory tracking of in-flight turns, **distinguishing
walk turns (steerable) from advisory supervisor turns**," and §12.4 requires the *opposite* treatment of
the same turns for `current.jsonl`: the pointer must retarget to `events/index.jsonl` "the moment two
turns are live together (branch turns, **or a supervisor flush overlapping the walk**)". One count
cannot serve both roles. Today exactly one does: `harness/backend.go:35` `live` is registered in
`startTurnLog` (`:318`), read for the index pin at `:307`, and read by `Steer` at `:212-221`, where
`len(b.live) != 1` yields `SteerAmbiguousTarget`.

Design line 13 says supervisor sessions are "non-steerable" and line 11 says the backend keeps "native
live-turn controls," but neither records the required split — and line 11 simultaneously moves segment
allocation to the engine, which relocates `startTurnLog`, the exact function that registers `live`
today. Failure scenario: supervisor flush turns are registered in the single `live` map (the natural
reading of "the backend remains responsible for … native live-turn controls" plus §12.4's requirement
that they pin `current.jsonl`); a 60-second patrol overlapping a codergen turn then makes `len(live)==2`,
so every external `POST /steer` returns 409 `ambiguous_target` for the duration of each flush —
silently degrading the external steering surface that `examples/steering/external-steering.json` is
built to prove. The design should record the ownership split explicitly: which component counts
steerable turns, which counts all live turns, and which now owns the segment counter recovery
(`harness/backend.go:328-349`) that travels with allocation.

### 4. "One uniform offered-target view" collides with the lint carve-outs that are type-specific — issue

Design line 9 keeps routing normalization in `graph` and hands the engine a single offered-target view.
Upstream's lint table is deliberately *not* uniform over that view: `edge_target_unique` exempts tool
nodes ("A tool's `on_success` and `on_error` naming one target is fine — there is no condition pairing
to corrupt"), and `edge_condition_missing` applies only to nodes that route via the choice schema
(codergen and `parallel.fan_in`). §3.2 adds a second asymmetry the normalized view must not flatten: a
parallel node's `next` names its fan-in, which is legal precisely because it is *not* an offered target.

The current lint code is where this bites: `lint/rules.go:395` (`edge_condition_missing`),
`lint/rules.go:127` (`dead_end`), and the target-uniqueness logic all iterate `NodeBase.Edges`
uniformly, because today every node type carries `edges`. Failure scenario: normalization synthesizes
edges for `on_success`/`on_error` and `branches`, the existing edge rules keep iterating them, and a
conforming pipeline — `{"type":"tool","on_success":"deploy","on_error":"deploy"}` — is rejected with a
spurious `edge_target_unique` ERROR, or a two-target tool node is rejected for missing `condition`.
The design should state that lint reads the type-specific fields and that the normalized view is an
engine-internal traversal convenience only.

### 5. The live-execution registry is scoped to a "supervision service," alongside the existing top-level tracker — nitpick

Design line 10 puts "live executions" inside a runner-owned supervision service. Upstream §3.10 and
§12.4 make the registry engine-wide and unconditional: it records *every* dispatched execution — top
level and inside parallel branch walks, including tool executions that carry no segment path — and its
publish-before-dispatch ordering is what makes §12.4's "a registered path always names an existing
file" hold for the walk, not just for supervision. Scoping it to supervision also leaves two live-state
trackers in the codebase: the new registry, and `engine/control.go:28,141` `r.active`, which the same
design (line 14) keeps as the steering path. They answer different questions (registry: everything
live; `r.active`: the single steerable top-level execution), which is defensible, but the design does
not say so, and the branch-walk feed is the part most likely to be missed — `engine/parallel.go:168-208`
`walkBranch` is a separate walk path, and in-scope attempts inside it must still append digests
(§11.3 item 1). One sentence naming the registry as engine-owned and stating that branch walks register
through the same path would close it.

---

**Outcome:** material findings remain
