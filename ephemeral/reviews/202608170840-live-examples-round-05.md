# Adversarial review — live examples (steering, fan-out/fan-in), round 05

## Review target

The entire current work on worktree `examples-live-validation` — everything
since `12749f1`, now five commits. New since round 04:

- `3d7b618` "Complete live proof provenance" — proof-only. It adds
  `final/parallel/repository-state.txt` (post-run `git status` / worktree list /
  HEAD / stash list of the disposable workspace repository),
  `final/parallel/live-observation-capture.txt` (poll condition, capture
  command, latest captured event time), two `REPORT.md` bullets documenting the
  `.git` move and the capture, a `verification.txt` line, round-04's artifact,
  and a worklog entry that explicitly declines round-04's timeout and
  checkpoint-key nitpicks.

No Go source, example, README, or configuration file changed in this commit;
`git diff 347e683..HEAD -- '*.go' 'examples/' 'README.md'` is empty. Working
tree clean at `3d7b618`.

Authorities unchanged: `docs/spec.md` (§3.9, §4.8–4.9, §5.6, §10, §11),
`lefthook.yml`, `skills/orchestrate-attractor-loops/SKILL.md`, and the
conversation requirements restated in round 01 (token-conscious examples
actually run, proving live steering of a Codex turn and observable parallel
fan-out/fan-in; the report must support an evaluation of the JSON workflow
language and ongoing-run observability). The manager node remains an
implementation exclusion; the gap it leaves is recorded at the end.

Operating constraints honored: read-only apart from this artifact. No caller
instruction attempted to narrow defects, files, or subject matter, and none was
applied.

## Evidence inspected

- `git diff 347e683..HEAD` in full; the new proof files read line by line;
  `REPORT.md` and `verification.txt` re-read whole.
- Implementation re-read beyond the branch diff, looking for defects five rounds
  of review may have missed: `engine/store.go` (checkpoint write-and-rename,
  `appendTimeline` stamping under `timelineMu`, `appendSteering` inside the
  `activeMu` critical section), `engine/state.go` (`engineState` is
  mutex-guarded, so concurrent branch walks share counters safely),
  `engine/runner.go`, `engine/parallel.go`, `engine/git_workspace.go`,
  `engine/control.go`, `harness/backend.go` (`Steer` live-turn cardinality,
  `startTurnLog`/`finishTurnLog` live set and `current.jsonl` pinning),
  `harness/contract.go`, `cmd/tractor/root.go`.
