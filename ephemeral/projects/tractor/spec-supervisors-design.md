# Spec migration and supervisors design

Authority: upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`.

## Decisions

- Replace the old pipeline language with the final typed shape: top-level `start`, type-owned routing fields, `success` and `failure` pseudo-targets, and a strict `supervisor` node variant. Keep YAML as a second decoding surface over the identical generated contract.
- Replace Tractor's contradictory local `docs/spec.md` with the pinned upstream document before implementation; Tractor-only YAML support remains an explicit product decision outside that normative copy.
- Remove the generic custom-node catch-all and its `KnownCustomTypes`/`type_known` lint surface. Future extension node types must be explicit schema branches; unknown types become parse errors.
- Keep routing normalization inside `graph` as an engine traversal convenience. Lints continue to apply type-specific routing semantics rather than treating normalized targets as edges.
- Keep supervisors outside the walk and handler registry. A runner-owned supervision service maintains digest inboxes, patrol clocks, verdict delivery, and supervision events.
- Serialize each supervisor's digest append and inbox rotation with one lock, append only after the attempt artifact exists, recover batch numbering by scanning its directory, and never retract digests during parallel rollback.
- Record and absorb supervision-side I/O and turn failures; advisory machinery never fails the walk.
- Keep one engine-wide live-execution registry used by top-level and branch dispatch. Retain the separate top-level active pointer only for unambiguous steering.
- Move run-log allocation to the engine role so registry snapshots can name an existing segment before backend dispatch. The backend remains responsible for event writes, session bindings, and native live-turn controls.
- Put the small shared allocator in `internal/runlog`; both the engine and standalone `agent` CLI call it, without a hidden backend allocation fallback. It serializes concurrent allocations and recovers its sequence by scanning existing segments on construction and resume.
- Return walk-side allocation failures as categorized attempt errors through the retry path; patrol-side allocation failure only skips that tick.
- Extend the backend with supervisor turns, verdict conversion, and a binding-open callback that lets the engine checkpoint before dispatch. Track steerable walk turns separately from all live turns: steering uses the first set; interruption and `current.jsonl` use the second. Supervisor sessions are full, node-keyed, advisory, and non-steerable.
- Treat the supervisor briefing as idempotent, at-least-once input across crashes: only a completed supervisor turn containing the briefing suppresses resend; missing or inconclusive evidence resends, accepting the irreducible duplicate-delivery window documented in `ephemeral/projects/tractor/upstream-spec-audit.md`.
- Use one engine prompt-expansion operation for codergen, fan-in, and supervisor prompts so `$goal` has identical semantics.
- Keep verdict schema conversion in the backend; the engine degrades a conditionally malformed `steer` to recorded `ok` before delivery.
- Use the existing steering path for walk-target coaching, with exact node matching and origin recorded. Supervisor-target coaching appends a digest to the target inbox.
- Serialize external and supervisor-originated writes to the active stage's steering audit.
- Do not add compatibility shims, configuration frameworks, scheduler abstractions, or generalized message buses.

## Sequence

1. Replace the local spec byte-for-byte and record Tractor's YAML input, binding callback, and at-least-once briefing deviations and rationale directly in the README.
2. Migrate the generated graph schema, parser, routing normalization, lint rules, examples, and CLI tests.
3. Migrate traversal, checkpoints, pseudo-target finalization, parallel branch convergence, and engine-owned run logs.
4. Add the backend supervisor turn and the runner supervision service.
5. Run all three shipped steering, parallel, and YAML examples through the real `tractor` CLI, add the supervised live/resume/shutdown/multi-level scenarios, then run full hygiene and independent consensus review.
6. Give the finished binaries and user-level tasks to a fresh validation agent for non-trivial `tractor` and `agent` CLI use through both harnesses.

The audit also records defects or unresolved tradeoffs found in the upstream specification; those are reported separately and do not mutate the authority document.

## Proof claims

- A pipeline authored in the new JSON or YAML language validates and runs through `success`; routing to `failure` produces a deliberate failed result and final checkpoint.
- `tractor validate` rejects unknown node types, illegal defaults and per-type fields, missing or invalid start/success paths, duplicate chooser targets, invalid supervision scopes, supervisor routing targets, and supervision cycles before a run starts.
- A live supervisor patrol receives durable digests, runs on its own harness session, and records an `ok` or targeted `steer` verdict without becoming a walk stage.
- When a supervisor turn overlaps a walk turn, `current.jsonl` points to the segment index and returns to ordinary single-turn behavior afterward.
- Out-of-scope attempts append no digest; a quiet scope opens no session and spends no turn; one supervisor's flush turns never overlap.
- A delivered supervisor steer changes observable work in the active supervised turn; a missed target is recorded and does not fail the run.
- A resumed run preserves supervisor session binding, unflushed backlog, and monotonic batch numbering, and serializes the binding-triggered checkpoint save with walk saves.
- A resume from the briefing-delivery window either finds a completed briefing turn or observably resends the same idempotent briefing; it never continues an unbriefed supervisor silently.
- Stop and normal finalization interrupt and await supervisor turns without making supervisor errors fail the run.
- Multi-level verdict and coaching digests reach every declared recipient and appear in the timeline.
- Existing Codex and Claude harness entry points still perform non-trivial workspace work through the real CLI.

Concurrency checks, including `-race`, prove atomic segment allocation, loss-free append/rotation, steering-audit serialization, and checkpoint-save serialization; they are required checks rather than substitutes for the live CLI claims.
