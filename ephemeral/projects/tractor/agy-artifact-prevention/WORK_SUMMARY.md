# agy artifact-path bug: preventive layer (round 3 — rework)

## TL;DR

Round 2's hook denied native writes based on **target path alone**
(outside vs. inside the conversation's artifact directory). Independent
review's round-3 finding correctly identified that this still blocks
plenty of legitimate plain workspace writes that have nothing to do with
the bug, because path was never the actual trigger condition. This round
found the real trigger — an **`ArtifactMetadata` argument** on the tool
call, not the path — by capturing two real `write_to_file` calls to the
identical path that differed only in whether they carried it (one failed,
one succeeded), and rewrote the hook to gate on that instead. It also
formally drops the manifest/`--agent`/`init.tools` contract from the
original task spec, under an explicit supervisor design ruling issued in
response to this same finding (full text and rationale below); fixes the
file-mode-widening and lock-boundary-overclaim issues from findings 4; adds
both required `init.tools` dumps for finding 5; and redesigns the
repair-retry to use a fresh conversation, since this round's live testing
found the sticky-status defect (round 2) reproduces **within a single
turn**, not just across resumed turns, making a same-conversation repair
attempt unsound regardless. Full reasoning and all live evidence:
`ephemeral/worklog/202608191900-agy-artifact-prevention.md` (durable,
survives the source research reports being deleted).

## The supervisor design ruling (addresses finding 1)

Finding 1 asked to either implement the manifest/`--agent`/`init.tools`
contract "on an empirically supported agy version" or obtain an explicit
scope/design change from the supervisor. Only agy 1.1.15 is installed in
this environment — there is no newer release to re-test against — and this
round added independent, static corroboration of round 1's live finding
that the `tools:` allowlist is inert for `--agent`-selected mainAgents: the
agy 1.1.15 binary's own embedded changelog text lists the Markdown
custom-agent frontmatter fields it supports (`mainAgent`, `subagent`,
`hidden`, `inheritMcp`, `commandExecutionPolicy`) and `tools:` is not among
them, even though a `Tools` struct field exists elsewhere in the binary's
parsing code. Mid-round, the supervisor issued this ruling, which the
implementation below follows exactly:

> SUPERVISOR DESIGN RULING — this answers EVAL_FEEDBACK finding 1's request
> for an explicit scope/design change, and supersedes the original
> acceptance design. (a) The manifest/--agent/init.tools contract is
> FORMALLY DROPPED: your round-1 evidence proved agy 1.1.15 does not
> enforce the allowlist, so the PreToolUse hook IS the accepted prevention
> design. Do not implement --agent or init.tools. Record this ruling in
> WORK_SUMMARY.md and the worklog. (b) NEW INTELLIGENCE resolving finding
> 2: per agy tool documentation, IsArtifact:true on write_to_file is meant
> ONLY for internal session-tracking documents (task checklists,
> ArtifactType "task") that live in the brain directory; the bug is the
> model MISAPPLYING IsArtifact:true to workspace deliverable files. [...]
> Therefore rewrite the hook policy: DENY only when the call carries
> artifact semantics (IsArtifact true, or ArtifactMetadata present —
> confirm exact arg names against the real payload schema you captured in
> round 2) AND the target is outside artifactDirectoryPath; the deny reason
> should instruct retrying the SAME write_to_file immediately with
> IsArtifact false (do not push the model to the shell). [...] (c) Finding
> 3 scope change AUTHORIZED: you may now modify the ae5edb6 repair-retry —
> on artifact-path or hook-denial errors, retry in a FRESH conversation
> carrying the original prompt plus a corrective instruction [...], instead
> of resuming the poisoned conversation; keep it bounded to one retry;
> update its tests. If feasible, add one bounded real Adapter.RunTurn proof
> of a denied-then-recovered turn. (d) Findings 4 and 5: fix exactly as
> written [...]

**What was verified vs. taken on trust**: (a) was already independently
corroborated by this round's static binary inspection before the ruling
arrived, so it's actual confirmation, not just an instruction followed
blind. (b)'s premise (an artifact-flagging argument gates the failure) was
re-verified live in this round with a fresh capture — see "New empirical
finding" below — but the **exact field name differs from what the ruling
described**: real agy 1.1.15's payload carries a nested `ArtifactMetadata`
object (`{"RequestFeedback":bool,"Summary":string,"UserFacing":bool}`),
whose *presence* gates the failure — not a boolean `IsArtifact` field.
There is no `ArtifactType: "task"` distinction visible in the captured
payload either. The hook, repair prompt, and prompt preamble all use the
verified `ArtifactMetadata`-presence semantics, not the ruling's speculated
`IsArtifact` boolean. (c) is implemented as specified, including the real
`Adapter.RunTurn` proof. (d) is implemented as specified.

## New empirical finding: `ArtifactMetadata`, not path, is the trigger

Captured live (real agy 1.1.15, `gemini-3.5-flash-low`, temp workspace,
`--dangerously-skip-permissions --output-format stream-json`) via a debug
`PreToolUse` hook that logged its own stdin before allowing everything
through, prompting a deliverable-style `write_to_file`:

- **Attempt 1** (model's own unprompted choice): `args` included
  `"ArtifactMetadata":{"RequestFeedback":false,"Summary":"...","UserFacing":true}`
  → `step_update.state: "ERROR"`, the classic artifact-path message.
- **Attempt 2**, same turn, same target path, `ArtifactMetadata` simply
  omitted from `args` (everything else unchanged) → `step_update.state:
  "DONE"`, file correctly on disk.
- **Separate run**, explicitly instructed to omit `ArtifactMetadata`:
  `result.status: "SUCCESS"` on the first attempt, no failed step.

This supersedes round 2's finding that the bug "didn't reproduce
reliably" — the earlier repro attempts varied prompt wording, not whether
the model attached `ArtifactMetadata`; the apparent non-determinism was the
model's own token-level choice for a given phrasing, not CLI flakiness.
Full transcripts in the worklog.

## How each `EVAL_FEEDBACK.md` finding was addressed

**1. Critical — manifest/--agent/init.tools contract not implemented.**
Addressed by the supervisor design ruling above: formally dropped, not
re-attempted, since only agy 1.1.15 is available and its inertness is now
corroborated two independent ways (round 1 live testing, round 3 static
binary/changelog inspection).

**2. Critical — the hook blocks legitimate native workspace writes.**
Fixed by rewriting `nativeWriteHookCommand`
(`harness/agy/native_write_hook.go`) to gate on `ArtifactMetadata`
presence, verified against the real payload shape above, rather than path
alone. A plain workspace write with no `ArtifactMetadata` argument is now
allowed unconditionally, regardless of target — the actual legal case round
2's path-only policy still denied. Tests:
`TestNativeWriteHookCommandAllowsPlainWorkspaceWrite`,
`TestNativeWriteHookCommandAllowsInBrainArtifact`,
`TestNativeWriteHookCommandDeniesOutOfBrainArtifact`,
`TestNativeWriteHookCommandDeniesWhenArtifactDirectoryMissing`,
`TestNativeWriteHookCommandDeniesEmptyPayload`,
`TestNativeWriteHookCommandAllowsCompletelyEmptyPayload`,
`TestNativeWriteHookCommandDeniesIfAnyTargetEscapesRoot` — all run the real
generated shell command via `sh -c`, not just assert on Go source.

**3. Issue — repair-retry resumes a status-poisoned conversation.**
Authorized and implemented per ruling (c): `RunTurn`
(`harness/agy/adapter.go`) now starts a fresh agy conversation
(`--new-project`, no `--conversation`) for the one bounded repair attempt,
carrying the original prompt plus a corrective note about omitting
`ArtifactMetadata`. On success, `sessionState.adoptConversationID` records
the new conversation ID so later `RunTurn`/`Compact` calls against the same
Tractor session resume the repaired conversation, not the abandoned one.
Tests: `TestRunTurnRecoversFromArtifactPathError` (now also asserts
`--new-project` present, `--conversation` absent, on the repair
invocation, and that the original task prompt is carried into the repair
prompt), `TestRunTurnArtifactRepairAdoptsFreshConversationForFutureTurns`
(new — proves a later `Compact` call resumes the adopted conversation),
`TestRunTurnRecoversFromNativeWriteHookDenial`, both `*RepairsAtMostOnce`
tests (bound still holds). Plus the real `Adapter.RunTurn` proof below.

**4. Issue — global `hooks.json` write widens file mode; lock's boundary
overstated.** `ensureNativeWriteHook` now stats an existing file and
reuses its permission bits instead of forcing `0o644` (only a brand-new
file gets that default) — test:
`TestEnsureNativeWriteHookPreservesExistingFileMode` (writes a `0o600`
file, provisions, asserts mode unchanged). Doc comments on
`lockHooksConfig` and `ensureNativeWriteHook` now state plainly that the
`flock` only binds *cooperating* writers (other code taking the same lock
file) and does not protect against a person hand-editing the file, or any
program writing it directly without acquiring the lock — the previous
"prevents loss against a person or arbitrary configuration tool" framing is
removed.

**5. Issue — `WORK_SUMMARY.md` missing the two `init.tools` dumps.**
Both included below, sourced from round 1's raw pipeline evidence (already
recorded in the worklog; not re-run live this round, since finding 1 asks
for a design decision on the *existing* evidence, not new experiments).

### Both required `init.tools` dumps (finding 5)

**Baseline** — `agy -p "..." --output-format stream-json` (no `--agent`),
agy 1.1.15: `init.tools` reports 57 tools, alphabetically:

```
ask_custom_permission, ask_permission, ask_question, browser_click_element,
browser_drag_pixel_to_pixel, browser_get_dom, browser_get_network_request,
browser_input, browser_list_network_requests, browser_mouse_down,
browser_mouse_up, browser_move_mouse, browser_press_key,
browser_refresh_page, browser_resize_window, browser_scroll,
browser_scroll_dom, browser_select_option, browser_subagent,
call_mcp_tool, capture_browser_console_logs, capture_browser_screenshot,
click_browser_pixel, command_status, define_subagent, delete_knowledge,
execute_browser_javascript, find_by_name, finish, generate_image,
grep_search, invoke_subagent, list_browser_pages, list_dir,
list_permissions, list_resources, manage_inbox, manage_subagents,
manage_task, multi_replace_file_content, notebook_edit,
notebook_execution, open_browser_url, read_browser_page, read_resource,
read_url_content, replace_file_content, run_command, schedule,
search_web, sed_file, send_command_input, send_message, view_file, wait,
wait_5_seconds, write_to_file
```

`write_to_file` is present in the baseline (57 tools total).

**Selected-agent** — `agy --agent tractor-shell-only-probe -p "..."
--output-format stream-json`, agy 1.1.15, manifest's `tools:` list
excluding `write_to_file`/`replace_file_content`/`multi_replace_file_content`:
`init.tools` reports the **identical 57-tool list above**, `write_to_file`
still included, unchanged from baseline. This is the direct evidence that
the allowlist is inert for a `--agent`-selected mainAgent: selecting the
restrictive agent changed nothing about what the CLI itself reports as
available, and (per round 1 invocation 4) `write_to_file` remained fully
callable and executed when the model was prompted to use it.

## Smoke-test evidence (round 3, real agy 1.1.15, all under `timeout 120`)

All invocations used a temp workspace under `/tmp`, model
`gemini-3.5-flash-low`, `--output-format stream-json`,
`--dangerously-skip-permissions`. `~/.gemini/config/hooks.json` was used
only transiently (a debug logging hook, then the real Go-generated hook via
`ensureNativeWriteHook` against the real `$HOME`) and removed afterward;
the developer's machine is back to its pre-work state (`~/.gemini/config/`
has no `hooks.json`; `~/.gemini/config/agents/` is empty; no manifest was
ever provisioned, per the design ruling).

