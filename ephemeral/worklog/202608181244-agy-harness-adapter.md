# agy harness adapter worklog

correction: Antigravity means the installed Antigravity.app and its `agy` CLI; the hosted Interactions agent and SDK-bundled `localharness` are not this integration target.
decision: Tractor will integrate through Google's documented `agy --print --output-format stream-json` interface while retaining orchestration, result validation, logging, and routing authority.
decision: Completion requires live proof through `tractor run` with an actual Gemini model and observable workspace or run-directory effects; tests alone are not proof.
decision: Review should prioritize material failures preventing useful 90-95 percent completion and should not delay shipping for hypothetical edge races or speculative future compatibility.
friction: Initial research inferred protocol details from the installed binary before locating Google's headless documentation -> use the official headless protocol as authority and local runs only for conformance evidence.
correction: Resumed agy turns require both Cmd.Dir and an absolute --add-dir; --new-project alone roots creation but did not reliably root a resumed file write in an independent probe.
decision: Every resumed init and result conversation_id must equal the requested native session ID because agy accepts an unknown --conversation ID by silently minting another conversation.
decision: A small checked-in open JSON Schema plus go-jsonschema generated tolerant structs was practical for the observed stream; keep event/type fields open and perform exact application-result validation separately.
friction: cmd/agent detects CODEX_THREAD_ID and intentionally overrides explicit standalone provider flags -> native provider smoke runs launched inside Codex must unset CODEX_THREAD_ID and CLAUDE_CODE_SESSION_ID or they prove the delegated Claude path instead.
friction: agy can return an ID-less eligibility failure with INTERNAL code 500 and cannot-connect wording before session initialization -> categorize result status/error before enforcing successful-stream ID invariants, and treat that observed signature as retryable.
proof: Sonnet 5 independently ran both the companion agent and a full codergen-to-tool Tractor graph through agy 1.1.14 with gemini-3.7-flash-low; exact workspace bytes, neutral events, checkpoint binding, tool verification, and pipeline completion all passed.
decision: Do not weaken the universal HarnessAdapter conformance program to mark agy green when print mode cannot support live steering; the missing agy selector/evidence is a transparent follow-up, not a blocker to the user-defined live Tractor graph ship gate.
proof: The new adapter was dogfooded for final validation with gemini-3.7-flash-medium; Gemini independently reran the committed proof graph, verified COMPLETED, exact AGY_PROOF_OK newline bytes, an agy checkpoint binding, normalized event pairs, and all Go tests, then returned PASS with no material functional defect.
