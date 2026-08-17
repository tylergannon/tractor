# Adversarial review — live examples (steering, fan-out/fan-in), round 03

## Review target

The entire current work on worktree `examples-live-validation` — everything
since `12749f1`, now three commits:

- `1944160` — the two examples, their READMEs, `examples/examples_test.go`.
- `1e9a0c9` — timeline `ts` stamping with clone-before-stamp
  (`engine/store.go`), branch attribution on stage events
  (`engine/runner.go:482-488`, `engine/parallel.go:194`), example index and
  README corrections, removal of the vacuous in-branch isolation checks, and the
  first retained proof tree.
- `b365554` — "Preserve current live validation": a real `workdir` assertion in
  `engine/observability_test.go:95-101`, main-workspace no-leak assertions
  prepended to the `verify` node of `examples/parallel/fan-out-fan-in.json:50`,
  the round-02 reruns preserved as `head-steering/` and `head-parallel/`,
  a replay of the current `verify` command (`head-parallel/current-verify.*`),
  and rewritten `REPORT.md` / `verification.txt`.

Working tree is clean at `b365554`.

Authorities: `docs/spec.md` (§3.9, §4.8–4.9, §5.6, §10, §11), `lefthook.yml`
(default-config linters, no suppressions, full `go test ./...` on commit),
`skills/orchestrate-attractor-loops/SKILL.md` (live proof through the running
software; "Preserve commands, versions, exit status, outputs, and artifacts
needed to make the proof auditable"), and the conversation requirements restated
in round 01: token-conscious examples that are *actually run*, proving (a) an
operator can invoke a workflow, steer its live turn, and have the steering
received, and (b) observable parallel fan-out/fan-in; the report must support an
evaluation of the JSON workflow language and ongoing-run observability. The
manager node remains an implementation exclusion; its residual gap is reported
at the end.

Operating constraints honored: read-only apart from this artifact. No caller
instruction attempted to narrow the defects, files, or subject matter, and none
was applied; this is a fresh full review of the current state, though the
disposition of earlier rounds is recorded as evidence.

## Evidence inspected

- Full `git diff 12749f1..HEAD`, and `git diff 1e9a0c9..HEAD` for this round's
  changes; surrounding code re-read: `engine/store.go`, `engine/runner.go`
  (`executeWithRetry`, `stageEvent`), `engine/parallel.go`,
  `engine/control.go`, `harness/backend.go`, `harness/codex/adapter.go`.
- The whole retained proof tree, including the new `head-steering/`,
  `head-parallel/` and `head-parallel/current-verify.*`, plus the rewritten
  `REPORT.md` and `verification.txt`.
- Hygiene at HEAD: `go build ./...`, `go test ./engine ./examples` (ok),
  `golangci-lint run ./...` → `0 issues`, `goimports -l .` → clean.
- **Independent live exercise of HEAD** (binary built from `b365554`):
  - Committed parallel example end-to-end, workspace prepared exactly as
    `examples/parallel/README.md` documents: run
    `acd54598e975fa8d7e8af5825d02efcb`, exit `0`, `COMPLETED`, verify tool log
    `parallel fan-in verified` — i.e. the newly added
    `test ! -e parallel-left.txt; test ! -e parallel-right.txt` assertions hold
    in a real run, not only in a replay.
  - Zero-token control-surface conformance probes against §3.9:
    steering while a `tool` node is the active top-level execution →
    `409` with a zero-byte body and **no** `steering.jsonl` written;
    steering while `fan_out` is active (both branch tools sleeping, run then
    interrupted) → `409`, again with no audit record, i.e. rejected by the
    engine before backend handoff as §3.9 requires;
    `[]` parts → `400`; `POST /events/lifecycle` → `404`.
  - Round-02's own reruns at `1e9a0c9` (steering
    `810b16f9be8d97f0caba33324b9ade15`, parallel
    `b6bd850707a383ab30c543df8f49978f`) are the artifacts now committed under
    `head-*`; their content matches what I produced.

