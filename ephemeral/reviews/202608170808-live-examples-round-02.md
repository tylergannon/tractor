# Adversarial review — live examples (steering, fan-out/fan-in), round 02

## Review target

The entire current work on worktree `examples-live-validation`, i.e. everything
since `12749f1`:

- `1944160` — "Add live steering and fan-in examples": `examples/README.md`,
  `examples/examples_test.go`, `examples/steering/*`, `examples/parallel/*`,
  worklog.
- `1e9a0c9` — "Prove live orchestration examples": timeline `ts` stamping and
  clone-before-stamp (`engine/store.go`), branch attribution on stage events
  (`engine/runner.go`, `engine/parallel.go`, `engine/runner_test.go`,
  `engine/observability_test.go`), example index/README corrections, removal of
  the in-branch isolation checks from `examples/parallel/fan-out-fan-in.json`,
  the retained proof tree `ephemeral/projects/tractor/live-examples/`, worklog,
  and round-01's review artifact.
- Working tree is clean at `1e9a0c9`; no uncommitted changes remain.

Authorities: `docs/spec.md` (§3.9, §4.8–4.9, §5.6, §10, §11), `lefthook.yml`
(default-config linters, no suppressions), `skills/orchestrate-attractor-loops/SKILL.md`
(live proof through the running software; preserve commands, exit status,
outputs and artifacts so the proof is auditable), and the conversation
requirements restated by the caller in round 01: token-conscious examples that
are *actually run*, proving (a) an operator can invoke a workflow, steer its
live turn, and have the steering received, and (b) observable parallel
fan-out/fan-in; the report must also support an evaluation of the JSON workflow
language and ongoing-run observability. The manager node remains an
implementation exclusion; its residual gap is still reported below.

Operating constraints honored: read-only apart from this artifact; nothing but
`ephemeral/reviews/202608170808-live-examples-round-02.md` was written. No
caller instruction narrowed the subject matter, and none was applied. This is a
fresh full review, not a check of round-01's fixes — though their disposition is
recorded because it is evidence about the current state.

## Evidence inspected

- `git diff 12749f1..HEAD` in full, and `git diff 1944160..HEAD` for the new
  engine changes; surrounding code: `engine/store.go`, `engine/runner.go`
  (`executeWithRetry`, `stageEvent`), `engine/parallel.go` (`walkBranch`,
  `runBranches`, `attributeBranchSegments`), `engine/control.go`
  (`serveControl`, active-execution gating), `harness/backend.go` (`Steer`),
  `harness/codex/adapter.go` (`Steer`, 5s `controlTimeout`).
- Retained proof tree: `REPORT.md`, `verification.txt`, both run directories
  (`timeline.jsonl`, `manifest.json`, `checkpoint.json`, `worktrees.jsonl`,
  `events/index.jsonl` + segments, per-stage `steering.jsonl`,
  `branches.json`, `tool.log`, `prompt.md`/`response.md`), the HTTP
  status/body captures, and `timestamp-smoke/`.
- Repo hygiene at HEAD: `go build ./...`, `go test ./...` (all packages ok),
  `golangci-lint run ./...` → `0 issues`, `goimports -l .` → clean.
- **Independent live re-runs of the committed examples at HEAD** (binary built
  from `1e9a0c9`, evidence kept in `/tmp/r2-par`, `/tmp/r2-steer`; not
  committed, since this pass is read-only):
  - `fan-out-fan-in.json`, workspace prepared exactly as
    `examples/parallel/README.md` now documents (`git init` + empty base
    commit): run `b6bd850707a383ab30c543df8f49978f`, exit `0`, `COMPLETED`.
    All 29 timeline events carry RFC 3339 `ts`; both branch stage events carry
    `"branch":"left"/"right"` and distinct `"workdir"` values under
    `logs/worktrees-883991409/branch-00{1,2}`, emitted *before* the fan-in
    started. The fan-in segment shows the agent listing both worktrees,
    asserting each contains only its own `parallel-*.txt`, hashing sources
    against copies, and writing `summary.txt` last;
    `tractor-fan-in-proof/summary.txt` = `isolation=verified` /
    `overlap=verified`; deterministic `verify` passed; workspace left with no
    stray worktrees, commits, or stashes.
  - `external-steering.json`: run `810b16f9be8d97f0caba33324b9ade15`,
    `POST /steer` → `200` with a zero-byte body, `steering-received.txt` =
    `TRACTOR_STEERING_RECEIVED`, `stages/latest/work/steering.jsonl` recorded
    the instruction, `verify` passed, exit `0`, `COMPLETED`.

