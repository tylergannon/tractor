# Tractor live examples proof

## Final frozen-implementation proof

`final/provenance.txt` pins commit
`dbbf5cba726aa16747e33f57380d7688cfcd696f`, the exact binary path and SHA-256,
and the Go toolchain. Both final directories retain the literal invocation,
PID, process exit status, stdout/stderr, complete run directory, and resulting
workspace. No example or runtime code changed after these executions.

- Steering run `3ac9df098b9b9a62895c0b671967409d` exited `0`, printed
  `COMPLETED`, accepted the recorded request with HTTP `200` and an empty body,
  projected the injected native `user` event, wrote the requested sentinel,
  passed its deterministic verify stage, and ended at the exit checkpoint.
- Parallel run `399dfc6a51c5b61effd966b1aceb95f5` exited `0`, printed
  `COMPLETED`, recorded distinct worktrees and overlapping intervals for both
  branches, consolidated both products, passed the no-leak and content checks,
  and removed both engine-owned worktrees at finalization.
- `final/parallel/live-observation.jsonl` was captured while the run was still
  live in its fan-in turn, after both branches had completed. It contains both
  branch-start events and both attributed stage starts with timestamps and
  distinct workdirs, proving branch activity could be inspected before the run
  completed.
- Both final manifests advertise the absolute main `workdir` as well as the
  control socket.
- The disposable parallel workspace's `.git` directory was moved to `/tmp`
  before committing artifacts so Git would not record an embedded repository.
  Before that move, and again from the untouched metadata afterward,
  `final/parallel/repository-state.txt` captured the single registered main
  worktree, base HEAD, empty stash list, and only the expected untracked fan-in
  proof directory.
- `final/parallel/live-observation-capture.txt` records the poll condition,
  capture command, and latest event timestamp for the live snapshot.

The earlier initial and independent-review runs below remain as corroborating
history.

The initial live runs are retained under `steering/` and `parallel/`. After the
example and observability repairs, the independent reviewer rebuilt Tractor at
`1e9a0c9` and re-ran both committed examples. Those current-commit primary
artifacts are retained under `head-steering/` and `head-parallel/`.

## External steering

The initial recorded invocation started run
`dd95b1ea43547825a84eec4cce0e7d1a`. The independent current-commit rerun is
`810b16f9be8d97f0caba33324b9ade15`. Each used one low-reasoning Codex turn.
After the live turn wrote `steering-ready.txt`, the request was posted to the
manifest's Unix socket.

- Tractor exit status: `0`; stdout: `COMPLETED`.
- HTTP status: `200`; response body: empty.
- `logs/stages/latest/work/steering.jsonl` records the accepted instruction.
- `logs/events/000001-work.jsonl` contains the additional native `user` event,
  the ensuing `fileChange`, and its successful result.
- The live session wrote `workspace/steering-received.txt` with exactly
  `TRACTOR_STEERING_RECEIVED`.
- The downstream `verify` tool passed, the terminal checkpoint names `exit`,
  and the timeline ends in `PipelineCompleted`.

The HTTP response and audit alone were not treated as receipt proof; the native
event plus unpredictable live workspace effect are the load-bearing evidence.

## Fan-out and fan-in

The initial recorded invocation started run
`88546c241d871dfa9063b5b4a1a39ef2`. The independent current-commit rerun is
`b6bd850707a383ab30c543df8f49978f`. Each used two zero-token tool branches and
one low-reasoning Codex fan-in turn.

- Tractor exit status: `0`; stdout: `COMPLETED`.
- `branches.json` records ordered `left` and `right` results with distinct
  workdirs and stage directories.
- The fan-in independently confirmed each worktree contained its own branch
  record and not the sibling's record.
- The retained branch records have identical intervals,
  `1786974899..1786974903`, proving overlap.
- The fan-in event segment shows the agent reading both named worktrees,
  checking isolation and overlap, copying both files into the main workspace,
  and writing `summary.txt` only after those checks passed.
- The deterministic `verify` tool independently checked both copied records,
  interval overlap, and the exact summary.
- Finalization removed both engine-owned branch worktrees; only the main proof
  workspace remained registered before its disposable Git metadata was moved
  out of the tracked evidence tree.
- In the current-commit rerun, every lifecycle event has `ts`, and live branch
  stage events carry `branch` plus the distinct branch `workdir` before fan-in.
- The final zero-token edit adds explicit no-leak assertions to `verify`; the
  exact current verify command was replayed over the retained current-commit
  workspace and exited `0` (`head-parallel/current-verify.*`).

## Lifecycle timestamps

The initial live runs exposed that `timeline.jsonl` had event order and
durations but no absolute timestamps. Tractor now stamps every lifecycle
event. `timestamp-smoke/logs/timeline.jsonl` isolates that change in a
zero-token CLI smoke run, while `head-parallel/logs/timeline.jsonl` proves the
timestamp and branch-attribution fields in the real parallel example.