- Hygiene at HEAD: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`
  → `0 issues`, `goimports -l .` → clean, and — beyond what `lefthook.yml`
  requires — **`go test -race ./...` passes on every package**, including the
  parallel-branch and control-socket tests. No data race is detectable in the
  concurrency this work added.
- Independent checks at HEAD carried forward and re-confirmed: the binary
  rebuilt from this tree still hashes to `bcd629f5…8370`, matching
  `final/provenance.txt`, and no Go source has changed since `dbbf5cb`;
  zero-token control probes still give `409` (no live target, and during
  fan-out), `400` (empty/malformed parts), `405` (`GET /steer`), `404`
  (unknown path), with no `steering.jsonl` written for rejections.
- **Artifact timestamps.** Filesystem mtimes in `final/` are genuine and align
  exactly with the runs' recorded event times (local clock is UTC−6:
  `steering/logs/stages/000002-work/steering.jsonl` mtime `08:25:42` matches its
  audit record `14:25:42.49Z`; `parallel/exit-status.txt` mtime `08:27:03`
  matches `PipelineCompleted` `14:27:03.14Z`). That makes them usable evidence
  about when each proof file was produced — see Finding 1.

Round-04's material finding is addressed: the `.git` move is now disclosed in
`REPORT.md:25-30` and the post-run repository state (one registered worktree,
base HEAD `ed53c9c`, empty stash list, only `?? tractor-fan-in-proof/`) is
retained. The two nitpicks were declined in writing
(`ephemeral/worklog/202608170739-live-examples.md:10`); the declines are
reasonable — the fan-in timeout margin is a judgment call and the NUL-prefixed
checkpoint key predates this branch.

## Findings

### 1. The frozen proof's live-observation claim is false as written (issue)

`REPORT.md:19-22` states: "`final/parallel/live-observation.jsonl` was captured
while both branches were still running." The retained artifacts contradict it:

- `final/parallel/live-observation.jsonl` has mtime `2026-08-17T08:26:27`
  local = `14:26:27Z`;
- both branches had already finished by then —
  `final/parallel/logs/timeline.jsonl` records
  `ParallelBranchCompleted` for `left` at `14:26:12.789933Z` and for `right` at
  `14:26:12.875052Z`, i.e. ~14 seconds earlier;
- the new `live-observation-capture.txt:1` records the condition the author
  actually used — "exit-status.txt absent and both ParallelBranchStarted events
  present" — which is satisfied any time before process exit and does **not**
  imply the branches were still live. Its `captured_event_time` is the newest
  event *in the file*, not the capture time, so it cannot rescue the sentence.

The capture command
(`jq -c 'select(.type == "ParallelBranchStarted" or (.type == "StageStarted" …))'`)
filters out completions, so the file's contents cannot distinguish the two
readings — only the timestamps can, and they refute the stronger one. Impact:
this is the one claim in the frozen proof that speaks to concurrency
observability *during* concurrency, exactly the property round 03 asked to be
demonstrated live, and it is overstated in the document a later auditor will
read first. The weaker true statement is still worth having and is fully
supported: the snapshot was taken mid-run, during the `combine` fan-in turn
(`14:26:12`–`14:27:03`), and it shows attributed, timestamped branch stage
events being readable before the run finished. Correcting the sentence — or
re-capturing during a 4-second branch window and recording the wall-clock
capture time — closes it. (The underlying capability is not in doubt: my own
HEAD-side runs read attributed branch events from a live `timeline.jsonl`.)

### 2. `repository-state.txt` does not record what `REPORT.md` says it records (nitpick)

`REPORT.md:27-30` says the repository state was captured "Before that move, and
again from the untouched metadata afterward". The retained file holds a single,
unlabeled capture — bare section headers `status`, `worktrees`, `head`,
`stashes` — with no commands, no timestamps, and no way to tell which of the two
captures it is or that the two agreed. Its own mtime (`08:35:22`) puts it eight
minutes after the run, alongside the `.git` move, so on the artifact alone the
"before the move" capture is unevidenced. This is the same standard the author
correctly applied one file over in `live-observation-capture.txt`, which does
record its command; applying it here (both captures, each with its command and
time) would make the round-04 remedy self-supporting.

### 3. The product has no operator affordance for the capability it proves (nitpick)

`cmd/tractor/root.go` registers `validate`, `run`, and `print-schema` only.
There is no `tractor steer` (or observe/attach) subcommand, and no script in the
repository: `git ls-files '*.sh'` is empty. So the steering example — the
deliverable's headline capability — is a two-actor procedure that a reader must
drive by hand from `examples/steering/README.md:12-22`: poll for
`steering-ready.txt`, parse `control_socket` out of `manifest.json`, and land a
`curl --unix-socket` POST inside the live turn (the marker's foreground command
sleeps 15s; the retained run's steer arrived at `14:25:42.49Z`, about one second
before that command ended). Nothing in `examples/` can be reproduced by a single
command, and a coding-agent operator — the audience §3.9 and §6 name explicitly
— must hand-roll socket plumbing that the CLI already has all the information to
perform. The spec requires no CLI, so this is ergonomics, not non-conformance;
but it is the one gap that keeps the proven capability from being usable by
anyone who did not write it.

## Evaluation notes requested by the caller

**JSON workflow language.** Unchanged this round (no example or schema edits).
Stable assessment across five rounds: `defaults` collapses
provider/model/effort/timeout repetition and single-successor nodes need no
`condition`, while `examples/examples_test.go` parse-and-lint gates every
committed example. The costs the examples make concrete remain (a) the mandatory
`parallel.fan_in` LLM join (spec §2.4, §4.9), which forces a real Codex turn to
consolidate two zero-token tool branches, (b) single-line shell in `tool` nodes
with no comments — `verify` encodes nine assertions in ~700 characters — and
(c) file-level-only `defaults`, so "90s for tools, longer for the model turn"
must be written per node; that is why the fan-in still runs at 47–54s against a
90s ceiling. The author declined tuning it; the risk is a flaky flagship example
on a slow day, not a correctness defect.

**Ongoing-run observability.** No code changed, so the round-04 assessment
stands and was re-verified: RFC 3339 nano `ts` on every timeline event (stamped
under the same mutex that serializes the append, so file order and timestamp
order agree); `branch` + `workdir` on branch stage events, emitted before
fan-in; absolute main `workdir` plus `control_socket` in `manifest.json`;
`worktrees.jsonl`, `events/index.jsonl`, `current.jsonl` (pinned to the index
whenever more than one turn is live) and relative `stages/latest/*` symlinks all
maintained live; `steering.jsonl` written only for accepted requests. Gaps, all
spec-permitted: no SSE projections, so remote supervision still needs filesystem
access; no harness/model/session on stage events (the binding exists only in
`checkpoint.json`, under the NUL-prefixed key the author declined to change);
rejected steering leaves no optional `timeline.jsonl` note; and no read-side CLI
(Finding 3), so "observable" currently means "greppable by whoever has the run
directory".

## Residual gap (from the excluded manager node)

The in-graph manager node remains omitted by user decision and excluded from
this task; not re-litigated. Recorded consequence, unchanged: the operator half
of a manager loop is proven and has been reproduced independently in five rounds
(invoke → observe durable surfaces → `POST /steer` → steering received and acted
on), and the rejection semantics a manager must handle are confirmed at HEAD.
What a manager driving a child pipeline would still lack is remote observation
(no SSE), turn-identity fields on stage events, and any first-class client for
the control socket (Finding 3); all three become blocking if a typed manager
node is restored later.

## Outcome

`material findings remain`
