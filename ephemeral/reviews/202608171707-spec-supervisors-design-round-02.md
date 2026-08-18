# Adversarial review — spec migration and supervisors design, round 02

**Target:** `ephemeral/projects/tractor/spec-supervisors-design.md` (36 lines, complete current
artifact, re-read in full at md5 `6c19ce38b1227bfd4e13484316794886`).

**Authoritative sources used:** upstream `tylergannon/attractor` `origin/main:docs/spec.md` at
`0aca8b748e6ecc23446fc690d2b66690b77fe0d3` (§2.1–2.9, §3.1–3.10, §4.1–4.7, §5.3, §5.6, §7.1–7.3, §9,
§10, §11.2–11.3, §12.1–12.4, Appendix A); the repository's current `docs/spec.md`;
`skills/orchestrate-attractor-loops/SKILL.md`; `README.md`; `examples/README.md`; `lefthook.yml`;
`ephemeral/worklog/202608171707-spec-supervisors.md`, including the standing correction that designs
here stay skeletal — decisions, **ownership**, and sequencing only, no code; and the current
implementation in `graph/`, `lint/`, `engine/`, `harness/`, `cmd/`, `examples/`.

No caller instruction narrowed the defects, files, or subject matter considered; the caller supplied a
target, a read-only boundary, and an artifact path — all valid operating constraints.

**Round-01 findings status — all five addressed:**

1. The stale in-repo normative spec now has a decision (line 8) and a worklog entry: replace
   `docs/spec.md` byte-for-byte with upstream `0aca8b7` before implementation. Verified that
   `docs/spec.md:829-836` ("Supervision is external… defines no in-graph supervisor handler") is what
   that replacement retires.
2. Proof claims now cover resume with session binding and serialized checkpoint save (line 33), stop
   and finalization with consumed supervisor errors (line 34), and multi-level verdict/coaching
   delivery in the timeline (line 35) — §11.3 items 4, 5, and 6.
