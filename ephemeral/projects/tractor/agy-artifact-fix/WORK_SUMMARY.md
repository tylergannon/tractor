# agy artifact-path regression: root cause and fix

## Symptom

Both reproductions fail identically:

- shared: `.tractor/runs/41348176f74b7d15876da38eeb869ae5` (branch `gemini_proposal`)
- isolated: `.tractor/runs/75e199e9c70058bb097577d2538d7956` (branch `gemini_critique`)

`stages/*gemini*/error.json` in both:

```
declaring permissions: cortex tool write_to_file: convert tool call for permissions: model output error: invalid tool call error (invalid_args) <path>/gemini_*.md is not a valid artifact path; artifacts must be in /Users/tyler/.gemini/antigravity-cli/brain/<uuid>/
```

`<path>` is the plain workspace path in the shared run and the branch worktree path
in the isolated run — the failure is identical across both workspace policies,
because (see Root cause) it happens entirely inside the external `agy` process,
before Tractor's workspace-policy-aware artifact collection
(`engine/artifacts.go:collectArtifacts`) ever runs.

## Root cause

**This error text is not produced anywhere in this repo.** `grep -rn "declaring
permissions" --include="*.go" .` and `grep -rn "cortex" --include="*.go" .` both
return nothing. It is the external `agy` CLI's own error message, propagated
verbatim: `harness/agy/adapter.go`'s `runOnceWithActive` takes a non-`SUCCESS`
`result` envelope's `Error` field and passes it through `categorize()`, which
matches `"permission"` in its terminal-marker list and returns it unchanged as
`harness.ErrorTerminal`.

Walking the evidence for the shared run's `gemini_proposal` branch
(`events/000002-gemini_proposal.jsonl`, decoded):

1. The model reads `INTENT.md` and `materials/` (`view_file`/`list_dir` calls).
2. It calls its **native** `write_to_file` tool twice (`call_id: step-24`, then
   `step-26`) targeting `.../wisdom-circle/gemini_proposal.md` — the real
   workspace path, **not** a shell heredoc, despite the branch's prompt in
   `circle.json` explicitly asking for "a plain file ... created with a shell
   command (for example a heredoc)". The second call's `tool_result` shows
   `output: null` with **no** `error` field — from the model's point of view the
   write tool call succeeded.
3. `timeline.jsonl` shows `StageFailed` for `gemini_proposal` at
   `19:27:24.346211Z`, 12ms after the assistant's final text at
   `19:27:24.334133Z` — i.e. the failure happens in the turn's terminal
   `result` envelope, after the visible tool activity, not during it.
4. `worktrees-1138677807/branch-003/gemini_critique.md` in the isolated run
   (33 real lines of critique content) proves the file **was** actually
   written to disk by the native tool before the turn's terminal result
   reported failure.

So `agy`'s native `write_to_file` tool physically writes the requested file, but
its own end-of-turn bookkeeping ("declaring permissions" — converting the
write into an artifact-permission record inside `agy`'s internal `cortex`
layer) rejects any target outside its private per-conversation
`~/.gemini/antigravity-cli/brain/<uuid>/` directory, and that rejection fails
the **whole turn**, discarding the turn's structured output (Tractor's
required `notes` field) along with it. `--dangerously-skip-permissions`
(already passed in `adapter.go`) only auto-approves the interactive
tool-permission prompt; it does not affect this separate artifact-path
validation.