Both headline capabilities therefore hold at HEAD, and all five round-01
findings are genuinely fixed in the software and documentation: the example
index now describes tool branches plus a Codex fan-in; the parallel README
documents the commit precondition (verified by following it verbatim); branch
stage events carry `branch`/`workdir`; `appendTimeline` clones and preserves a
caller-supplied `ts`; the vacuous in-branch isolation checks are gone.

## Findings

### 1. The retained proof no longer matches the committed examples or engine (issue)

`ephemeral/projects/tractor/live-examples/REPORT.md:3-6` states: "The example
definitions were committed at `1944160`. Both real runs used a binary built from
that commit. The subsequent lifecycle-timestamp change only adds `ts` while
appending timeline events". At HEAD neither sentence holds:

- the example definitions changed after those runs —
  `examples/parallel/fan-out-fan-in.json` had both branch commands rewritten in
  `1e9a0c9` (isolation asserts removed, output text changed). The retained
  `parallel/logs/stages/000004-left/tool.log` reads `left isolated`; the
  committed example can only print `left completed`. The retained
  `branches.json` notes likewise read `exit 0: left isolated`;
- the follow-up commit did **not** only add `ts`: it also added `branch` and
  `workdir` to stage events (`engine/runner.go:482-488`). The retained
  `parallel/logs/timeline.jsonl` contains zero `ts` fields and no branch
  attribution at all (`grep -c '"ts"'` → 0; branch stage lines are
  `{"index":4,"name":"left","type":"StageStarted"}`);
- `verification.txt:9` still asserts "PASS branch-owned intervals overlap and
  **branch isolation checks passed**" — for checks that no longer exist in the
  committed graph;
- the only current-binary evidence, `timestamp-smoke/`, is a two-stage
  `start → check → exit` pipeline with no parallel node, so it proves the `ts`
  stamp and nothing about branch attribution. The round-01 fix that the parallel
  README now advertises as a success criterion ("live branch stage events
  identify their branch and worktree before fan-in",
  `examples/parallel/README.md:33`) is backed only by a unit test.

Impact: the deliverable's central claim is "examples that were *actually run*",
and `skills/orchestrate-attractor-loops` requires preserved, auditable proof of
the running software; an auditor reading the committed proof tree is told it
covers artifacts it does not cover, and two of its statements are now false of
the repository. The capability is fine — I re-ran both committed examples at
HEAD and they pass (evidence above) — so this is an evidence-integrity defect,
not a functional one, but it is the difference between a proof and a stale
claim. Re-running both examples with the HEAD binary and rewriting
`REPORT.md`/`verification.txt` against those runs closes it; the parallel re-run
now also carries the branch-attribution evidence for free.

### 2. The new branch-attribution assertion cannot fail for a missing `workdir` (nitpick)

`engine/observability_test.go:76-78`:

```go
if event["branch"] != name || event["workdir"] == "" {
```

`events` are decoded into `map[string]any`, so a *missing* `workdir` key yields
`nil`, and `nil == ""` is false — the assertion passes. Deleting
`event["workdir"] = workdir` from `stageEvent` (`engine/runner.go:482-488`)
would therefore not fail this test; only an explicitly empty string would. The
`branch` half is sound (`nil != "left"`). This is the same shape of defect the
round-01 pass removed from the example graph — an assertion that cannot fail —
so it deserves the same treatment: assert the key is present and equals the
expected worktree path (the test already knows the branch worktree root).

### 3. Isolation lost its deterministic backstop when the vacuous checks were removed (nitpick)