**Mechanism capture**: see "New empirical finding" above — the two
`write_to_file` attempts (with/without `ArtifactMetadata`) that pinned
down the real trigger condition.

**Hook logic, real generated command, `sh -c`** (not the fake-subprocess
harness — the actual `nativeWriteHookCommand()` output executed for real):
an `ArtifactMetadata` call to a workspace path → denied, reason names
`ArtifactMetadata` and instructs omitting it on retry; the identical call
without `ArtifactMetadata` → allowed; an `ArtifactMetadata` call inside
`artifactDirectoryPath` → allowed. All three match the captured real
payload shape.

**Real `Adapter.RunTurn` end-to-end proof** (not the fake-subprocess test
harness — the real `agy` binary via `Adapter.CreateSession` +
`Adapter.RunTurn`, a throwaway test file deleted after use): prompted to
`write_to_file` a deliverable with an explicit `ArtifactMetadata` argument
to a workspace path, deterministically reproducing agy's own native
artifact-path validator failure. `RunTurn` returned
`harness.Result{"done":true}` with the target file genuinely on disk,
having internally absorbed the failed first attempt and completed the
fresh-conversation repair transparently — proving the adapter contract
(`RunTurn` either returns a valid result or a terminal/retryable/interrupted
error; repair is invisible to the caller) holds against a real, live
failure. Event sequence observed: `user, usage, tool_call, usage,
tool_call, tool_result, assistant, usage, usage, user, usage, tool_call,
tool_result, assistant, usage, usage` — the second `user` event is the
fresh-conversation repair prompt being injected. (This run happened to trip
agy's own native validator rather than Tractor's hook specifically, since
it ran against a fresh, unprovisioned `$HOME` with no hook installed — the
hook's own allow/deny behavior is separately verified above via the real
generated script against real captured payloads.)

