# Adversarial review: agy artifact prevention round 2

## Review target

Uncommitted prevention-layer rework on HEAD `ae5edb6` in the Tractor agy harness.

## Evidence inspected

- User acceptance criteria and repository instructions
- `WORK_SUMMARY.md`, full tracked diff, all untracked implementation/test/worklog files, and surrounding adapter/schema code
- Commit `ae5edb6` repair-retry implementation
- Both named read-only research reports, compared directly with the durable worklog
- Raw pipeline events for baseline, selected-agent, forced-writer, and hook smoke invocations
- Independent `go test ./...`, `go vet ./...`, `gofmt -l .`, and `git diff --check`

## Findings

1. **Critical:** the required custom-agent manifest, `--agent` argument, `init.tools` parsing, native-writer rejection, and missing-`run_command` rejection are absent. The empirical evidence that agy 1.1.15 ignores the allowlist is credible, but it demonstrates a blocker to the accepted design rather than completion by a different mechanism.
2. **Critical:** the replacement hook globally denies every native target outside the brain directory, even though the worklog records multiple native workspace writes that completed successfully without the artifact bug. Persistent global installation therefore breaks valid unrelated agy behavior.
3. **Issue:** a hook denial is retried by resuming the same conversation, while live evidence says the denial error/status remains sticky across resumed successful or tool-free turns. No real adapter-entry-point recovery proof closes this gap.
4. **Issue:** global hook provisioning rewrites any existing `hooks.json` with mode `0644`, widening restrictive modes, and its advisory lock does not protect non-cooperating editors despite the claim and test setup.
5. **Issue:** `WORK_SUMMARY.md` omits the explicitly required baseline and selected-agent `init.tools` dumps; raw transcript inspection confirms the evidence exists and is internally consistent.

## Verification

- `go test ./...`: exit 0; all packages passed
- `go vet ./...`: exit 0; no output
- `gofmt -l .`: exit 0; no output
- `git diff --check`: exit 0; no output
- Existing repair-retry remains present and bounded to one retry.
- Optional live agy smoke was not rerun because raw empirical evidence was credible and sufficient.

## Outcome

material findings remain