3. Line 15 now records the split upstream §12.1 requires: steerable walk turns tracked separately from
   all live turns, with interruption and `current.jsonl` on the second set (matching §12.4's pinning
   rule and §12.1's `interrupt_all()`).
4. Line 10 now states that lints keep type-specific routing semantics and that normalization is an
   engine traversal convenience — preserving the tool-node carve-outs in `edge_target_unique` and
   `edge_condition_missing` (§7.2).
5. Line 12 now makes the live-execution registry engine-wide across top-level and branch dispatch, with
   the top-level active pointer retained only for unambiguous steering (§3.9, §3.10).

**Evidence inspected this round (read-only, this machine):**

- Upstream §3.10 in full, re-read for the inbox contract: "Appends and batch rotation must not lose or
  duplicate a line under concurrency"; digests appended "once the engine has written the attempt's
  `outcome.json` or `error.json`"; "The engine never rolls a digest back: the parallel counter rollback
  (Section 4.6) does not retract them, and a replayed fan-out appends new ones"; batch files
  "`inbox.{batch}.jsonl`, monotonic per supervisor" and "permanent — the engine never deletes them…
  the external observer's audit". §11.3 item 1 tests exactly this: "a flush rotation concurrent with
  appends loses no digest". §5.6 fixes the layout `supervisors/{node_id}/inbox.jsonl` +
  `inbox.000042.jsonl`.
- Existing append/rotate/recover patterns the migration will extend:
  `engine/store.go:95-116` (`appendTimeline`, mutex-guarded open/append/close),
  `engine/store.go:117-140` (`appendSteering`, open/append/close, no mutex — single-writer today),
  `harness/backend.go:377-388` (`appendJSONLine`, open/append/close, called under `b.mu` at `:301`).
  Counter recovery already exists twice and by scan, never from the checkpoint alone:
  `engine/store.go:186-204` (stage seq by directory scan) and `harness/backend.go:328-349`
  (segment seq by filename scan); `engine/store.go:151-162` allocates stages under
  `s.state.mu`.
- Parallel rollback that digests must survive: `engine/parallel.go:24-101` snapshots and rolls back
  visit counters on branch failure; `engine/parallel.go:168-208` walks branches on separate goroutines;
  `engine/parallel.go:244-291` attributes segments to branches by `node_id` membership in the branch's
  walked path (confirmed a supervisor's own segments cannot be mis-credited to a branch).
- Steering and registry surroundings: `engine/control.go:28,118-155,157-179` (single `r.active`
  top-level pointer, engine-side `nodeType == "parallel"` rejection, stage-dir `steering.jsonl` audit);
  `harness/backend.go:32-42,209-229,257-326` (single `live` map today driving both `Steer` cardinality
  and `current.jsonl` pinning at `:307` — the split line 15 now records).
- Divergence surface for the byte-for-byte spec replacement: `grep -in yaml docs/spec.md` matches only
  `response.md` frontmatter (`:994`, `:1050`) — YAML input is documented solely in `README.md:3-4` and
  `examples/README.md:13-15`, with `examples/yaml/multiline-tool.yaml` and `examples/examples_test.go`
  enforcing it; `docs/spec.md:1-9` (the header carrying the derivation and divergence discipline, and
  naming a `docs/attractor-spec.md` and `ephemeral/projects/spec-rebuild/north-star.md` that do not
  exist in this worktree) is what the replacement removes.
- `grep -rn 'supervis' --include='*.go' .` → still no matches; supervision remains entirely greenfield.

---

## Findings

### 1. The inbox's durability contract is unrecorded: rotation-vs-append serialization and batch-counter recovery — issue

Upstream §3.10 states two normative durability properties for the supervisor inbox, and §11.3 item 1
makes one of them a Definition-of-Done test. The design assigns ownership of inboxes (line 11: the
supervision service "maintains digest inboxes, patrol clocks, verdict delivery, and supervision
events") but records no decision for either property, even though it deliberately records the
comparable concurrency contracts elsewhere — the two live-turn sets (line 15) and the serialized
binding-triggered checkpoint save (line 33). The proof claims are silent too: claim 31 proves a patrol
receives durable digests, and claim 33 proves resume preserves "backlog", but neither asserts
loss-free rotation under concurrent appends or batch-number continuity.

**(a) Rotation racing appends.** Digest appends originate from every goroutine that finishes an
in-scope attempt — the walk goroutine and, per line 12 and §3.10 ("follows the node everywhere it
executes, including inside parallel branch walks"), each branch goroutine at
`engine/parallel.go:168-208`. Rotation originates from the patrol goroutine. Every append helper in
this codebase is open/append/close (`engine/store.go:95-140`, `harness/backend.go:377-388`), and
rotation to a permanent numbered batch is naturally `os.Rename`. Failure scenario: a branch goroutine
opens `supervisors/scope_cop/inbox.jsonl` with `O_APPEND` and is descheduled; the patrol renames that
inode to `inbox.000007.jsonl`, counts its lines into the nudge tally, and dispatches the flush turn;
the branch goroutine's write then lands in `inbox.000007.jsonl` after the supervisor was told the file
holds N lines. The line is in no future inbox and outside the tally the supervisor read — a silently
dropped digest, precisely what §11.3 item 1 forbids. The design should name the serialization point
(one mutex covering append and rotation, as `appendTimeline` already does for its file) and state that
appends happen only after `outcome.json`/`error.json` is written (§3.10), since the digest carries
`stage_dir` and the supervisor reads it.

**(b) Batch numbering across resume.** Batches are "monotonic per supervisor" and "permanent — the
engine never deletes them… the external observer's audit" (§3.10). Nothing in the design says where
the batch counter comes from after a restart, and it is not part of the checkpoint record (§5.3 lists
`seq` only). Failure scenario: a run flushes `inbox.000001.jsonl` and `inbox.000002.jsonl`, crashes,
and resumes; a supervision service that initializes its counter from zero writes `inbox.000001.jsonl`
at the first live patrol, destroying the pre-crash audit file that the spec says the engine never
deletes. This repository already solves the identical problem twice by scanning the directory rather
than trusting in-memory or checkpoint state (`engine/store.go:186-204`,
`harness/backend.go:328-349`); the design should make the same call explicitly, and claim 33's
"backlog" should be sharpened to include batch continuity.

### 2. Digest immutability under parallel rollback is not stated, and step 2 edits the rollback — nitpick

§3.10 calls this out as non-obvious: "The engine never rolls a digest back: the parallel counter
rollback (Section 4.6) does not retract them, and a replayed fan-out appends new ones — seeing a
genuinely repeated attempt twice is correct." Sequence step 2 (line 22) puts "parallel branch
convergence" in scope, and the rollback it names is live code —
`engine/parallel.go:24-101` snapshots and restores `node_visits` when a branch fails. An implementer
holding both subsystems in the same step, and reading line 11's "maintains digest inboxes" as the only
guidance, has a plausible route to a tidy-looking symmetry ("the fan-out is being replayed, so retract
its digests") that the spec forbids. One clause in the decisions — digests are append-only and never
retracted — removes the ambiguity at zero cost.

### 3. The `docs/spec.md` replacement is a decision without a place in the sequence, and it removes the only record of Tractor's YAML divergence — nitpick

Line 8 correctly decides the replacement and says "before implementation", but Sequence steps 1–4
(lines 21–24) enumerate only code work, so the step most likely to be skipped or deferred under time
pressure is the one that is not on the list. More substantively, the replacement is byte-for-byte
(worklog: "Replace Tractor's stale normative `docs/spec.md` byte-for-byte with upstream `0aca8b7`"),
which deletes `docs/spec.md:1-9` — the header that told a reader this document is derived and that
divergences must be justified. Afterwards the project's normative document asserts JSON-only input
("The document is strict JSON (RFC 8259, UTF-8): no comments, no trailing commas", §2.1) while the
shipped product accepts YAML with comments (`README.md:3-4`, `examples/yaml/multiline-tool.yaml`,
enforced by `examples/examples_test.go`). Line 8 says YAML "remains an explicit product decision
outside that normative copy" but names no location for that record; today the only statements of it are
a README sentence and an example README. Naming the home for the divergence — and putting the
replacement in the sequence — closes both halves.

### 4. The shared run-log allocator's owner is not named — nitpick

Line 14 says the standalone `agent` CLI uses "the same small run-log allocator directly", and line 13
moves allocation to the engine. Which package owns it is left open, and the repository's standing
correction asks these designs for decisions, **ownership**, and sequencing. It matters here because
`cmd/agent` today depends only on `harness/` (its wiring in `cmd/agent/main.go` constructs adapters and
a backend, never the engine), while `engine/` already owns the analogous stage allocator
(`engine/store.go:151-162`). Putting the allocator in `engine` gives the standalone one-shot CLI a
dependency on the pipeline engine; putting it in `harness` leaves allocation in the package the design
just took it out of. One sentence naming the owning package settles it before two implementers pick
differently in steps 2 and 3.

---

**Outcome:** material findings remain
