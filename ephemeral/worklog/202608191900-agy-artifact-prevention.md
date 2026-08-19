# agy artifact-path bug: the ArtifactMetadata mechanism, and the three-layer fix

Distillation of two now-deleted research reports (`deep-research-report-2.md`,
primary; `Antigravity CLI Path Configuration.md`, corroborating) plus real
`agy 1.1.15` smoke invocations run across three rounds of this work, kept
here because the source reports will not survive. Continues
[[202608191356-agy-artifact-validator]] (the reactive repair-retry added in
`ae5edb6`), which this work keeps as the last-resort fallback layer.

**Round 3** (this revision) found the actual trigger condition — an
`ArtifactMetadata` argument on the tool call, not merely a path outside the
workspace — replacing round 2's path-only "artifact-aware" hook (which
independent review found still broke legitimate workspace writes) and
round 1's abandoned custom-agent-manifest plan. See "History across rounds"
at the end for what changed and why.

## The failure mechanism

`agy`'s native `write_to_file`, `replace_file_content`, and (assumed,
uncaptured — see below) `multi_replace_file_content` physically write the
requested file successfully, then agy's own end-of-turn bookkeeping —
"declaring permissions", converting the tool call into an artifact-metadata
record inside agy's internal `cortex` layer — rejects the call if **both**:

1. the call's `args` include an `ArtifactMetadata` object (agy's own tool
   schema exposes this for `write_to_file`; captured live as
   `{"RequestFeedback":bool,"Summary":string,"UserFacing":bool}`), **and**
2. the target (`args.TargetFile`) lies outside agy's private
   per-conversation `~/.gemini/antigravity-cli/brain/<uuid>/` directory.

```
declaring permissions: cortex tool write_to_file: convert tool call for
permissions: model output error: invalid tool call error (invalid_args)
<path> is not a valid artifact path; artifacts must be in
~/.gemini/antigravity-cli/brain/<uuid>/
```

This fails the **whole turn**, discarding structured output along with it,
even though the file is genuinely on disk. `--dangerously-skip-permissions`
does not affect this: it only auto-approves the interactive tool-permission
prompt, a different check from artifact-path validation.

**This is deterministic and directly demonstrated, not correlational.**
Round 3 captured the exact same `write_to_file` call, same target path,
same conversation, twice in one turn: the model's first attempt included
`ArtifactMetadata` and failed with the error above; its second attempt (same
target, same content, `ArtifactMetadata` simply omitted) succeeded with no
error, and the file's final content was the second attempt's. This
supersedes round 2's finding that the bug "did not reproduce reliably" —
the earlier repro attempts varied the prompt's wording, not whether the
model attached `ArtifactMetadata`, so the observed non-determinism was
really the model's own token-level choice of whether to include that
argument for a given phrasing, not a flaky bug. A model instructed
explicitly to omit `ArtifactMetadata` on a deliverable-style write
succeeded cleanly on the first attempt (`status: "SUCCESS"`, no failed step
at all) — see "Live evidence" below.

## Why no flag/setting/env var fixes the artifact root

Exhaustively audited (both reports, current as of 2026-08-19): `--add-dir`
scopes filesystem *access*, not the artifact namespace — the CLI's own Hooks
API exposes `workspacePaths` and `artifactDirectoryPath` as distinct fields.
`settings.json`'s `artifactReviewPolicy: "always-proceed"` only skips the
human *review* step; it does not relax path validation. `--mode`,
`--sandbox`, and `--input-format stream-json` (new in 1.1.15) don't touch
tool selection or the artifact root. No `AGY_*`/`GEMINI_*` environment
variable does either. **There is no supported way to make a workspace path
a valid artifact path, and no way to make agy stop attaching
`ArtifactMetadata` other than the model's own tool-call choice.**

## The documented custom-agent mitigation: dropped, not implemented

Google documents a custom-agent `tools:` frontmatter allowlist, selected
with `agy --agent <name>`, as removing tools from an agent's toolset. The
primary report's central recommendation was: define a `mainAgent: true`
custom agent whose `tools:` list omits the three native writers, launch it
with `--agent`, and verify via the `stream-json` `init.tools` field that
they're absent.

