# Adversarial review — live examples (steering, fan-out/fan-in), round 06

## Review target

The entire current live-examples work on worktree `examples-live-validation` —
everything since `12749f1` (`1944160`, `1e9a0c9`, `b365554`, `dbbf5cb`,
`347e683`, `3d7b618`) plus the uncommitted working-tree correction to
`ephemeral/projects/tractor/live-examples/REPORT.md:19-23` and the round-05
worklog entry. This review is not narrowed to that delta: implementation,
examples, documentation, and the whole retained proof tree were re-examined, and
both examples were run live again at HEAD.

Authorities: `docs/spec.md` (§3.6 Finalize, §3.9, §4.8–4.9, §5.6, §10, §11),
`lefthook.yml`, `skills/orchestrate-attractor-loops/SKILL.md`, and the
conversation requirements restated in round 01 — token-conscious examples that
are actually run, proving (a) an operator can invoke a workflow, steer its live
turn, and have the steering received, and (b) observable parallel
fan-out/fan-in; the report must support an evaluation of the JSON workflow
language and ongoing-run observability.

Caller dispositions recorded, not re-litigated: round-05 finding 1 accepted and
corrected; findings 2 (duplicate repository-state capture detail) and 3
(steer/observe CLI) declined as nitpicks that would add unrequested proof
machinery or CLI surface — both were classified as nitpicks by this review and
the declines are within the caller's prerogative; the earlier declines (fan-in
timeout margin, NUL-prefixed checkpoint key) stand. Manager-node behavior
remains excluded by user decision; the residual gap is recorded at the end.
No caller instruction attempted to narrow which defects, files, or subject
matter this review may consider, and the review was performed broadly from the
authoritative sources. Read-only except for this artifact.

## Evidence inspected and independently verified

**The corrected claim.** `REPORT.md:19-23` now reads: captured "while the run
was still live in its fan-in turn, after both branches had completed". Verified
against the retained artifacts, which are timestamp-faithful (local clock UTC−6;
`steering/logs/stages/000002-work/steering.jsonl` mtime `08:25:42` = its audit
record `14:25:42.49Z`; `parallel/exit-status.txt` mtime `08:27:03` =
`PipelineCompleted 14:27:03.14Z`):

- `final/parallel/live-observation.jsonl` mtime `08:26:27` = `14:26:27Z`;
- both `ParallelBranchCompleted` at `14:26:12.78Z` / `14:26:12.87Z` — so the
  capture is after branch completion, as the corrected sentence says;
- the `combine` stage ran `14:26:12.87Z → 14:27:03.08Z` and the pipeline
  completed at `14:27:03.14Z` — so the capture is inside the live fan-in turn
  and before run completion, as the corrected sentence says.

The claim is now accurate in both directions and consistent with
`live-observation-capture.txt`'s recorded condition.

**Live re-verification at HEAD** (binary rebuilt from this tree; sha256
`bcd629f5…8370`, identical to `final/provenance.txt`, and no Go source has
changed since `dbbf5cb`):

- Steering example, `examples/steering/README.md` followed verbatim: run in
  `/tmp/r6/steer`, marker appeared after 9s of polling, `POST /steer` via the
  `control_socket` read from `manifest.json` → **`200` with a zero-byte body**,
  workspace gained `steering-received.txt` = `TRACTOR_STEERING_RECEIVED`,
  `stages/latest/work/steering.jsonl` recorded the instruction at
  `14:42:15.43Z`, deterministic `verify` passed, process exited `0` printing
  `COMPLETED`.
- Parallel example, `examples/parallel/README.md` followed verbatim
  (`git init` + empty base commit): run in `/tmp/r6/par`, exit `0`,
  `COMPLETED`, verify log `parallel fan-in verified`,
  `tractor-fan-in-proof/summary.txt` = `isolation=verified` /
  `overlap=verified`, main workspace holds only that directory, and afterwards
  `git worktree list` shows one worktree, `git stash list` is empty, and the
  engine-owned `worktrees-*` directory is gone from `{logs_root}`.
- **Zero-token mid-branch observation** (the stronger property, established
  without spending a fan-in turn): a second parallel run was snapshotted 2.0s
  into the 4s branch sleeps and then interrupted before `combine`. At
  `14:43:54.93Z`, `timeline.jsonl` already carried
  `{"branch":"left",…,"type":"StageStarted","workdir":".../branch-001"}` and the
  matching `right`/`branch-002` line (both branches still running), while the
  main workspace contained no branch files. Branch-attributed live observability
  during concurrency is therefore independently confirmed at HEAD.
- Control-surface conformance (zero tokens): `409` with no audit record when no
  turn is live and while a fan-out is the active top-level execution (rejected
  by the engine before backend handoff, §3.9), `400` for `[]` and for a
  non-array body, `405` for `GET /steer`, `404` for unknown paths; empty bodies
  throughout.
