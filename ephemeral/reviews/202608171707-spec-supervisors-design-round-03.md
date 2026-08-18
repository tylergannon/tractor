# Adversarial review — spec migration and supervisors design, round 03

**Target:** `ephemeral/projects/tractor/spec-supervisors-design.md` (38 lines, complete current
artifact, re-read in full at md5 `b14943bb9a588319424abcc4aa0e57b0`).

**Authoritative sources used:** upstream `tylergannon/attractor` `origin/main:docs/spec.md` at
`0aca8b748e6ecc23446fc690d2b66690b77fe0d3` (§2.1–2.9, §3.1–3.10, §4.1–4.7, §5.3–5.6, §7.1–7.3, §8, §9,
§10, §11.2–11.3, §12.1–12.4, Appendix A); the repository's current `docs/spec.md`;
`skills/orchestrate-attractor-loops/SKILL.md`; `README.md`; `examples/README.md`; `lefthook.yml`;
`ephemeral/worklog/202608171707-spec-supervisors.md`, including the standing correction that designs
here stay skeletal — decisions, ownership, and sequencing only; and the implementation in `graph/`,
`lint/`, `engine/`, `harness/`, `cmd/`, `examples/`.

No caller instruction narrowed the defects, files, or subject matter considered; the caller supplied a
target, a read-only boundary, and an artifact path — all valid operating constraints.

**Round-02 findings status — all four addressed:**

1. Inbox durability is now a decision (line 12): one lock over each supervisor's append and rotation,
   append only after the attempt artifact exists, batch numbering recovered by directory scan, and no
   retraction during parallel rollback. Claim 35 now names monotonic batch numbering.
2. Digest non-retraction under parallel rollback is folded into the same line 12 — matching §3.10's
   "the parallel counter rollback (Section 4.6) does not retract them".
3. The spec replacement is now Sequence step 1, ahead of all code work, with the YAML extension recorded
   in the README (already true at `README.md:3-4`, so the step is a confirmation rather than new work).
4. Line 15 names `engine` as the allocator's owner.

**Evidence inspected this round (read-only, this machine):**

- Upstream advisory-failure rules re-read in full: §3.10 "Steering is its only lever -- it never routes,
  and nothing it does can fail the run"; "A missed or twice-read review must never be the reason a run
  failed"; "**Advisory means advisory.** A supervisor turn that returns an Error is recorded … and
  consumed: no retries, no `max_retries`, no effect on the run"; §12.4 "If allocation, file creation, or
  the index append fails, the engine does not dispatch: a walk attempt returns a categorized Error
  through the ordinary retry path (Section 3.5), **a patrol flush simply does not run that tick**".
- The engine's established bookkeeping-error habit, which the new digest append will sit inside:
  `engine/runner.go:406-409` (stage allocation), `:413-418` (`StageStarted`), `:430-432`
  (`stage.complete`), `:437-445` (`StageCompleted`), `:447-449` (`stage.fail`), `:452-459`
  (`StageFailed`) — every one returns the raw I/O error out of `executeWithRetry`, which
  `engine/runner.go:298-340` turns into a failed run. Same pattern at `engine/runner.go:254-292`
  (pipeline-level timeline writes) and `engine/runner.go:335` (`CheckpointSaved`).
- Append/rotate/recover surroundings: `engine/store.go:95-116` (`appendTimeline`, mutex-guarded),
  `engine/store.go:117-140` (`appendSteering`), `engine/store.go:151-162`, `:186-204` (stage counter by
  directory scan), `harness/backend.go:257-326`, `:328-349`, `:377-388`.
- Package coupling for line 15: `cmd/agent/main.go:1-16` imports only `harness`, `harness/claude`,
  `harness/codex`; `engine` pulls in `graph`, `harness`, and `lint`
  (`grep -rh '"github.com/tylergannon/tractor' engine/*.go`), plus the git-worktree machinery in
  `engine/git_workspace.go`.
- Current unknown-type behavior, against line 9's wording: `graph/internal/schemafix/main.go:129-150`
  rewrites the generated decoder's `default:` branch to decode `CustomNode`, and adds the custom case's
  `not:{enum:<builtins>}` guard, so an unknown `type` passes `graph.Parse`; it is caught only later by
  the `type_known` lint at `lint/rules.go:403-412` (ERROR), configured through
  `lint/lint.go:41,130-133` (`KnownCustomTypes`).
- Delta sweep for behavior this design does not mention: upstream §8 vs local §8.1–8.3 — identical
  resolution rules, the only change being that supervisor turns resolve the same three fields; §5.3,
  §3.4, §3.5 checkpoint/visit/retry semantics unchanged. No unrecorded behavioral migration found
  outside the language, supervision, and run-log areas the design already covers.