**This does not work for a `--agent`-selected mainAgent in real agy
1.1.15.** Five bounded real invocations in round 1 (model
`gemini-3.5-flash-low`, `--output-format stream-json`,
`--dangerously-skip-permissions`, temp workspaces under `/tmp`, `timeout
120` on every call) established this empirically:

1. **Baseline, no `--agent`**: `init.tools` lists 57 tools, including
   `write_to_file` (full list below).
2. **`--agent` with a manifest whose `tools:` used `init.tools`-style names
   verbatim**: agy failed immediately with `unknown component: tool "X" not
   found in registry"` for most of them — `tools:` validates against a
   *different, narrower* identifier namespace than `init.tools` reports.
3. **`--agent` with a manifest whose `tools:` used only the ~20 names that
   validated cleanly (including `run_command`, omitting the three native
   writers)**: the turn succeeded, but `init.tools` still reported the full
   57-tool baseline, `write_to_file` included.
4. **Same manifest, prompt explicitly forcing `write_to_file`**: the model
   called it and it fully executed, reproducing the original bug. The tool
   was never actually removed from the model's capability.
5. **Manifest with `tools: [view_file]` only**: `init.tools` unchanged,
   turn behaved identically to no restriction at all.

Conclusion: for a `--agent`-selected `mainAgent`, agy 1.1.15's `tools:`
allowlist is **inert**. It's possible the field only functions for
dynamically-defined subagents (`define_subagent`'s tool-restriction
parameters, invoked via `invoke_subagent`), not for the CLI's `--agent`
selection path, but that's inference, not independently re-tested.

**Round 3 static corroboration** (no live agy invocation needed): the agy
1.1.15 binary's own embedded changelog text (`strings agy | grep -A1
'Custom Agents (Markdown Format)'`) documents exactly which frontmatter
fields Markdown agents support: *"Markdown agents support `mainAgent`,
`subagent`, `hidden`, `inheritMcp`, and `commandExecutionPolicy` frontmatter
fields for fine-grained control over agent behavior."* `tools:` is not on
that list, even though the binary's own struct reflection metadata (`strings
agy | grep 'yaml:"tools,omitempty"'`) shows a `Tools` field exists somewhere
in agy's config parsing — consistent with "the field parses but isn't wired
up for this code path," not "the field is documented and simply
untested." This corroborates, from a completely independent angle (static
binary inspection vs. live behavioral testing), that round 1's finding
wasn't a fluke of that specific run.

**Supervisor design ruling (round 3, in response to independent-review
finding 1 asking for either re-verification on a supported agy version or
an explicit scope change): the manifest/`--agent`/`init.tools` contract is
formally dropped from Tractor's design.** Only agy 1.1.15 is installed in
this environment (`agy --version`); there is no newer release to re-test
against, and the inertness above is now corroborated two independent ways
(live behavioral testing in round 1, static binary/changelog inspection in
round 3). The `init.tools`-parses-but-can't-be-satisfied problem round 2
already identified still holds: `write_to_file` is unconditionally present
in `init.tools` regardless of any manifest, so a literal "fail fast if a
native writer is advertised" invariant would fail on 100% of turns forever
— a total, self-inflicted outage, not a useful guard. `harness/agy` does
not provision a custom agent, does not pass `--agent`, and does not parse
`init.tools`. The `PreToolUse` hook (below) is the accepted primary
prevention mechanism, not a fallback-of-last-resort alternative to a
manifest that was never shippable. If a future agy version starts honoring
`tools:` for mainAgents, this is worth revisiting; the manifest shape from
the primary report, kept here for that purpose only, is:

```yaml
---
name: <tractor-owned-name>
mainAgent: true
tools:
  - view_file
  - list_dir
  - find_by_name
  - grep_search
  - run_command
  # omit write_to_file, replace_file_content, multi_replace_file_content