**A quoting bug found and fixed along the way**: the hook script wraps its
JSON decision in a single-quoted `printf` argument; round 3's deny-reason
text contains a literal apostrophe ("the conversation's private artifact
directory"), and POSIX sh has no escape sequence inside single quotes —
this silently broke the generated script (syntax error, hook effectively a
no-op) until caught by actually executing it via `sh -c` rather than only
inspecting the Go source. Fixed with `shSingleQuote` (closes/reopens the
quote around each embedded `'`). Worth flagging because it's easy to
reintroduce by editing hook text without re-running the real command.

## Test results

```
$ gofmt -l .
(no output — clean)

$ go vet ./...
(no output — clean)

$ go test ./harness/agy/... -race -count=1 -v
=== RUN   TestCreateSessionAndRunTurn
--- PASS
=== RUN   TestRunTurnPromptIncludesArtifactMetadataPreamble        (new)
--- PASS
=== RUN   TestRunTurnReconstructsAndRejectsConversationMismatch
--- PASS
=== RUN   TestRunTurnRepairsAtMostOnce
--- PASS
=== RUN   TestRunTurnRecoversFromArtifactPathError
--- PASS
=== RUN   TestRunTurnArtifactRepairAdoptsFreshConversationForFutureTurns (new)
--- PASS
=== RUN   TestRunTurnArtifactPathErrorRepairsAtMostOnce
--- PASS
=== RUN   TestIsArtifactPathError (+ subtests)
--- PASS
=== RUN   TestRunTurnRecoversFromNativeWriteHookDenial
--- PASS
=== RUN   TestRunTurnNativeWriteHookDenialRepairsAtMostOnce
--- PASS
=== RUN   TestEnsureNativeWriteHookIdempotentAndContentCorrect
--- PASS
=== RUN   TestEnsureNativeWriteHookMergesWithExistingHooks
--- PASS
=== RUN   TestEnsureNativeWriteHookRefusesForeignEntry
--- PASS
=== RUN   TestEnsureHookFailsFastOnOldAgyVersion
--- PASS
=== RUN   TestEnsureHookFailsFastOnUnparseableAgyVersion
--- PASS
=== RUN   TestAdapterProvisionsHookBeforeFirstInvocation
--- PASS
=== RUN   TestRunTurnTimeoutInterruptsProcess
--- PASS
=== RUN   TestSteerInterruptsAndResumesSameRunTurn
--- PASS
=== RUN   TestSteerInactiveSessionDoesNotQueue
--- PASS
=== RUN   TestCompactSendsNativeCommand
--- PASS
=== RUN   TestCreateSessionClassifiesIDLessServiceFailure
--- PASS
=== RUN   TestCategorize
--- PASS
=== RUN   TestModelIncludesEffort
--- PASS
=== RUN   TestAgyHelperProcess
--- PASS
=== RUN   TestEventProjectorCoalescesAndPairs
--- PASS
=== RUN   TestNativeWriteHookCommandAllowsPlainWorkspaceWrite       (new)
--- PASS
=== RUN   TestNativeWriteHookCommandAllowsInBrainArtifact           (renamed)
--- PASS
=== RUN   TestNativeWriteHookCommandDeniesOutOfBrainArtifact        (renamed/rewritten)
--- PASS
=== RUN   TestNativeWriteHookCommandDeniesWhenArtifactDirectoryMissing
--- PASS
=== RUN   TestNativeWriteHookCommandDeniesEmptyPayload
--- PASS
=== RUN   TestNativeWriteHookCommandAllowsCompletelyEmptyPayload    (new)
--- PASS
=== RUN   TestNativeWriteHookCommandDeniesIfAnyTargetEscapesRoot
--- PASS
=== RUN   TestAgyVersionAtLeast
--- PASS
=== RUN   TestAgyVersionAtLeastRejectsUnparseableVersions
--- PASS
=== RUN   TestEnsureNativeWriteHookPreservesExistingFileMode        (new)
--- PASS
=== RUN   TestEnsureNativeWriteHookConcurrentProvisioningLosesNoUpdates
--- PASS
PASS
ok  	github.com/tylergannon/tractor/harness/agy	38.923s

$ go test ./...
ok  	github.com/tylergannon/tractor/cmd/agent
ok  	github.com/tylergannon/tractor/cmd/tractor
ok  	github.com/tylergannon/tractor/engine
ok  	github.com/tylergannon/tractor/examples
ok  	github.com/tylergannon/tractor/graph
ok  	github.com/tylergannon/tractor/harness
ok  	github.com/tylergannon/tractor/harness/agy
ok  	github.com/tylergannon/tractor/harness/claude
ok  	github.com/tylergannon/tractor/harness/codex
ok  	github.com/tylergannon/tractor/internal/runlog
ok  	github.com/tylergannon/tractor/lint
```

## Layer zero: prompt preamble (supervisor addendum, implemented)

Mid-round the supervisor offered an optional addendum: prepend a static
line to the turn prompt discouraging `ArtifactMetadata`/`IsArtifact` on
workspace writes, explicitly marked as not required and skippable. Since
this round already had the correct field name (`ArtifactMetadata`, not
`IsArtifact`) from live evidence, it was cheap to implement correctly:
`artifactMetadataPromptPreamble` (`harness/agy/adapter.go`) is prepended
once to the original turn prompt (not to repair or schema-repair prompts).
It does not replace the hook — prompting alone is unreliable, per the
"Attempt 1 vs. Attempt 2" evidence above, where the *same* underlying model
attached `ArtifactMetadata` unprompted in one case — but it's a free
reduction in how often a hook denial or repair even needs to fire. Test:
`TestRunTurnPromptIncludesArtifactMetadataPreamble`.

## Files changed

- `harness/agy/native_write_hook.go` — `ArtifactMetadata`-aware hook
  command (replacing round 2's path-only policy); `shSingleQuote` fix;
  file-mode preservation; honest lock-boundary doc comments.
- `harness/agy/adapter.go` — `artifactMetadataPromptPreamble` (layer
  zero); `sessionState.agyConversationID` +
  `resolveConversationID`/`adoptConversationID`; `RunTurn`'s repair branch
  now starts a fresh conversation and adopts it; `Compact` resolves through
  the adopted conversation ID; `artifactWriteRepairPrompt` rewritten
  (original prompt + `ArtifactMetadata`-omission instruction, not a push to
  `run_command`).
- `harness/agy/adapter_test.go` — fake-helper `--new-project`/repair-shape
  detection rewritten for the new mechanism; new/updated tests per finding
  3 above.
- `harness/agy/native_write_hook_test.go` — hook-decision tests rewritten
  for `ArtifactMetadata` semantics; new file-mode-preservation test.
- `go.mod` — unchanged this round (still `golang.org/x/sys` direct, from
  round 2, for `unix.Flock`).
- `ephemeral/worklog/202608191900-agy-artifact-prevention.md` — rewritten
  in place: corrected failure mechanism, dropped-manifest ruling record,
  three-layer design, both `init.tools` dumps, all round-3 live evidence,
  updated upstream bug reports, round-by-round history section.

No files under the main checkout (`/Users/tyler/src/tractor`) were
touched. The developer's real `~/.gemini` was used only for the smoke
invocations described above, fully cleaned up afterward (`hooks.json`
removed; no manifest was ever written, per the design ruling). Nothing
committed.
