# Adversarial review: agy artifact prevention round 1

## Review target

Uncommitted prevention-layer changes on HEAD `ae5edb6` in the Tractor agy harness.

## Evidence inspected

- User acceptance criteria and repository instructions
- `WORK_SUMMARY.md`, full diff, untracked implementation/worklog files, and surrounding agy adapter/schema code
- Commit `ae5edb6` repair-retry implementation
- Both named read-only research reports, including the artifact-aware hook policy
- Recorded raw events for the implementation's bounded real agy invocations
- Independent `go test ./...`, `go vet ./...`, `gofmt -l .`, and `git diff --check`

## Findings

1. **Critical:** `native_write_hook.go:71-95` globally and unconditionally denies all native writers, including valid writes below the conversation's `artifactDirectoryPath`. Once Tractor provisions this persistent global policy it breaks core artifact creation in unrelated interactive agy sessions. The research explicitly provides an artifact-aware alternative. Parse the hook payload, allow contained artifact targets, deny outside targets, and prove both paths live.
2. **Issue:** `adapter.go:415-435` does not pass `--agent`, the schema does not parse `init.tools`, and no writer-present or `run_command`-missing invariant exists. The raw smoke evidence credibly disproves allowlist enforcement in agy 1.1.15, but the pivot leaves the requested contract incomplete and provides no version-aware runtime validation of the replacement hook.
3. **Issue:** `native_write_hook.go:120-156` rewrites the shared global `hooks.json` without interprocess locking or atomic replacement. Parallel processes or concurrent user edits can lose keys, and interruption can truncate the global file.
4. **Issue:** the durable worklog omits the research's `artifactDirectoryPath`-aware hook and incorrectly characterizes unconditional global blocking as beneficial to manual sessions.

## Verification

- `go test ./...`: exit 0
- `go vet ./...`: exit 0, no output
- `gofmt -l .`: exit 0, no output
- `git diff --check`: exit 0, no output
- Existing repair-retry remains intact and bounded.
- Allowlist disproof is supported by raw selected-agent init events and a forced excluded `write_to_file` call; no extra live smoke was necessary.

## Outcome

material findings remain
