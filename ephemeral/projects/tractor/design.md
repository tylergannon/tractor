# Tractor implementation design

## Scope

Implement only the harness slice of the Tractor spec:

- Codex and Claude Code adapters;
- the harness-backed CodergenBackend;
- a caller-aware `agent` CLI that runs through that backend; and
- shared live conformance proof for both harnesses.

Do not build the graph engine, compatibility layers, credential plumbing, model discovery, or speculative recovery.

## Choices

- Codex uses app-server. Keep its process through the first turn because thread creation alone is not resumable; resume later turns from native history.
- Generate only the Codex request type the selected generator handles and the implementation consumes. Keep the small observed response surface handwritten.
- Claude uses the pinned Roasbeef Go SDK. Mint a local session ID, use it for the first turn, and switch to native resume as soon as Claude reports materialization.
- Both adapters use native structured output, validate the exact caller schema locally, normalize the spec events, and own per-session serialization and controls.
- The backend owns provider routing, logical bindings, fidelity, workdir consistency, live targeting, and run logs. It does not duplicate adapter serialization.
- The CLI copies only Unified LLM's caller detection: Claude caller first, then Codex caller. It selects the opposite harness with fixed defaults and executes only through the backend.
- Native CLI authentication remains external. Codex runs approval-free with full access to the supplied workdir; Claude uses bypass permissions.

## Sequence

1. Implement the neutral contract, exact schema validation, backend, and run logs with hermetic tests.
2. Characterize the few unresolved Codex lifecycle signals, implement the Codex adapter, and prove it live.
3. Characterize Claude materialization and steering, implement the Claude adapter, and prove it with the same live flow.
4. Run the full hooks, obtain one unconditional Fable implementation review, fix material findings, then have a fresh agent use the real CLI for non-trivial work through both harnesses.

## Done

Done means the real `agent` CLI, through the backend, visibly completes non-trivial workspace tasks with both Codex and Claude. Tests and linters are hygiene; live conformance, Fable's implementation review, and fresh independent use are the acceptance proof.