**This is not a code regression bisectable in this repo's git history.**
`harness/agy/*.go` has not changed since `5eb89fc`/`eefed09`, and
`engine/artifacts.go` (added in `2485e4c`, "Add heterogeneous fan-out
workspaces") never injects `branch.Artifacts` into any prompt — it is a
purely post-hoc, filesystem-only collector (`os.Lstat` + copy), skipped
entirely once `result.Error != nil`. The PR #18-era proof pipeline
(`ephemeral/projects/tractor/heterogeneous-fanout-proof/heterogeneous.json`,
captured at `2485e4c`) only exercises `openai` and `anthropic` branches — it
never ran a `gemini`/`agy` branch with a declared artifact, so this failure
mode was never actually exercised end-to-end before `wisdom-circle`. The
gemini branch's own manually-authored prompt in `circle.json` ("write ... with
a shell command") shows this was already known to be a risk and worked around
by hand — but prose instructions alone are not reliable: the model used its
native tool anyway.

## Fix

`harness/agy/adapter.go`: `RunTurn` now recognizes this specific `agy`
failure signature (`isArtifactPathError`, matching the stable substring `"is
not a valid artifact path"` that `agy` uses in both reproductions) and spends
its **one bounded repair attempt** — the same mechanism `RunTurn` already uses
for schema-invalid structured output — retrying the turn once with an explicit
corrective instruction (`artifactWriteRepairPrompt`) telling the model its
file-write tool is restricted to a private directory and it must redo the
work using its shell/terminal tool instead. If the repaired turn still fails
(model doesn't comply), the terminal error propagates exactly as before — no
infinite loop, no silent success.

This is correct for **both** workspace policies because it operates entirely
inside the `agy` adapter's turn-execution loop, before `engine/artifacts.go`
ever sees the branch result — workdir (shared workspace path vs. isolated
worktree path) never enters into the decision. And it does not paper over the
error: success is only ever returned via a genuine second `agy` turn that
passes structured-output validation (`validateStructured`) and schema
comparison (`compareEchoedSchema`) exactly like every other turn; the branch's
declared artifact is still collected afterward by
`engine/artifacts.go:collectBranchArtifact`, which independently `os.Lstat`s
the real file on disk. Nothing is faked or assumed.

Diff: `harness/agy/adapter.go` (`RunTurn`, plus `isArtifactPathError` /
`artifactWriteRepairPrompt` helpers near `categorize`/`terminal`).

## Regression test

`harness/agy/adapter_test.go`, in the existing table-driven / fake-subprocess
style (`testAdapter`/`TestAgyHelperProcess`, same pattern as
`TestRunTurnRepairsAtMostOnce`):

- `TestIsArtifactPathError` — table-driven unit test on the detector against
  the exact real `agy` error text from both run reproductions.
- `TestRunTurnRecoversFromArtifactPathError` — fake `agy` returns the real
  error on the first invocation, succeeds on the second once the prompt
  contains the repair guidance; asserts `RunTurn` returns the valid result,
  emits exactly one repair `user` event containing both the corrective
  guidance and the original `agy` error text, and makes exactly 2 process
  invocations.
- `TestRunTurnArtifactPathErrorRepairsAtMostOnce` — fake `agy` returns the
  error on *every* invocation; asserts `RunTurn` still terminates with the
  original terminal error after exactly 2 invocations (bounded retry, no
  infinite loop).

### Fails without the fix

Reproduced by `git stash push -- harness/agy/adapter.go` (keeping the new
tests) and running the two `RunTurn`-level regression tests:

```
=== RUN   TestRunTurnRecoversFromArtifactPathError
    adapter_test.go:116: artifact-path recovery result=harness.Result(nil) err=terminal: declaring permissions: cortex tool write_to_file: convert tool call for permissions: model output error: invalid tool call error (invalid_args) /workdir/gemini_proposal.md is not a valid artifact path; artifacts must be in /Users/tyler/.gemini/antigravity-cli/brain/56536675-0470-4bfc-b8ee-a04946debce9/
--- FAIL: TestRunTurnRecoversFromArtifactPathError (0.01s)
=== RUN   TestRunTurnArtifactPathErrorRepairsAtMostOnce
    adapter_test.go:147: invocations = 1, want exactly 2 (initial plus one bounded repair, no further retries)
--- FAIL: TestRunTurnArtifactPathErrorRepairsAtMostOnce (0.01s)
FAIL
FAIL	github.com/tylergannon/tractor/harness/agy	0.307s
```

(`TestIsArtifactPathError` was temporarily excluded from that run since it
calls the new helper directly and would fail to *compile* pre-fix — a
stronger, but less illustrative, failure signal. It was restored immediately
after capturing this output; `git stash pop` restored `adapter.go`.)

### Passes with the fix

```
=== RUN   TestCreateSessionAndRunTurn
--- PASS: TestCreateSessionAndRunTurn (0.02s)
=== RUN   TestRunTurnReconstructsAndRejectsConversationMismatch
--- PASS: TestRunTurnReconstructsAndRejectsConversationMismatch (0.01s)
=== RUN   TestRunTurnRepairsAtMostOnce
--- PASS: TestRunTurnRepairsAtMostOnce (0.01s)
=== RUN   TestRunTurnRecoversFromArtifactPathError
--- PASS: TestRunTurnRecoversFromArtifactPathError (0.01s)
=== RUN   TestRunTurnArtifactPathErrorRepairsAtMostOnce
--- PASS: TestRunTurnArtifactPathErrorRepairsAtMostOnce (0.01s)
=== RUN   TestIsArtifactPathError
=== RUN   TestIsArtifactPathError/nil_error
=== RUN   TestIsArtifactPathError/agy_artifact-path_error
=== RUN   TestIsArtifactPathError/unrelated_terminal_error
--- PASS: TestIsArtifactPathError (0.00s)
    --- PASS: TestIsArtifactPathError/nil_error (0.00s)
    --- PASS: TestIsArtifactPathError/agy_artifact-path_error (0.00s)
    --- PASS: TestIsArtifactPathError/unrelated_terminal_error (0.00s)
=== RUN   TestRunTurnTimeoutInterruptsProcess
--- PASS: TestRunTurnTimeoutInterruptsProcess (0.05s)
=== RUN   TestSteerInterruptsAndResumesSameRunTurn
--- PASS: TestSteerInterruptsAndResumesSameRunTurn (0.02s)
=== RUN   TestSteerInactiveSessionDoesNotQueue
--- PASS: TestSteerInactiveSessionDoesNotQueue (0.01s)
=== RUN   TestCompactSendsNativeCommand
--- PASS: TestCompactSendsNativeCommand (0.01s)
=== RUN   TestCreateSessionClassifiesIDLessServiceFailure
--- PASS: TestCreateSessionClassifiesIDLessServiceFailure (0.00s)
=== RUN   TestCategorize
--- PASS: TestCategorize (0.00s)
=== RUN   TestModelIncludesEffort
--- PASS: TestModelIncludesEffort (0.00s)
=== RUN   TestAgyHelperProcess
--- PASS: TestAgyHelperProcess (0.00s)
=== RUN   TestEventProjectorCoalescesAndPairs
--- PASS: TestEventProjectorCoalescesAndPairs (0.00s)
PASS
ok  	github.com/tylergannon/tractor/harness/agy	0.395s
```

## Full verification

```
$ gofmt -l .
(no output — clean)

$ go vet ./...
(no output — clean)

$ go test ./...
ok  	github.com/tylergannon/tractor/cmd/agent	0.216s
ok  	github.com/tylergannon/tractor/cmd/tractor	6.134s
ok  	github.com/tylergannon/tractor/engine	4.644s
ok  	github.com/tylergannon/tractor/examples	1.970s
ok  	github.com/tylergannon/tractor/graph	1.077s
ok  	github.com/tylergannon/tractor/harness	1.647s
ok  	github.com/tylergannon/tractor/harness/agy	1.920s
ok  	github.com/tylergannon/tractor/harness/claude	0.700s
ok  	github.com/tylergannon/tractor/harness/codex	1.357s
ok  	github.com/tylergannon/tractor/internal/runlog	1.437s
ok  	github.com/tylergannon/tractor/lint	0.454s
```

## Files changed

- `harness/agy/adapter.go` — the fix.
- `harness/agy/adapter_test.go` — the regression tests and fake-`agy`-process
  modes (`artifact_recover`, `artifact_persist`) that pin it.

No files under `/Users/tyler/src/tractor` (main checkout) or
`antigravity-integration-plan` were touched. No real `agy`/Gemini invocations
were made; the fix and tests are derived entirely from the recorded run
evidence and a fake subprocess harness matching the existing test style.