All round-02 findings are addressed: the retained proof now describes the
current examples honestly and attributes the reruns to the independent reviewer;
the `workdir` assertion now type-asserts and compares the worktree basename, so
deleting `event["workdir"] = workdir` would fail the test; the deterministic
no-leak backstop exists and passes live. The nil-map guard was declined with a
recorded rationale (`ephemeral/worklog/202608170739-live-examples.md:7`) — every
internal call constructs a literal, so I accept the decline.

## Findings

### 1. The current-commit proof drops the invocation, exit status and binary provenance it is supposed to preserve (issue)

`skills/orchestrate-attractor-loops/SKILL.md` ("Validate independently", item 3)
requires preserving "commands, versions, exit status, outputs, and artifacts
needed to make the proof auditable", and the earlier trees do exactly that:
`live-examples/parallel/` and `live-examples/steering/` each carry
`invocation.txt` and `exit-status.txt` (and `pid.txt` for the steering run). The
new trees — the ones that exist specifically to prove the *committed* artifacts —
do not:

```
head-parallel/   current-verify.{exit-status,stdout,stderr}.txt logs stderr.txt stdout.txt workspace
head-steering/   body.txt logs status.txt stderr.txt stdout.txt workspace
```

No `invocation.txt`, no `exit-status.txt`, and nothing recording which binary or
commit produced them; `REPORT.md:4-7` asserts the reviewer "rebuilt Tractor at
`1e9a0c9`" in prose only. The replayed verify, by contrast, does keep its exit
status (`head-parallel/current-verify.exit-status.txt`), which shows the
convention was available and simply not applied to the runs that matter most.

Impact: process exit status is the one claim in `REPORT.md`'s bullet lists
("Tractor exit status: `0`") that has no primary artifact behind it for the
current-commit runs; a later auditor must take the prose on trust or infer it
from `stdout.txt` plus the terminal checkpoint. This costs zero tokens to fix —
the information existed at capture time and was discarded. It is also the second
consecutive round in which the proof tree, rather than the software, is what
falls short.

### 2. The committed parallel graph has still never been run end-to-end by the author (nitpick)

`b365554` edited `examples/parallel/fan-out-fan-in.json:50` (the `verify`
command gained the two no-leak assertions) *after* the retained
`head-parallel/` run was captured at `1e9a0c9`. The mitigation —
replaying the exact new verify command over the retained workspace
(`head-parallel/current-verify.*`, exit `0`) — is a sensible token-conscious
choice and is described accurately in `REPORT.md:51-54`. Its weakness is that
`head-parallel/workspace/` contains only `tractor-fan-in-proof/`, so
`test ! -e parallel-left.txt` cannot fail there under any implementation: the
replay confirms the command's syntax and the final-state property, not that a
real fan-out leaves the main workspace clean. I closed that gap myself (run
`acd54598e975fa8d7e8af5825d02efcb` above, real fan-out, assertions passed), so
the property does hold at HEAD; the residual is evidentiary.

Worth naming as a pattern rather than as a one-off: each of the last two rounds
made a small, obviously-safe edit to an already-proven example, and each time
the retained proof silently stopped describing the committed artifact. Either
re-run on every example edit, or state the freshness rule in `REPORT.md` so a
reader knows which retained run corresponds to which commit.

### 3. The run directory never records the main workspace path (nitpick)

`runManifest` (`engine/control.go:19-25`) advertises `id`, `name`, `goal`,
`started_at`, `control_socket`. Branch worktrees are now well covered —
`worktrees.jsonl` at creation time and `branch`/`workdir` on every branch stage
event — but the top-level `--workdir` appears nowhere in `{logs_root}`:
confirmed on my HEAD run, where the only occurrences of the string in
`manifest.json`/`checkpoint.json` come from the pipeline's own `goal` text and
stage prose. An external supervisor that attaches to a live run (the operating
model spec §3.9 and §6.2 prescribe) can therefore locate the engine-owned
worktrees but not the workspace the run is actually mutating, and cannot tell
two concurrent runs of the same pipeline apart by target. Spec §5.6 does not
require it; one field next to `control_socket` would close it.