- `grep -rn 'supervis' --include='*.go' .` → still no matches; supervision remains greenfield.

---

## Findings

### 1. Nothing records that supervision I/O failures are absorbed, and the digest append lands on the walk's fail-fast path — issue

§3.10 states twice that supervision can never be the reason a run failed, and §12.4 spells out the
asymmetry for the one shared resource: a failed segment allocation is a categorized Error for a *walk*
attempt but merely a skipped tick for a *patrol flush*. The design assigns supervision ownership
(line 11), the inbox's concurrency and ordering contract (line 12), and the allocator's owner (line 15),
but never states that supervision-side I/O failures — digest append, batch rotation, inbox directory
creation, flush segment allocation — are absorbed rather than propagated.

That omission is dangerous here specifically because line 12 puts the digest append on the walk's
critical path: "append only after the attempt artifact exists" places it immediately after
`stage.complete`/`stage.fail` in `executeWithRetry`, and every existing neighbour on that path returns
its I/O error straight out to the caller, failing the run (`engine/runner.go:430-432, 437-445,
447-449, 452-459`). An implementer following the house pattern writes
`if err := supervision.append(...); err != nil { return ..., err }` without a second thought.

Failure scenario: a run declares `scope_cop` over `plan`/`implement`; `{logs_root}/supervisors/` is
unwritable (read-only mount, quota, a stale directory from a prior run owned by another uid). Every
in-scope attempt now fails its execution after the work succeeded and `outcome.json` was written — the
walk dies with an I/O error at the first supervised node, and adding an advisory observer to a working
pipeline has turned it into a broken one. The same latent rule governs the flush path: a failed segment
allocation for a patrol must skip the tick, not fail anything. One decision line — supervision failures
are recorded and swallowed; only walk-side allocation errors enter the retry path — closes it, and it
belongs in the design because it is the invariant that spans the engine, the supervision service, and
the allocator rather than sitting inside any one of them.

### 2. Putting the allocator in `engine` makes the standalone one-shot `agent` CLI depend on the pipeline engine — nitpick

Line 15 resolves the ownership question raised in round 02, but the resolution imports a lot into a
place that had none of it. `cmd/agent/main.go:1-16` today depends only on `harness`, `harness/claude`,
and `harness/codex` — it is a single-turn agent CLI with no graph, no lint, no worktrees. `engine`
depends on `graph`, `harness`, and `lint`, and carries the git-worktree machinery
(`engine/git_workspace.go`); calling into it for a file allocator drags the whole pipeline runtime into
a binary that never parses a pipeline. §12.4 assigns allocation to "the engine" as a role in the
run — the component that dispatches turns — not as a Go package name, so a small dedicated package
(both `engine` and `cmd/agent` importing it) satisfies the decision's actual intent, keeps the "no
hidden backend fallback" property line 15 wants, and leaves the standalone CLI's dependency surface
where it is.

### 3. Two invariants are stated as live-CLI proof claims but are not observable through the CLI — nitpick

`skills/orchestrate-attractor-loops/SKILL.md` requires proof claims to be observable and to separate
hygiene checks from proof through the running software. Claim 35 bundles three observable outcomes
(resume preserves session binding, unflushed backlog, and monotonic batch numbering — all visible in
`checkpoint.json` and `supervisors/{id}/`) with one that is not: "serializes the binding-triggered
checkpoint save with walk saves" is a code invariant whose violation is a rare interleaving, not
something a `tractor` run demonstrates. The mirror-image gap is line 12's loss-free append/rotation
contract, which §11.3 item 1 makes a Definition-of-Done item ("a flush rotation concurrent with appends
loses no digest") and which has no claim at all. Both want the same vehicle — a stress/`-race` check
named as hygiene — rather than an implied live demonstration that will read as proved when nobody
actually proved it.

### 4. "Unknown types remain parse errors" describes a change as a preservation — nitpick

Line 9 says extension types must be explicit schema branches and "unknown types remain parse errors".
Today they are not parse errors: `graph/internal/schemafix/main.go:129-150` rewrites the generated
decoder's `default:` branch so any unrecognized `type` decodes into `CustomNode`, and the rejection
happens one stage later in `lint/rules.go:403-412` (`type_known`), against a set that
`lint/lint.go:41,130-133` lets callers extend. Upstream §2.8 requires the rejection at parse
("Unknown fields are a parse error, at every level"; §2.4 "Any other value is a parse error") and §4.2
depends on it ("an unknown `type` never survives parsing"). The distinction is load-bearing for any
caller that parses without linting, and the specific artifacts to change — the schemafix decoder
rewrite, the `not:{enum:…}` custom case, `type_known`, and `KnownCustomTypes` — are easy to leave in
place while reading line 9 as "already true". Saying "become parse errors" points the implementer at
them.

---

**Outcome:** material findings remain