- Interrupted-run worktree retention observed in the interrupted run above
  (branch worktrees still registered afterwards) is **spec-required**, not a
  defect: §3.6 line 414 and the §11 checklist ("A failed run's worktrees survive
  on disk; a completed run's are cleaned up at Finalize") mandate exactly that.

**Code and hygiene.** Re-read `engine/store.go` (atomic checkpoint
write-and-rename; `appendTimeline` clones the caller map, stamps only if absent,
and stamps under the same mutex that serializes the append, so timestamp order
matches file order; `appendSteering` runs inside the `activeMu` critical
section), `engine/state.go` (mutex-guarded counters shared by concurrent branch
walks), `engine/runner.go`, `engine/parallel.go`, `engine/git_workspace.go`
(freeze via temp index + `commit-tree`, no mutation of the workspace's refs;
`workdirRel` mapping; cleanup sweep of `worktrees.jsonl`), `engine/control.go`,
`harness/backend.go` (`Steer` cardinality, live-turn set, `current.jsonl`
pinning). `go build ./...`, `go vet ./...`, `golangci-lint run ./...` →
`0 issues`, `goimports -l .` clean, and `go test -race ./...` passes on every
package — stricter than `lefthook.yml` requires, and clean.

**Conclusion on material functionality:** both headline capabilities hold at
HEAD, reproduced by this review from the committed documentation alone; the
frozen proof's provenance is independently reproducible; no material defect
survives. I have no critical or issue-level finding this round.

## Findings (nitpicks only)

### 1. The worklog still asserts the claim that was just corrected (nitpick)

`ephemeral/worklog/202608170739-live-examples.md:9` still reads: "The parallel
evidence includes a live snapshot taken before either branch completed." That is
the exact sentence the round-05 correction removed from `REPORT.md:19`, and the
timestamps refute it (capture `14:26:27Z`; both branches completed
`14:26:12Z`). The following worklog line records the correction, so the file now
contradicts itself in adjacent entries. Worklog lines are an append-only
decision record, so rewriting history there is not obviously right — but a
reader tracing the proof will meet the refuted claim first. One clarifying
clause on the round-05 line ("supersedes the snapshot description above") would
settle it.

### 2. The corrected report sentence now understates a property that is true and cheap to show (nitpick)

`REPORT.md:19-23` now claims only that "branch activity could be inspected
before the run completed". The stronger property — attributed branch stage
events readable *while both branches are still executing* — is what the
observability requirement was really about, it holds at HEAD, and demonstrating
it costs nothing: start the run, snapshot `timeline.jsonl` ~2s into the 4s branch
sleeps, and interrupt before `combine` spends a token (my run above, snapshot
`14:43:54.93Z` against branch starts at `14:43:52.92Z`). Recording that
zero-token capture beside the existing one would let the report state the
stronger claim with evidence instead of trading it away.

### 3. The parallel example does not mention what an interrupted run leaves behind (nitpick)

`examples/parallel/README.md` documents the disposable-repository precondition
and the success criteria but not the failure path. Per spec §3.6 the engine
deliberately retains branch worktrees when a run does not reach `exit`, so a
reader who Ctrl-C's the example is left with two detached worktrees registered
in their workspace repository, pointing under `{logs_root}` — and deleting the
logs directory afterwards leaves stale registrations that need
`git worktree prune`. One sentence ("a stopped or failed run keeps its branch
worktrees as evidence; `git worktree prune` after deleting logs") would keep the
example's first-run experience clean without touching the engine.

## Evaluation notes requested by the caller

**JSON workflow language.** No example or schema changed since round 04;
assessment stable across six rounds. Strengths: `defaults` collapses
provider/model/effort/timeout repetition, single-successor nodes need no
`condition`, and `examples/examples_test.go` parse-and-lint gates every
committed example. Costs the examples make concrete: the mandatory
`parallel.fan_in` LLM join (spec §2.4, §4.9) means a fully deterministic
fan-out is unexpressible and this example must spend a Codex turn to consolidate
two zero-token branches; JSON's lack of multi-line strings and comments
concentrates authoring risk in `tool` nodes (`verify` encodes nine assertions in
one ~700-character line); and `defaults` is file-level only, so per-node timeouts
must be repeated — which is why the fan-in still runs at 47–54s (50.5s in
today's run) against a 90s ceiling. The caller has declined tuning that margin;
it remains a flakiness risk rather than a correctness defect.

**Ongoing-run observability.** Verified live again this round. Every
`timeline.jsonl` event carries an RFC 3339 nano `ts`; branch stage events carry
`branch` + `workdir` on `StageStarted`/`Completed`/`Failed`/`Retrying` and are
readable while the branches are still running (demonstrated above);
`manifest.json` advertises the run id, name, goal, absolute main `workdir`, and
`control_socket` before the first stage; `worktrees.jsonl`,
`events/index.jsonl`, `current.jsonl` and the relative `stages/latest/*`
symlinks track the run as it proceeds; `steering.jsonl` is written only for
accepted requests, and rejections are the status code alone, as §3.9 permits.
Remaining gaps, all spec-permitted MAYs or declined by the caller: no SSE
projections, so remote supervision still requires filesystem access to the run
directory; no harness/model/session fields on stage events (the binding lives
only in `checkpoint.json`, under the NUL-prefixed key); no `timeline.jsonl` note
for rejected steering; and no first-class client for the control socket, so the
operator side stays a documented `curl` procedure.

## Residual gap (from the excluded manager node)

The in-graph manager node remains omitted by user decision and excluded from
this task. Recorded consequence, unchanged: the operator half of a manager loop
is proven and has now been reproduced independently in six rounds (invoke →
observe durable surfaces → `POST /steer` → steering received and acted on), and
the rejection semantics a manager must handle are confirmed at HEAD. What a
manager driving a child pipeline would still lack is remote observation (no
SSE), turn-identity fields on stage events, and any first-class control-socket
client; those become blocking only if a typed manager node is restored later.

## Outcome

`only nitpicks remain`