---
```

placed at `~/.gemini/config/agents/<name>/agent.md` (global) or
`.agents/agents/<name>/agent.md` (workspace-local), selected via `agy
--agent <name>`. Re-run the same style of empirical smoke test (invocation
4 above) before trusting it again, on whatever agy version is current then.

## Empirically verified tool identifiers (agy 1.1.15, 2026-08-19)

`init.tools` baseline (57 tools; the two source reports disagreed with each
other on names like `view_file` vs `read_file` — this is what agy actually
reports, both with and without `--agent`, per round 1):

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

The three native writers Google's Hooks reference documents as
file-mutating (and, for `write_to_file`/`multi_replace_file_content`,
`ArtifactMetadata`-capable): `write_to_file`, `replace_file_content`,
`multi_replace_file_content`. These are the ones the hook targets.
`sed_file` and `notebook_edit` are also plausibly native mutators (new
identifiers neither report named), but weren't independently confirmed to
carry `ArtifactMetadata`, so they're intentionally left alone rather than
speculatively blocked.

**Both required dumps, labeled** (independent-review finding 5 for round
2 asked these be recorded verbatim in `WORK_SUMMARY.md` rather than only
here — repeated in both places):

- **Baseline** (`agy -p ... --output-format stream-json`, no `--agent`,
  agy 1.1.15): `init.tools` = the 57-tool list above, `write_to_file`
  included.
- **Selected-agent** (`agy --agent tractor-shell-only-probe -p ...`, same
  agy 1.1.15, manifest excluding the three native writers): `init.tools` =
  the identical 57-tool list, `write_to_file` still included, unchanged
  from baseline. This is the direct evidence for "the allowlist is inert":
  selecting the restrictive agent changed nothing about what the CLI itself
  reports as available.

## The `init.tools` verification invariant — not implemented, and why

The original plan was: parse `init.tools`, fail fast with a clear terminal
error if a native writer is advertised. This cannot be implemented as a
hard invariant against agy 1.1.15, for the reason above: `write_to_file` /
`replace_file_content` / `multi_replace_file_content` are **always**
present in `init.tools` regardless of any manifest. Enforcing "must be
absent" as fatal would fail every single turn — worse than the bug it
replaces. `harness/agy` does not parse or enforce `init.tools`.

## What Tractor implements: three layers

### Layer zero: a static prompt preamble (new, round 3, optional-but-cheap)

`artifactMetadataPromptPreamble` (`harness/agy/adapter.go`) is prepended
once, to the original turn's prompt only, telling the model never to
attach `ArtifactMetadata` to a workspace write. This is *not* a substitute
for the hook: the same instruction, given verbatim to the model, still let
it attach `ArtifactMetadata` to an out-of-workspace write in some live
trials (prompting a stochastic model is inherently unreliable). But every
hook denial or native-validator failure burns a full turn plus a
fresh-conversation repair (real cost: 5–20 real-world seconds and the
turn's full token spend, discarded), so a static line that reduces how
often that happens at all is worth the negligible per-turn token cost.

### Layer one: an `ArtifactMetadata`-aware `PreToolUse` hook

agy's lifecycle-hooks system (`hooks.json`, documented inside the CLI
binary itself — `strings agy | grep -A200 'Lifecycle Hooks'` dumps the full
spec) supports a `PreToolUse` event that fires **before** a tool executes
and can hard-block it:

```json
{
  "tractor-no-native-write": {
    "PreToolUse": [
      {
        "matcher": "write_to_file|replace_file_content|multi_replace_file_content",
        "hooks": [ { "command": "<inline shell command>", "timeout": 5 } ]
      }
    ]
  }
}
```

`nativeWriteHookCommand` (`harness/agy/native_write_hook.go`) reads its
JSON stdin and prints an allow/deny decision:

- **allow** when the call has no `ArtifactMetadata` argument at all (an
  ordinary workspace write — legal wherever it targets, since agy's own
  validator never rejects these), or when every extracted `TargetFile`
  lies inside `artifactDirectoryPath` (a genuine artifact write, which
  agy's own validator accepts anyway);
- **deny** (marker-tagged reason, instructing an immediate retry of the
  *same* call with `ArtifactMetadata` omitted — not a push onto
  `run_command`) when the call carries `ArtifactMetadata` **and** any
  target lies outside `artifactDirectoryPath` — the one combination
  confirmed to reproduce the bug — or when the payload doesn't parse as
  expected (fail closed: this can only ever wrongly deny a call agy would
  have accepted, never wrongly allow one it would reject).

The extraction is plain-text (`grep`/`sed`/`case`), not a JSON library, to
keep the hook command a single portable POSIX-sh script with no `jq`/
`python3` dependency on the invoking machine.

**A POSIX-sh quoting bug found and fixed while writing this round's deny
reason**: the script wraps its JSON output in a single-quoted `printf`
argument (`printf '%s\n' '<json>'`); POSIX sh has no escape sequence inside
single quotes, so a literal `'` in the reason text (round 3's text says
"the conversation's private artifact directory") broke the generated
script with a syntax error, silently making the hook a no-op (agy would
see the hook command fail rather than emit a decision). `shSingleQuote`
(`harness/agy/native_write_hook.go`) closes/reopens the quote around any
embedded `'` (`'\''`). Caught by actually executing the generated script
via `sh -c`, not just eyeballing the Go string — a reminder that this
hook's tests must always run the *real* generated command end-to-end, not
assert against the Go source that builds it.

