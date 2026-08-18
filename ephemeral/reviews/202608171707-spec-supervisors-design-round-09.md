# Adversarial review — spec migration and supervisors design, round 09

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (47 lines, md5
`daf5e06aee64af2b54156c87b2230c47`), with its companion
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

## Round-08 findings status

Both round-08 findings are addressed:

1. Claim 39 now states the three negative properties: "Out-of-scope attempts
   append no digest; a quiet scope opens no session and spends no turn; one
   supervisor's flush turns never overlap."
2. Step 5 (line 29) now names the scenario set: "the supervised
   live/resume/shutdown/multi-level scenarios".

Nothing regressed.

## Evidence inspected

- Spec §12.4 Run Log in full (2431–2489), which is the normative contract for
  the ownership move the design makes at lines 15–16: engine allocates the
  sequence, creates the file, appends the index, publishes the registry entry,
  then dispatches; "Allocation is atomic across concurrent branch turns, and
  the counter is recovered at startup and resume from the highest sequence
  already present in `events/`"; and the failure policy quoted in finding 1.
- Spec §3.9 (803–889), §3.10 in full (890–1115), §3.5 retry logic, §4.1
  `ExecutionScope`, §5.3, §5.6, §7.2, §9, §10, §11.2–§11.3, §12.1–§12.3.
- Implementation: `engine/runner.go:400-460` (`executeWithRetry`: every
  bookkeeping failure — `allocateStage` at `:406-409`, `StageStarted` at
  `:413-418`, `stage.complete` at `:430-432`, `StageCompleted` at `:437-445`,
  `stage.fail` at `:447-449`, `StageFailed` at `:452-459` — is returned as a
  raw error past the retry loop, failing the run, in contrast to handler errors
  which are categorized and retried); `harness/backend.go:78-87` and `:338`
  (`recoverEventSequence`, the startup/resume counter recovery that today lives
  in the backend constructor), `:283-320` (`startTurnLog`, whose allocation and
  index-append failures return `terminalError`), `:27-44`, `:157-195`,
  `:210-252`; `engine/{store,control,parallel}.go`; `graph/{parse,graph}.go`;
  `lint/{rules,lint}.go`; `cmd/tractor/root.go`; `cmd/agent/main.go:1-16`.
- Prior rounds 01–08 under `ephemeral/reviews/`.

## Findings

### 1. issue — the run-log allocator moves without its resume and failure contract

Lines 15–16 move run-log allocation out of the backend into the engine role and
into `internal/runlog`. §12.4 attaches two non-happy-path obligations to
whoever owns that allocator, and the design carries neither across:

- **Resume recovery.** "The counter is recovered at startup and resume from the
  highest sequence already present in `events/`" (spec 2443–2445). Today that
  recovery is a constructor step of the component being split:
  `harness/backend.go:78-87` calls `recoverEventSequence` (`:338`) and seeds
  `nextSeq`. If the allocator moves and the recovery stays behind — the ordinary
  outcome of an extraction that nobody wrote down — a resumed run starts its
  counter at zero and the first turn's `os.OpenFile(..., O_CREATE|O_EXCL)`
  (`harness/backend.go:293`) collides with the existing `000001-*.jsonl`. The
  design records the exactly analogous obligation for the supervision side at
  line 12 ("recover batch numbering by scanning its directory"), which makes the
  omission on the run-log side conspicuous rather than implied.
- **Walk-side failure categorization.** §12.4's failure policy is split by
  caller: "a walk attempt returns a categorized Error through the ordinary retry
  path (Section 3.5), a patrol flush simply does not run that tick". Line 13
  records the patrol half verbatim and stops there. The walk half is not merely
  unrecorded, it is contrary to where the code will put it: after the move,
  allocation failures land in `executeWithRetry`, whose bookkeeping failures at
  `engine/runner.go:406-409,413-418,430-432,437-445,447-449,452-459` are
  returned as raw errors past the retry loop and fail the run, and whose current
  backend analogue returns `terminalError` (`harness/backend.go:292,302`) rather
  than a retryable one.

Impact: a transient `events/` write failure fails a whole run instead of costing
one retry, and a resumed run can die on its first turn — both in the component
this change is creating, and both invisible to the claim set, whose resume claim
(line 41) is scoped to supervisor state rather than the segment counter.

### 2. nitpick — the allocator's concurrency property is the one not covered by the `-race` line

Line 47 requires concurrency checks for "loss-free append/rotation and
checkpoint-save serialization" — the two supervision-side invariants. §12.4
states a third for the moving part: "Allocation is atomic across concurrent
branch turns" (spec 2443). That atomicity is currently provided incidentally by
`b.mu` inside `startTurnLog` (`harness/backend.go:284-287`), a lock that does
not travel with the code to `internal/runlog`. Since branch fan-out is exactly
the case where concurrent allocation happens, naming it alongside the other two
would cost a clause and would put the new package's only concurrency invariant
under the same required check.

## Outcome

material findings remain
