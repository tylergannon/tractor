# Live proof: spec migration and supervisors

All evidence was produced by binaries built from this worktree on 2026-08-17
(America/Costa_Rica). Provider versions were Codex CLI 0.147.0 and Claude Code
2.1.233.

The live pipeline command was:

```sh
tractor run PIPELINE.json --workdir "$WORKSPACE" --logs "$LOGS"
```

The resume scenario was interrupted with Ctrl-C after `first-started.txt`,
`briefed.json`, and `inbox.000001.jsonl` existed, then continued with the same
arguments plus `--resume`. Exact pipeline inputs are preserved beside each
scenario. The shipped live-steering input is
`examples/supervisor/live-steering.json`.

## Native backend conformance

`../conformance/codex-0.147.0.json` and
`../conformance/claude-2.1.233.json` each record eight passing live scenarios.
The added `backend_supervisor` scenario forces exact `ok` and `steer` verdicts
through one reused supervisor session and verifies both engine-allocated run-log
segments.

## Live steering, batching, and overlap

`examples/supervisor/live-steering.json` completed with a Claude supervisor and
Codex worker. `live-steering/supervisor-received.txt` is the exact steered
workspace effect. The timeline records a delivered steer and a flush with
`count: 1`; `inbox.000001.jsonl` is the completed `prepare` digest. During the
overlapping worker and supervisor turns, `current.jsonl` was observed pointing
to `events/index.jsonl` (`live-steering/current-overlap.txt`). The supervisor
binding checkpoint event names `coach`. The `events/` directory preserves
every coach segment and the segment index, including the one briefing-bearing
nudge.

## Resume

The initial run was interrupted after the supervisor binding, briefing, and
first numbered batch existed. The resumed run reused the exact saved Claude
binding, retried `work`, created `resume/resumed-success.txt`, and completed.
The backlog error digest moved into `inbox.000002.jsonl`; batch numbering stayed
monotonic. Across all supervisor segments, the briefing occurred once. The
final checkpoint records two work attempts and the original supervisor session;
all coach segments and their index are preserved under `resume/events/`.

## Multi-level supervision

A live tool stage kept the scope active while Claude supervisor `lead` observed
it and Codex supervisor `director` observed `lead`. The timeline records four
delivered director-to-lead steer verdicts. `director-inbox.000001.jsonl` is an
upward `lead` verdict; `lead-inbox.000001.jsonl` is the resulting downward
coaching digest. The 30-second pipeline completed.

## Operator stop

The stop scenario interrupted the CLI while the supervisor had an active native
turn. The foreground command returned five seconds after Ctrl-C. The timeline
records pipeline failure from the operator stop and the supervisor error
verdict; `stop/errors.jsonl` preserves `turn was interrupted`.

## Standalone agent wrapper

The standalone binary used Codex to reverse a three-line input and Claude to
sum a numeric input. The exact workspace results are preserved in `agent/`;
both were byte-compared against independently constructed expected files.
