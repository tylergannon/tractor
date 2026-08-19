# Adversarial review: agy artifact prevention round 3

## Review target

Uncommitted prevention-layer changes on HEAD `ae5edb6` in the Tractor agy harness.

## Evidence inspected

- Current authoritative validation criteria and repository instructions
- `WORK_SUMMARY.md`, full tracked diff, all untracked implementation/test/worklog files, and surrounding agy adapter/protocol code
- Commit `ae5edb6` and its original repair-retry behavior
- Both named read-only research reports
- Durable worklog and raw pipeline evidence for the real agy 1.1.15 experiments
- Independent `go test ./...`, `go vet ./...`, `gofmt -l .`, and `git diff --check`

## Findings

1. **Critical:** The required Tractor-owned custom-agent manifest, `--agent` argument, `init.tools` parsing, native-writer rejection, and missing-`run_command` rejection are all absent. The current diff explicitly substitutes a different design based on an earlier pipeline ruling, while the current acceptance criteria explicitly require this contract.
2. **Critical:** The substitute prevention layer permanently installs a global `PreToolUse` hook for every agy session on the user account. It is not scoped to Tractor invocation the way selecting a Tractor-owned agent would be, and it changes unrelated interactive sessions.
3. **Issue:** The committed ae5edb6 fallback is not intact: the diff replaces same-conversation shell-only repair with a fresh conversation, session-ID remapping, native-writer retry instructions, and a prompt preamble on every turn.

The required checks pass. The empirical tool dumps are credible and show that the attempted agy 1.1.15 manifest did not satisfy the invariant; that is a blocker to resolve or report, not proof that the substitute hook satisfies the current task. The worklog contains the requested research topics but adopts the superseded design conclusion.

## Outcome

material findings remain
