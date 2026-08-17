# Independent final validation

Validated HEAD: `58759b7386e550e6090d7471e711d80dad4e417d`

Overall result: **PASS.** Both fresh caller-routing tasks passed. The original composed run directly proves the remaining integration claims, and the primary artifacts in `supplemental-logs-v2-58759b7` close the two earlier proof gaps for distinct-process resume and parallel steering rejection.

## One-time build

`go build -o .../independent-agent-validation/agent ./cmd/agent` ran once and exited 0. SHA-256: `6657299de6d423facff325cc84c6cb185541de1f076a6b9b886c722d354eca4f`.

## Fresh live tasks

### Codex caller routes to Claude/Fable: PASS

The invocation set nonempty `CODEX_THREAD_ID`, unset `CLAUDE_CODE_SESSION_ID`, supplied no provider/model/reasoning flags, and exited 0. Native session `35d8f416-0703-4e5e-b5dc-75d81a990d6c` is a Claude Code 2.1.233 `sdk-go` transcript whose assistant messages name `claude-fable-5`. The public event segment contains matched Claude-native `Read`, `Write`, `Bash`, and `StructuredOutput` calls. The agent read nonce-bearing dataset `cbea32c3edd5`, wrote `inventory_audit.json`, and independently verified it. A separate AWK recomputation and a semantic comparison to `independently-computed-expected.json` both matched: 10 rows, net variance -1, net impact -1327 cents, and 9 discrepancies.

### Claude caller routes to Codex/GPT: PASS

The invocation set nonempty `CLAUDE_CODE_SESSION_ID`, unset `CODEX_THREAD_ID`, supplied no provider/model/reasoning flags, and exited 0. Native session `01a00e66-1228-7411-9c0b-67d18a0cfc2f` is a Codex 0.147.0 rollout with originator `tractor`, model `gpt-5.6-sol`, and effort `medium`. The public event segment contains Codex-native `commandExecution` and `fileChange` calls. The agent read nonce-bearing dataset `ac9ba7baf1fe`, wrote `ledger_reconciliation.json`, and independently re-read it. A separate AWK recomputation and semantic comparison matched: 12 transactions, account differences +222 and -120 cents, net 102 cents, absolute 342 cents.

The Codex-backed agent briefly created local commit `b364acc` containing only its output and worklog because it followed the generic repository checkpoint instruction. The commit was removed with a non-hard mixed reset to the validated HEAD; the two files remain untracked evidence. No production file changed and no commit remains.

## Existing composed run `run-logs-58759b7`

- Both harnesses: **PASS.** Final checkpoint bindings contain main and branch sessions for both Claude and Codex. Native transcripts/rollouts confirm Claude Fable and Codex GPT with the recorded workdirs.
- Loop and compaction: **PASS.** Timeline stages 9-11 route review -> implement -> review -> done; checkpoint records `review: 2`. The same Claude main session contains two native `system/compact_boundary` entries immediately before the two review turns.
- Parallel workdirs: **PASS.** `worktrees.jsonl` and `branches.json` record distinct branch-001 and branch-002 workdirs. Native Codex and Claude sessions, plus public tool events, use their corresponding recorded branch workdirs; both branches completed and finalization removed the worktrees.
- Accepted steering: **PASS.** `steering.jsonl` records the mid-turn instruction; the same public event segment contains it as a second user event followed by Claude's `Write` tool creating `steered-live.txt` with `STEER-58759B7`.
- Interruption: **PASS.** Timeline records `StageFailed` and `PipelineFailed` with `turn was interrupted`; the first Codex event segment ends during the 45-second command, no `interrupt-finished.txt` exists, and the subsequent segment writes `interrupt-resumed.txt` through the same native session.
- Final completion: **PASS.** Timeline ends `PipelineCompleted`; final checkpoint has `current_node: done`, empty `next_node`, stage sequence 11, two review visits, and two interrupt attempts.
- Fresh-process resume: **PASS with supplemental primary evidence.** `initial.pid` records PID 65285 and `resume.pid` records distinct PID 65399. Their `ps` captures name the same `/tmp/tractor-live-58759b7` binary, pipeline, workdir, and logs; only PID 65399's command includes `--resume`. Exit captures are 1 then 0. The supplemental timeline orders interrupted `StageFailed`, `CheckpointSaved`, and `PipelineFailed` before the second `PipelineStarted`; final checkpoint is `done`, has no successor, and records two attempts but one visit for `interruptible`. Workspace markers show the initial attempt ran before the resumed branch.
- Parallel steering rejection: **PASS with supplemental primary evidence.** `parallel-steer.status` contains 409 and its paired body is empty. Both were created at 00:37:13, after worktree creation at 00:37:02 and before completed `branches.json` at 00:37:32. The pipeline deliberately keeps both branches live for 30 seconds; timeline orders `ParallelStarted` and both branch starts before either branch completion. This places the captured 409 within live parallel execution rather than before or after it.
- Supplemental finalization: **PASS.** The resume exited 0, final checkpoint and timeline record completion, both branch outcomes exited 0, and the two recorded branch paths plus their containing generated worktree root are absent from disk and the Git worktree registry.

## Repository state

HEAD remains `58759b7386e550e6090d7471e711d80dad4e417d`. No commit was retained. Existing unrelated evidence was preserved. Concurrent tracked edits to `engine/fan_in.go`, `engine/fan_in_test.go`, and the orchestration worklog appeared after the one-time HEAD build and were not part of this validation or modified by it.