**Provisioning**: global (`~/.gemini/config/hooks.json`), not
workspace-local, for the reason unchanged since round 1 — Tractor's
isolated-worktree branches (`engine/artifacts.go`, added in `2485e4c`) each
get their own workdir, so a workspace-local hook file would need
provisioning into and cleanup from every branch worktree and risk being
swept into artifact collection. `ensureNativeWriteHook` merges (not
replaces) the document, only ever touching its own
`"tractor-no-native-write"` key; is idempotent; refuses to clobber a
same-named non-Tractor entry; is guarded by an exclusive `flock` on a
sibling lock file for the read-modify-write; and writes via a
same-directory temp file plus atomic rename. It now **preserves an
existing file's permission bits** rather than forcing `0o644` (round 2
widened a pre-existing `0o600` file; fixed in round 3 — only a brand-new
file gets the `0o644` default).

**The lock's honest boundary** (round 3 correction — round 2's comments and
worklog overstated this): `flock` is advisory and only binds *cooperating*
writers — other code that takes the same `.tractor-hooks.lock` file before
reading and writing, as `ensureNativeWriteHook` and its own concurrency
test both do. It does **not** stop a person hand-editing `hooks.json` in a
text editor, or any other program that writes the file directly without
taking that lock, from racing a Tractor write and having either side's
change silently lost to the other's atomic rename. The atomic rename means
no reader ever observes a *torn* (partially-written) file regardless of who
writes it — that guarantee is unconditional — but "no lost update" is only
guaranteed between cooperating writers.

**Runtime invariant**: `Adapter.ensureHook` runs `agy --version` (cheap, no
model turn) and requires `>= minSupportedAgyVersion` (`1.1.15`, the only
version this hook was verified live against) before writing anything to
`hooks.json`; an older or unparseable version fails the adapter fast with
an actionable error, rather than silently trusting an unverified mechanism.

### Layer two: a fresh-conversation repair-retry (redesigned, round 3)

`ae5edb6`'s original repair-retry resumed the **same** agy conversation for
its one corrective turn. Round 2 discovered why that's unsound: once a
conversation has taken **any** `PreToolUse` denial or native artifact-path
failure, agy's own top-level `result.status`/`result.error` stays stuck at
that stale failure on every later turn in that conversation — even a
resumed turn whose own actions fully succeed, and even a turn that calls no
tools at all. Round 3 reproduced a tighter version of the same defect
**within a single turn**: the model's first `write_to_file` call failed
with the artifact-path error, its second call (same target, no
`ArtifactMetadata`, in the *same* turn) succeeded (`step_update.state:
"DONE"`, correct file on disk) — and the turn's overall `result.status` was
still `"ERROR"`, carrying the first call's stale message. Once any call in
a turn's history hits this failure, the top-level status can't be trusted
again for that conversation, full stop.