### 4. The new `workdir` assertion is coupled to worktree naming and ordering (nitpick)

`engine/observability_test.go:97-99` maps `left → branch-001`,
`right → branch-002` and compares `filepath.Base(workdir)`. This is a real
assertion now (round-02's defect is fixed), but it encodes two incidental facts:
that branch worktree directories are named `branch-%03d`, and that `left` is
allocated before `right`. A future change to worktree naming, or any reordering
of the offered edges, fails this test for a reason unrelated to attribution.
Asserting `workdir` is non-empty *and present* and that the two branch events
carry **different** workdirs would test the property (attribution to distinct
worktrees) without pinning the naming scheme.

### 5. `examples/` is unreachable from the repository's front door (nitpick)

`README.md` is still the single line "Tractor is a Go module." Nothing in the
repo root points at `examples/`, which is now the only user-facing
demonstration of the two headline capabilities and the only place the operator
procedure (wait for the marker, read `control_socket` from `manifest.json`,
`POST /steer`) is written down. Three lines in the root README would make the
work discoverable to the audience it was built for.

## Evaluation notes requested by the caller

**JSON workflow language.** Re-confirmed against the current examples; the
assessment is stable across rounds. The language does the simple things well —
`defaults` removes provider/model/effort/timeout repetition, single-successor
nodes need no `condition`, and `examples/examples_test.go` gives every committed
example a parse + lint gate. Two structural costs remain visible in these very
files: (a) every `parallel` block must converge on a `parallel.fan_in`, which is
an LLM node by definition (spec §2.4, §4.9; lint `parallel_fan_in`), so the
example must spend a real Codex turn to join two zero-token tool branches — a
purely deterministic fan-out is unexpressible; (b) JSON's lack of multi-line
strings and comments concentrates authoring risk in `tool` nodes — the `verify`
node is now a ~650-character single-line shell program, and this round's edit
consisted of prepending two asserts inside that string, invisible to any reader
who does not diff character-by-character. The rationale for that edit lives in
the worklog, which is the right place but not where an example's reader looks.

**Ongoing-run observability.** The strongest it has been. Verified live at HEAD:
`timeline.jsonl` is appended per event and every event carries an RFC 3339 nano
`ts`; branch stage events carry `branch` + `workdir` for `StageStarted`,
`StageCompleted`, `StageFailed` and `StageRetrying`, so a supervisor tailing the
file can attribute stages of multi-node branches and branch retries without
waiting for `branches.json`; `manifest.json` advertises `control_socket` before
the first stage (with the `/var/folders` fallback for >100-byte socket paths);
`worktrees.jsonl`, `events/index.jsonl` and `current.jsonl` are all written as
the run proceeds; `steering.jsonl` lands in the active stage at accept time and,
as my probes confirm, *only* for accepted requests. Remaining gaps, all
spec-permitted MAYs or omissions: no SSE projections
(`GET /events/lifecycle` returns 404), so remote supervision still requires
filesystem access to the run directory; stage events carry no harness/model/
session, so an observer sees that a stage runs but not what is running it; and
the main workdir is unrecorded (Finding 3).

## Residual gap (from the excluded manager node)

The in-graph manager node remains omitted by user decision and excluded from
this task; not re-litigated. Current consequence: the operator half of a manager
loop is proven and now independently reproduced three times (invoke → observe
durable surfaces → `POST /steer` → steering received and acted on), and the
rejection semantics a manager must handle are confirmed at HEAD (409 with no
audit while a tool node or a fan-out is active, 400 on empty parts). What a
manager driving a child pipeline would still lack is remote observation (no SSE)
and turn-identity fields on stage events; both become blocking if a typed
manager node is restored later.

## Outcome

`material findings remain`
