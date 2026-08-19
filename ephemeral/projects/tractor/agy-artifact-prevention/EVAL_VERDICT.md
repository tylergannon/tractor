# Independent adversarial validation verdict

## Judgment

**REWORK REQUIRED — material findings remain.**

The current diff does not implement the prevention layer described by the acceptance criteria. It explicitly drops the custom-agent manifest, passes no `--agent`, and ignores `init.tools`. It instead installs a persistent global `PreToolUse` hook and materially rewrites the committed repair fallback.

## Evidence inspected

- `WORK_SUMMARY.md`, `git status --short --branch`, the complete tracked diff, all untracked implementation/test files, and surrounding agy invocation/protocol code
- Commit `ae5edb6`, including its original repair implementation and regression tests
- Both read-only research reports under `/Users/tyler/src/tractor/ephemeral/`
- `ephemeral/worklog/202608191900-agy-artifact-prevention.md`
- Raw pipeline transcript under `.tractor/runs/0d96c49ed7c4291e107ffc77249f42fe/`
- Required Go suite, vet, formatting, and diff-hygiene checks

The worklog entry exists and durably covers the requested research topics: failure mechanism, absence of an artifact-root configuration fix, documented allowlist mechanism, empirically observed tool identifiers, intended `init.tools` invariant, hook alternative, version/hang constraints, prevention-plus-repair architecture, and upstream bug recommendations. Its empirical addendum is internally consistent, but its conclusion that the manifest contract should be dropped conflicts with the current authoritative acceptance criteria.

## Empirical evidence judgment

The recorded evidence does not look fabricated. `WORK_SUMMARY.md:161-195` records a 57-tool baseline and an identical selected-agent list containing all three native writers plus `run_command`; the raw pipeline transcript records the manifest experiment and the same conclusion. This is credible evidence that the attempted agy 1.1.15 manifest did **not** satisfy the required invariant. It is therefore evidence of a blocker, not evidence that the current hook implementation satisfies the requested design.

I did not consume the optional additional real agy smoke run: the existing evidence is credible and already dispositive, while no smoke result can make the absent manifest, `--agent` argument, or `init.tools` enforcement present in this diff.

## Material findings

1. **Critical:** no manifest, `--agent`, parsed `init.tools`, forbidden-writer rejection, or missing-`run_command` rejection is implemented.
2. **Critical:** the replacement prevention mechanism is persistent global hook policy affecting unrelated agy sessions, rather than Tractor-scoped agent selection.
3. **Issue:** the ae5edb6 fallback was materially redesigned instead of retained beneath the new prevention layer.

Full actionable detail is in `EVAL_FEEDBACK.md`.

## Command results

### `git status --short --branch` before this evaluation's writes

```text
## claude/jovial-aryabhata-59c086
 M go.mod
 M harness/agy/adapter.go
 M harness/agy/adapter_test.go
?? .tractor/
?? EVAL_FEEDBACK.md
?? EVAL_VERDICT.md
?? WORK_SUMMARY.md
?? ephemeral/reviews/202608191639-agy-artifact-prevention-round-01.md
?? ephemeral/reviews/202608191705-agy-artifact-prevention-round-02.md
?? ephemeral/worklog/202608191900-agy-artifact-prevention.md
?? harness/agy/native_write_hook.go
?? harness/agy/native_write_hook_test.go
```

### `go test ./...`

Exit 0. All packages passed; `github.com/tylergannon/tractor/harness/agy` passed in 0.940s.

### `go vet ./...`

Exit 0; no output.

### `gofmt -l .`

Exit 0; no output.

### `git diff --check`

Exit 0; no output.

Passing checks do not exercise the missing acceptance contract; the fake-subprocess tests were rewritten around the substitute hook and fresh-conversation behavior.