**The fix** (`RunTurn` in `harness/agy/adapter.go`, authorized as an
in-scope change by the round-3 supervisor ruling): on an artifact-path
error or hook denial, the bounded, single-shot repair now starts a **fresh
conversation** (`--new-project`, no `--conversation`) instead of resuming
the failed one, carrying the turn's *original* prompt plus a corrective
note (retry the same native write tool, omitting `ArtifactMetadata` — not
"switch to `run_command`", since that's no longer the actual fix). On
success, the session adopts the new conversation ID
(`sessionState.adoptConversationID`) so every later `RunTurn`/`Compact`
call against the same Tractor-visible session ID resumes the repaired
conversation, not the abandoned, status-poisoned one. Still bounded to one
retry; a second failure still propagates as a terminal error.

This does lose the abandoned conversation's turn history for that session
(a fresh conversation starts without it) — an accepted tradeoff given the
alternative (resuming a conversation whose top-level status can no longer
be trusted) is not actually a repair at all, just a turn that *looks* like
it might have recovered.

## Live evidence

### The `ArtifactMetadata` mechanism (round 3)

Real agy 1.1.15, model `gemini-3.5-flash-low`, temp workspace,
`--dangerously-skip-permissions --output-format stream-json`, prompted to
write a deliverable-style proposal via `write_to_file`:

**Attempt 1** (model's own choice, unprompted about `ArtifactMetadata`) —
captured via a debug `PreToolUse` hook that logged its stdin verbatim
before allowing everything through:

```json
{"artifactDirectoryPath":"/Users/tyler/.gemini/antigravity-cli/brain/<uuid>",
 "conversationId":"<uuid>","modelName":"gemini-3.5-flash-extra-low","stepIdx":3,
 "toolCall":{"name":"write_to_file","args":{
   "ArtifactMetadata":{"RequestFeedback":false,"Summary":"Project proposal for the cloud migration and infrastructure modernization project.","UserFacing":true},
   "CodeContent":"# Project Proposal ...","Description":"...","Overwrite":true,
   "TargetFile":"/tmp/.../proposal.md","toolAction":"...","toolSummary":"..."}},
 "workspacePaths":["/tmp/...","/tmp/.../ws-..."]}
```

→ `step_update.state: "ERROR"`, the classic `"is not a valid artifact
path"` message, `result.status: "ERROR"`.

**Attempt 2**, same turn, same conversation, model's own retry of the
identical call, `ArtifactMetadata` simply absent from `args` this time
(every other key unchanged) → `step_update.state: "DONE"`, file correctly
on disk. (Overall `result.status` still `"ERROR"` — the same-turn
sticky-status defect above.)

**Second run**, fresh conversation, explicitly instructed *not* to include
`ArtifactMetadata` on the write: `result.status: "SUCCESS"` on the first
attempt, no failed step at all, file correctly on disk with the requested
content. This is the direct evidence that omitting `ArtifactMetadata`
(not switching tools) is the actual, sufficient fix.

### The redesigned hook (round 3)

The real Go-generated `nativeWriteHookCommand()`, executed via `sh -c`
exactly as agy invokes it (not just asserted against Go source), against
three payload shapes: an `ArtifactMetadata` call targeting a workspace path
→ denied, reason names `ArtifactMetadata` and instructs omitting it; the
identical call with `ArtifactMetadata` omitted → allowed; an
`ArtifactMetadata` call targeting inside `artifactDirectoryPath` → allowed.
All three match the real payload capture above and are now unit tests
(`native_write_hook_test.go`).

### The redesigned repair-retry, end to end (round 3)

A bounded real invocation of the actual `Adapter.CreateSession` +
`Adapter.RunTurn` (not the fake-subprocess test harness — the real `agy`
binary, real stream-json parsing, real process management), prompted to
write a deliverable via `write_to_file` with an explicit `ArtifactMetadata`
argument to a workspace path (forcing the native-validator failure agy's
own bookkeeping produces, deterministically, per the mechanism above):
`RunTurn` returned successfully (`{"done":true}`) with the target file on
disk, having internally absorbed one failed attempt and completed the
fresh-conversation repair transparently to the caller — proving the
adapter-level contract (`RunTurn` either returns a valid result or a
terminal/retryable/interrupted error, repair is invisible to the caller)
holds against a real, live failure, not just the fake-subprocess unit
tests. (This run happened to trip agy's own native validator rather than
Tractor's hook specifically, since the run used a fresh, unprovisioned
`$HOME` with no hook installed — the hook's own allow/deny behavior is
separately verified above via the real generated script.)

## Version constraints

Pin **agy 1.1.15+** (`--agent` since 1.1.1, Markdown `agent.md` custom
agents since 1.1.6, `--output-format stream-json` since 1.1.8,
`--input-format stream-json` since 1.1.15). Hooks (`hooks.json`,
`PreToolUse`) are present and documented in the 1.1.15 binary and enforced
at runtime via the version gate above; no earlier-version testing was
done. The corroborating report's hang-on-unrecognized-tool-identifier
warning applies to the (unused, dropped) `tools:` allowlist path only —
Tractor's current design never provisions a custom agent, so this specific
hang risk doesn't apply to Tractor's invocations.

## Recommended upstream bug reports (`google-antigravity/antigravity-cli`)

**Bug 1 — `ArtifactMetadata` on a native write outside the brain directory
fails the whole turn even though the physical write succeeds** (revised,
round 3, with a deterministic repro — round 1/2's repro attempts varied
prompt wording, not whether the model attached `ArtifactMetadata`, which is
why they looked non-deterministic):

> **Title**: `write_to_file` with an `ArtifactMetadata` argument targeting
> a path outside `brain/<uuid>` succeeds physically, then fails the turn
>
> **Repro**:
> ```bash
> workspace="$(mktemp -d)"
> agy -p "Use the write_to_file tool, with an ArtifactMetadata argument (any Summary, UserFacing:true), to write a short document to $workspace/foo.md." \
>   --add-dir "$workspace" --dangerously-skip-permissions --output-format stream-json --model gemini-3.5-flash-low
> ```
>
> **Expected**: either (1) `ArtifactMetadata` on a call targeting an active
> `--add-dir` workspace path is accepted (or silently ignored) rather than
> rejected, since the file is a legitimate workspace deliverable; or (2)
> the tool schema/prompt guidance makes clear to the model when
> `ArtifactMetadata` is and isn't valid, so this is a model-usage error
> surfaced before the call executes, not an unrecoverable turn failure
> after a real write already landed; or (3) the CLI exposes a documented
> way to configure the artifact root or disable artifact declaration for a
> given call.
>
> **Actual**: the file is written successfully, then the turn fails:
> `declaring permissions: cortex tool write_to_file: convert tool call for
> permissions: model output error: invalid tool call error (invalid_args)
> <workspace>/foo.md is not a valid artifact path; artifacts must be in
> ~/.gemini/antigravity-cli/brain/<uuid>/`. Retrying the identical call with
> `ArtifactMetadata` omitted succeeds cleanly.

**Bug 2 — `tools:` allowlist does not restrict a `--agent`-selected
mainAgent's actual tool availability, though the CLI's own changelog
describes it as a supported frontmatter field is misleading** (unchanged
substance from round 1/2; round 3 adds the changelog cross-check). For a
`mainAgent: true` agent launched via `--agent`, `init.tools` still reports
the full default tool set and an explicitly-excluded tool (tested:
`write_to_file`) remains fully callable and executes. Repro: invocations
3–5 in "The documented custom-agent mitigation" above. Note for the report:
agy 1.1.15's own embedded changelog documents supported Markdown-agent
frontmatter fields as `mainAgent`, `subagent`, `hidden`, `inheritMcp`, and
`commandExecutionPolicy` — `tools:` is conspicuously absent from that list,
suggesting this may be a known internal limitation rather than a bug, but
Google's separate public Hooks/custom-agent documentation (per the primary
research report) does present `tools:` as a real, working allowlist
mechanism for custom agents generally, which is what makes this worth
filing: the two sources disagree about whether this is expected.

**Bug 3 — conversation-level `result.status`/`result.error` is sticky
after any `PreToolUse` hook denial or native artifact-path failure,
persisting across resumed turns *and within the same turn* once any call
in its history hit that failure:**

> **Title**: once a turn (or conversation) has had one denied/failed
> native-write call, `result.status` stays `"ERROR"` with that call's stale
> message even when a later call (same turn or a resumed turn) succeeds or
> uses no tools at all
>
> **Repro A (within one turn, round 3)**: prompt a model to write a
> deliverable via `write_to_file` with `ArtifactMetadata` to a path outside
> `--add-dir`'s workspace (see Bug 1's repro). The model retries the same
> call without `ArtifactMetadata` in the same turn and that retry succeeds
> (`step_update.state: "DONE"`, file correct on disk) — but the turn's
> `result.status` is still `"ERROR"`, `result.error` is still the first
> call's message.
>
> **Repro B (across resumed turns, round 2)**: start a conversation whose
> first turn takes a `PreToolUse` hook denial or native artifact-path
> failure. Resume it (`--conversation <id>`) with a turn that either
> succeeds at the same write (`ArtifactMetadata` corrected) or uses no
> tools at all ("Reply OK."). `result.status` is still `"ERROR"` with the
> **first** turn's stale message; `num_turns` keeps incrementing across
> resumes.
>
> **Expected**: `result.status` reflects the outcome of the calls actually
> made in *this* turn (or, for a resumed conversation, at minimum this
> turn's own actions), not a stale failure from an earlier call/turn.
>
> **Actual**: sticky at `"ERROR"` with the original message, both within a
> single turn (once any call in it failed) and across resumed turns.
>
> **Why it matters**: this defeats any repair pattern that resumes a
> conversation (or continues a turn) after a denied/failed native-write
> call and checks the eventual top-level `result.status` to know whether
> the repair worked — including `ae5edb6`'s original design (fixed in round
> 3 by never resuming the failed conversation for the repair attempt) and
> any naive same-turn retry that trusts the turn's own final status rather
> than each step's own outcome.