Removing `test ! -e parallel-right.txt` from the branches was correct — those
could never fail. But nothing deterministic replaced them: at HEAD the only
in-graph isolation statement is `summary.txt`'s `isolation=verified`, written by
the fan-in model, and `verify` (`fan-out-fan-in.json:52`) merely greps that line
back. Model-attested proof is exactly what the worklog's own decision line ("Live
examples must be self-verifying") warns against. A free, genuinely deterministic
check is available in the same node: after `prepare` deletes them and the
branches write only inside their worktrees, the main workspace must still
contain no `parallel-left.txt` / `parallel-right.txt` at `verify` time — that
assertion fails loudly if branch writes ever leak into the shared workspace,
which is the property the example exists to demonstrate. (My HEAD re-run
confirms the property holds today: `ls` of the workspace shows only
`tractor-fan-in-proof/`.)

### 4. `appendTimeline` will panic on a nil event map (nitpick)

`engine/store.go:98-101` now does `stamped := maps.Clone(event)` and then
assigns `stamped["ts"]`. `maps.Clone(nil)` returns a nil map, and assignment to
a nil map panics. No current caller passes a nil `timelineEvent` — every call
site builds a literal — so this is unreachable today and the clone itself is a
correct response to round-01's mutation finding. It is worth one line
(`if stamped == nil { stamped = timelineEvent{} }`, or stamping into a fresh map)
because the panic would surface inside a store method that all lifecycle events
funnel through, taking the run down at an arbitrary point rather than returning
the error the signature promises.

## Evaluation notes requested by the caller

**JSON workflow language.** Unchanged in substance from round 01, now
re-confirmed against the edited examples. Strengths: `defaults` genuinely
removed provider/model/effort/timeout repetition; single-successor nodes need no
`condition`; `examples/examples_test.go` gives every committed example a
parse + lint gate, which caught nothing here only because the examples are
correct. Constraints the examples make concrete:

- every `parallel` block must converge on a `parallel.fan_in`, which is an LLM
  node by definition (spec §2.4, §4.9; lint `parallel_fan_in`,
  `fan_in_single_parallel`), so a fully deterministic fan-out is impossible —
  this example spends a real Codex turn purely to join two zero-token tool
  branches. Spec-mandated, not an implementation defect, but it is the dominant
  cost property of the language;
- JSON's lack of multi-line strings and comments concentrates authoring risk in
  `tool` nodes: `verify` is a ~600-character single-line shell program whose
  embedded `\n` escapes become literal newlines in the executed command — the
  edit in `1e9a0c9` had to be made blind, inside a JSON string, with no way to
  comment why the isolation asserts were dropped. The worklog carries that
  rationale instead, which is the right place but not a place a reader of the
  example will look.

**Ongoing-run observability.** Materially better than at round 01, verified
live: `timeline.jsonl` is appended per event, every event carries RFC 3339 nano
`ts`, and branch stage events now carry `branch` + `workdir`, so a supervisor
tailing the file during a fan-out can attribute every stage — including stages
of multi-node branches and branch retries (`stageEvent` is applied to
`StageStarted`, `StageCompleted`, `StageFailed`, `StageRetrying`) — without
waiting for `branches.json`. `manifest.json` advertises `control_socket` before
the first stage (with the `/var/folders` fallback for >100-byte paths,
`engine/control.go:42-49`), `worktrees.jsonl` is written as worktrees are
created, `events/index.jsonl` + `current.jsonl` point at live native segments,
and `steering.jsonl` lands in the active stage at accept time. Remaining gaps,
both spec-permitted MAYs: no SSE projections (`/events/lifecycle`,
`/events/detail`), so remote supervision still requires filesystem access to the
run directory; and stage events still omit harness/model/session, so an observer
sees *that* a stage runs, not *what* is running it.

## Residual gap (from the excluded manager node)

The in-graph manager node remains omitted by user decision and was excluded from
this task; not re-litigated. Consequence, recorded: the operator half of a
manager loop is now proven end-to-end (invoke → observe run surfaces →
`POST /steer` → steering received and acted on, reproduced twice
independently), and the observation half is materially stronger after the branch
attribution fix. What a co-process manager still lacks is remote observation
(no SSE) and turn-identity fields on stage events; both become blocking if a
manager node is later restored as a typed node driving a child pipeline.

## Outcome

`material findings remain`
