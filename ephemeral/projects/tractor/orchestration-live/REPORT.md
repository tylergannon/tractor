# Composed live Tractor proof

Result: PASS at `58759b7386e550e6090d7471e711d80dad4e417d` with Codex CLI 0.147.0 and Claude Code 2.1.233.

The built `tractor` binary validated and ran `composed-pipeline.json` against the disposable Git repository in `workspace/`.

The first process ran a real Claude/Fable stage. While its turn was live, `POST /steer` over the manifest's Unix socket returned 200 with an empty body. Claude then wrote the exact steered value `STEER-58759B7` to `workspace/steered-live.txt`; the accepted request is recorded in the stage's `steering.jsonl`.

The next real Codex turn wrote `interrupt-started.txt` and entered a 45-second foreground task. SIGINT stopped the process. Tractor exited 1 with an interrupted turn, removed the socket, and checkpointed `current_node=interrupt_stage`, `next_node=interrupt_stage`, and `retry_visit=true`, including both native session bindings.

A fresh `tractor --resume` process restored those bindings. Codex reused session `01a00e5b-d970-7e32-ba8a-eeadd252ace5`, observed the persisted marker, and wrote `interrupt-resumed.txt`. The fan-out then opened isolated worktree-bound sessions for Codex and Claude and ran them concurrently. While fan-out was the top-level stage, a second `POST /steer` returned 409 with an empty body before backend delivery. `branches.json` records both workdirs, paths, stage directories, run-log segments, and successful outcomes; finalization removed the worktrees.

The real Codex fan-in wrote `workspace/synthesis.md`. Claude session `97050e76-52fd-4ab4-a708-bfafa0d8cc7a` reviewed and chose `implement`; the reused Codex session wrote `workspace/implementation.txt`; compacted Claude review then chose `done`. The fresh process exited 0 with `COMPLETED`.

The final checkpoint names `done`, has no continuation, records one visit and two attempts for the interrupted node, two visits for `review`, and four native bindings: main and branch sessions for both harnesses. The combined timeline contains both process starts, one pipeline failure, one pipeline completion, eleven stage starts, ten stage completions, the interrupted stage failure, all four parallel lifecycle events, and eleven checkpoint events.

Evidence is preserved in `run-logs-58759b7/`, including the manifest, full timeline, checkpoints, stage artifacts, event segments/index, steering audit, branch evidence, worktree inventory, and the two empty control-response bodies.

## Supplemental primary evidence

An independent validator accepted every composed-run claim except two that the original capture stated only narratively: distinct OS processes for resume, and the HTTP status of the parallel steering rejection. `supplemental-proof-pipeline.json` and `supplemental-logs-v2-58759b7/` close those evidence gaps through another real Tractor run.

The initial invocation is preserved as PID 65285 with its full command line and exit status 1. Its checkpoint records an interrupted tool node with `retry_visit=true`. The resume invocation is preserved as distinct PID 65399 with `--resume` in its full command line and exit status 0. Its final checkpoint names `done`, records two attempts for the interrupted node, and the timeline records failure, a second pipeline start, parallel execution, and completion.

While `ParallelStarted` was present and both 30-second branch tools were still active, the control request's primary artifacts recorded status 409 and a zero-byte body. The pipeline then completed and finalization removed its branch worktrees.
