# Adversarial review: agy artifact-path recovery

## Review target

Uncommitted changes to `harness/agy/adapter.go` and `harness/agy/adapter_test.go` that recover from agy's terminal `is not a valid artifact path` result.

## Evidence inspected

- `WORK_SUMMARY.md`, `git status`, the complete implementation/test diff, and surrounding agy adapter and engine artifact-collection code.
- Shared run `41348176f74b7d15876da38eeb869ae5`: stage/fan-out errors, prompt, resolved branches, Gemini event stream, timeline, and the real 90-line `gemini_proposal.md`.
- Isolated run `75e199e9c70058bb097577d2538d7956`: stage/fan-out errors, prompt, resolved branches, Gemini event stream, timeline, and the real 33-line branch-worktree `gemini_critique.md`.
- Existing isolated/shared artifact-collection tests in `engine/parallel_test.go` and the collector in `engine/artifacts.go`.
- Required implementation-only stash regression, focused repaired tests, full Go suite, vet, formatting, and diff hygiene.

## Findings

No findings. The error signature and ordering match the recorded external agy failure in both workspace policies. The change performs one bounded resumed agy turn, does not turn an error into synthetic success, propagates a persistent failure, and leaves real-path artifact verification/copying to the existing engine collector. The diff is confined to the agy backend, and existing shared/isolated and Claude/Codex coverage remains green.

## Outcome

no findings
