# Adversarial review — spec migration and supervisors design, round 10

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (48 lines, md5
`1c1d58f0ba7d209af05094cbb8be704c`), with its companion
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

## Round-09 findings status

Both round-09 findings are addressed:

1. Line 16 now carries the allocator's full contract ("It serializes concurrent
   allocations and recovers its sequence by scanning existing segments on
   construction and resume"), and line 17 is a new decision recording the
   walk-side failure categorization ("Return walk-side allocation failures as
   categorized attempt errors through the retry path; patrol-side allocation
   failure only skips that tick") — matching §12.4's split policy.
2. Line 48 now requires `-race` coverage of "atomic segment allocation"
   alongside the append/rotation and checkpoint-save invariants.

Nothing regressed.

## Evidence inspected

- Spec §3.1 run lifecycle (449–477), §3.9 (803–889), §3.10 in full (890–1115),
  §5.3 checkpoint and resume (1659–1728), §5.6, §7.2, §10 event definitions
  including `SupervisorFlushed`/`SupervisorVerdict` (2008–2012), §11.3
  (2211–2222), §12.1–§12.4 (run log in full, 2431–2489).
- Implementation, with attention to every writer that this design makes
  concurrent: `engine/store.go` — `runStore`'s mutex set (`checkpointMu`,
  `timelineMu`), the guarded `appendTimeline` `:95-116`, the **unguarded**
  `appendSteering` `:117-140`, `allocateStage` `:136-163`, stage seq recovery;
  `engine/control.go:118-155` (the `/steer` handler, today the sole caller of
  `appendSteering` at `:149`) and `:157-179`; `engine/runner.go:400-460`;
  `engine/parallel.go`; `harness/backend.go:27-44,78-87,157-195,210-252,283-320,338`;
  `graph/{parse,graph}.go`; `lint/{rules,lint}.go`; `cmd/tractor/root.go`;
  `cmd/agent/main.go`.
- Prior rounds 01–09 under `ephemeral/reviews/`.

## Findings

### 1. issue — supervisor coaching makes the steering audit a two-writer path, the one new concurrent writer the design does not serialize

Line 21 routes walk-target coaching through "the existing steering path … with
origin recorded". §3.10 requires that path to audit: "An accepted delivery is
audited in the active execution's `steering.jsonl` naming the supervisor as
origin", while "External `POST /steer` is untouched: it still addresses the
single active steerable turn" (spec 1063–1067). Both writers therefore append to
the same `steering.jsonl` in the same stage directory, from different goroutines
— the control server's handler and the supervision service's patrol.

That file is written by `engine/store.go:117-140`, which opens with `O_APPEND`,
encodes, and closes with **no lock**. `runStore` carries `checkpointMu` and
`timelineMu` but nothing for steering, because until now the function had
exactly one caller (`engine/control.go:149`) on one goroutine. The design adds
the second caller without saying who serializes them, even though it records the
serialization decision for every other newly-concurrent writer: digest append
and inbox rotation under one lock (line 12), checkpoint saves serialized with
the binding-open save (lines 18, 42), and atomic segment allocation (lines 16,
48). Line 48 then requires `-race` proof of exactly those three and not this one
— and a race here is between two file writes, so `-race` would not surface it
even if a test provoked it.

Impact: two concurrent `Encode` calls on one descriptor can interleave partial
lines, corrupting the audit record §3.9 designates as the evidence that a
steering instruction was accepted — precisely the record a supervised run relies
on to show that a verdict reached the live turn. The fix is one clause naming
the writer or the lock, consistent with what lines 12 and 16 already do.

### 2. nitpick — the pointer a supervisor flush perturbs has no claim

Line 18 decides that `current.jsonl` follows the all-live-turns set, which
implements §12.4's rule that "the moment two turns are live together (branch
turns, or a supervisor flush overlapping the walk), the backend points it at
`events/index.jsonl` and leaves it there until the live count returns to zero".
A supervisor flush is the new way for that stickiness to trigger on an otherwise
serial run, and `harness/backend.go:307,318` shows the existing behaviour keys
on `len(b.live)`, the set line 18 redefines. No claim covers it. The spec calls
`current.jsonl` a best-effort convenience pointer, so this is genuinely small —
but it is the one user-visible artifact a supervisor changes for observers who
are not supervising, and it would cost a clause on claim 39.

## Outcome

material findings remain
