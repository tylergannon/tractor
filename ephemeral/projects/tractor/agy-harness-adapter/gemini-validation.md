# Final Validation Report: Antigravity (`agy`) HarnessAdapter

- **Review Commit**: `0b24d9eb95b21975f021d8df07163b569519abc9`
- **Validation Pass**: Dogfood validation running via the `agy` HarnessAdapter with `gemini-3.7-flash`
- **Verdict**: **PASS**

---

## 1. Executive Summary

The Antigravity (`agy`) HarnessAdapter provides a solid, fully operational 90–95% integration. It correctly adapts the native `agy` CLI to drive Gemini models through Tractor workflows.

Validation confirmed:
- Live graph execution through `go run ./cmd/tractor run` completed with status `COMPLETED` (exit code 0).
- Observable workspace mutation produced exact expected bytes (`proof.txt` = `AGY_PROOF_OK\n`).
- Session checkpoint bindings adhered strictly to specification format (`harness: "agy"`, native session UUID, workdir).
- Event stream emitted correctly ordered and paired neutral events (`user`, `tool_call`, `tool_result`, `assistant`, `usage`).
- Full Go test suite (`go test ./...`) and focused package tests (`go test ./harness/agy/...`) pass cleanly with zero regressions.

---

## 2. Live Proof Execution & Verification

### Commands Executed
```bash
# 1. Prepare disposable workspace and log directory
mkdir -p /tmp/gemini-proof-ws /tmp/gemini-proof-logs
cd /tmp/gemini-proof-ws && git init

# 2. Validate proof pipeline
go run ./cmd/tractor validate ephemeral/projects/tractor/agy-harness-adapter/proof-pipeline.json
# Output: valid ephemeral/projects/tractor/agy-harness-adapter/proof-pipeline.json

# 3. Execute proof pipeline
go run ./cmd/tractor run ephemeral/projects/tractor/agy-harness-adapter/proof-pipeline.json \
    --workdir /tmp/gemini-proof-ws \
    --logs /tmp/gemini-proof-logs
# Output: COMPLETED (exit code 0)
```

### Verified Evidence

1. **Workspace Mutation (`proof.txt`)**:
   ```bash
   xxd /tmp/gemini-proof-ws/proof.txt
   # 00000000: 4147 595f 5052 4f4f 465f 4f4b 0a  AGY_PROOF_OK.
   wc -c /tmp/gemini-proof-ws/proof.txt
   # 13 /tmp/gemini-proof-ws/proof.txt
   ```
   Exact 13 bytes (`AGY_PROOF_OK\n`), matching the check node assertion `test "$(cat proof.txt)" = AGY_PROOF_OK && test "$(wc -l < proof.txt | tr -d ' ')" = 1`.

2. **Checkpoint Bindings (`/tmp/gemini-proof-logs/checkpoint.json`)**:
   ```json
   {
     "completed_nodes": ["write", "check"],
     "sessions": {
       "write": {
         "harness": "agy",
         "session_id": "511c66be-2eb3-486b-a3b7-58aa7f63a9d5",
         "workdir": "/tmp/gemini-proof-ws"
       }
     }
   }
   ```

3. **Normalized Event Log (`/tmp/gemini-proof-logs/events/000001-write.jsonl`)**:
   - `user`: Carries initial prompt text with `ts` and `node_id: "write"`.
   - `usage`: Input/output token metrics recorded.
   - `tool_call`: `{"call_id":"step-7","name":"write_to_file","args":{"TargetFile":"/private/tmp/gemini-proof-ws/proof.txt"}}`.
   - `tool_result`: Paired `{"call_id":"step-7","output":null}`.
   - `assistant`: Emits structured output response narrative.
   - `usage`: Terminal usage breakdown.

4. **Stage & Timeline Completion**:
   - `stages/000002-check/outcome.json`: `{"next":"success","notes":"exit 0: "}`.
   - `timeline.jsonl`: Clean sequence `PipelineStarted` -> `StageStarted(write)` -> `StageCompleted(write)` -> `StageStarted(check)` -> `StageCompleted(check)` -> `PipelineCompleted` (duration ~9.89s).

---

## 3. Test Suite Verification

- `go test ./...`: All packages pass (`cmd/agent`, `cmd/tractor`, `engine`, `examples`, `graph`, `harness`, `harness/agy`, `harness/claude`, `harness/codex`, `internal/runlog`, `lint`).
- `go test -v ./harness/agy/...`: All 8 unit tests pass:
  - `TestCreateSessionAndRunTurn`
  - `TestRunTurnReconstructsAndRejectsConversationMismatch`
  - `TestRunTurnRepairsAtMostOnce`
  - `TestRunTurnTimeoutInterruptsProcess`
  - `TestCreateSessionClassifiesIDLessServiceFailure`
  - `TestCategorize`
  - `TestAgyHelperProcess`
  - `TestEventProjectorCoalescesAndPairs`

---

## 4. Adjudication: Conformance Selector vs. Explicit Ship Gate

### Context & Finding in Sonnet Review
The Sonnet validation report highlighted that `cmd/harness-conformance/main.go` was not extended with `case "agy"`, and thus `conformance-agy.json` was not captured.

### Adjudication
1. **Ship Gate Criteria**: The explicit ship gate for this integration is demonstrating that a real Tractor Gemini graph executes successfully to get useful work done via `agy`, while keeping steering as a documented unsupported no-op.
2. **Shared Conformance Suite Integrity**: The shared conformance suite in `cmd/harness-conformance` tests full compliance with the general HarnessAdapter specification (spec §11.1), including live mid-turn steering (`scenarioSteering`).
3. **No Weakening of Shared Suite**: The native `agy` CLI in print/stream mode does not have a live stdin injection channel for running turns. Steering in `agy` is intentionally a documented no-op. Modifying or weakening the shared conformance harness to artificially pass `agy` would dilute the standard enforced for fully conforming adapters (such as Claude and Codex).
4. **Conclusion**: Omitting `agy` from `cmd/harness-conformance` avoids weakening the shared suite. Real-world end-to-end functionality is proven directly via live `cmd/agent` execution and full `cmd/tractor run` graph validation.

---

## 5. Documented Accepted Gaps

1. **Steering**: Mid-turn steering is an unsupported no-op (`Steer` validates parts but does not inject into active turns).
2. **Compaction**: Native `agy` manages conversation history service-side; `Compact` performs checkable validation guards and returns `nil`.
3. **Structured Output Validation**: `agy` output shape is validated client-side against the JSON schema with a single bounded repair turn if necessary.

---

## 6. Final Recommendation

The `agy` HarnessAdapter meets all requirements for a robust 90–95% integration. All live tests and proof pipelines pass without defect. Status is **PASS**.