## History across rounds (why earlier framings in this worklog changed)

- **Round 1**: added the reactive repair-retry (`ae5edb6`, kept
  throughout); found the custom-agent `tools:` allowlist inert for
  `--agent`-selected mainAgents; concluded (correctly, per round 3's static
  corroboration) that Tractor should not provision a manifest.
- **Round 2**: shipped a global `PreToolUse` hook. Its first cut denied
  every native write unconditionally, which independent review correctly
  flagged as breaking legitimate in-brain artifact writes for any agy
  session sharing the global config. The fix in that same round made the
  hook allow/deny based on **target path alone** (inside vs. outside
  `artifactDirectoryPath`) — better, but still wrong: independent review's
  round-3 finding correctly noted this still denies plenty of legitimate
  plain workspace writes that have nothing to do with the bug, since path
  alone was never the actual trigger condition.
- **Round 3** (this revision): found the real trigger condition
  (`ArtifactMetadata` presence, not path) by capturing and diffing two
  real tool calls to the identical path that differed only in whether they
  carried it; rewrote the hook to gate on that; formally dropped the
  manifest/`--agent`/`init.tools` contract per an explicit supervisor
  design ruling (round 1's inertness finding, now corroborated
  statically); redesigned the repair-retry to use a fresh conversation
  instead of resuming a status-poisoned one; fixed the file-mode-widening
  and lock-boundary-overclaim issues in the hook's provisioning code; added
  a static prompt-preamble layer; and added a real, live, adapter-level
  (not just fake-subprocess) proof that the redesigned repair-retry
  recovers a genuine agy failure end to end.

## Related

- [[202608191356-agy-artifact-validator]] — the reactive repair-retry this
  work sits on top of (`ae5edb6`).
