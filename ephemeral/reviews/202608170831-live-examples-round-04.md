# Adversarial review — live examples (steering, fan-out/fan-in), round 04

## Review target

The entire current work on worktree `examples-live-validation`, i.e. everything
since `12749f1`. Two commits are new since round 03:

- `dbbf5cb` "Expose run workspace in observability" — `runManifest.Workdir`
  (`engine/control.go:23,102`) plus its manifest assertion
  (`engine/observability_test.go:171-172`), the branch-attribution assertion
  decoupled from worktree naming and strengthened to require two distinct,
  stable workdirs (`engine/observability_test.go:88,100-111`), a root
  `README.md` pointer to `examples/`, worklog, and round-03's artifact.
- `347e683` "Record frozen live example proof" — a new
  `ephemeral/projects/tractor/live-examples/final/` tree (provenance, both
  runs' invocation/PID/exit status/stdout/stderr/logs/workspace, plus
  `parallel/live-observation.jsonl`) and the corresponding `REPORT.md` /
  `verification.txt` sections.

Working tree is clean at `347e683`.

Authorities: `docs/spec.md` (§3.9, §4.8–4.9, §5.6, §10, §11), `lefthook.yml`,
`skills/orchestrate-attractor-loops/SKILL.md`, and the conversation
requirements restated in round 01 (token-conscious examples actually run,
proving live steering of a Codex turn and observable parallel fan-out/fan-in;
the report must support an evaluation of the JSON workflow language and
ongoing-run observability). The manager node remains an implementation
exclusion; its residual gap is recorded at the end.

Operating constraints honored: read-only apart from this artifact. No caller
instruction attempted to narrow defects, files, or subject matter, and none was
applied.

## Evidence inspected

- `git diff b365554..HEAD` in full and the surrounding code it touches:
  `engine/control.go`, `engine/observability_test.go`, plus a re-read of
  `engine/git_workspace.go` (freeze, `workdirRel` mapping, worktree creation and
  cleanup), `engine/parallel.go`, `engine/runner.go`, `harness/backend.go`.
- The whole `final/` proof tree: `provenance.txt`, both `invocation.txt` /
  `exit-status.txt` / `pid.txt` / `stdout.txt`, both `manifest.json`,
  `timeline.jsonl`, `worktrees.jsonl`, `branches.json`, per-stage
  `outcome.json` / `tool.log` / `prompt.md` / `response.md` /
  `steering.jsonl`, the native event segments, `current.jsonl` and
  `stages/latest/*` symlink targets, `checkpoint.json`, and both workspaces.
- Hygiene at HEAD: `go build ./...`, `go test ./...` (all packages ok),
  `golangci-lint run ./...` → `0 issues`, `goimports -l .` → clean.
- **Independent verification at HEAD:**
  - Provenance reproduced. `go build -o /tmp/tractor-r4-head ./cmd/tractor` from
    this tree yields sha256
    `bcd629f54dab28024ff6c03f88b28d9939ed130c20002ff6ab8c4ce868ae8370`, byte-
    identical to `final/provenance.txt`'s recorded binary, and
    `git diff dbbf5cb..HEAD -- '*.go' 'go.mod' 'go.sum'` is empty — so the
    frozen-implementation claim is independently checkable, not prose. Round-03
    finding 1 is fully closed.
  - Zero-token control-surface probes with a live `tool` node
    (`/tmp/r4`, `probe.json`): manifest now advertises an absolute
    `"workdir": "/tmp/r4/ws"` even though `--workdir ws` was relative;
    `POST /steer` with a valid part array while no turn is live → `409`,
    zero-byte body, and **no** `steering.jsonl` anywhere in the run directory;
    `[]` → `400`; a `{"parts": […]}` object → `400` (the spec's wire format at
    §3.9 is a bare array, which is what `examples/steering/README.md` documents);
    `GET /steer` → `405`; `POST /events/lifecycle` → `404`. Every event in the
    probe's `timeline.jsonl` carries a `ts`.
  - Retained fan-out evidence cross-checked against the engine: `branches.json`
    and `worktrees.jsonl` record two distinct worktrees, both
    `ParallelBranchStarted` and both attributed `StageStarted` events precede
    either completion by ~4s, and the engine-owned `worktrees-*` directory is
    absent from the final `logs/`, corroborating cleanup at finalization.

All four round-03 findings are addressed: full process evidence and reproducible
provenance now exist for the frozen runs; the committed graph was run end-to-end
at the frozen commit (`399dfc6a51c5b61effd966b1aceb95f5`, exit `0`, verify log
`parallel fan-in verified`); `manifest.workdir` is recorded; the branch test no
longer pins `branch-001`/`branch-002`; the root README links `examples/`.
Both headline capabilities hold, and the software itself is in good shape — the
findings below are one evidentiary gap and three small quality points.

## Findings

### 1. The frozen parallel workspace was altered after the run: its Git repository is gone (issue)

`REPORT.md:6-8` says the final directories retain "the literal invocation, PID,
process exit status, stdout/stderr, complete run directory, and resulting
workspace". The parallel workspace is not the state the run left:

- the run's own evidence proves the workspace was itself a Git repository. In
  `final/parallel/logs/stages/000003-fan_out/branches.json` each branch
  `workdir` is exactly the worktree root (`…/worktrees-1541366887/branch-001`)
  with no relative suffix. `createBranchWorktreesWithStop` builds that path as
  `filepath.Join(path, snapshot.workdirRel)` (`engine/git_workspace.go:125`) and
  `workdirRel` is `filepath.Rel(repoRoot, resolvedWorkdir)`
  (`engine/git_workspace.go:57-60`), so `workdirRel` was `"."`, i.e.
  `git rev-parse --show-toplevel` from the workspace returned the workspace —
  as `examples/parallel/README.md:9-14` requires;
- but `ls -a
  ephemeral/projects/tractor/live-examples/final/parallel/workspace` now shows
  only `tractor-fan-in-proof/`. The `.git` directory was removed before the tree
  was committed (had it still been there, `git add` would have recorded a
  gitlink instead of the three files that appear in `347e683`).

Impact: the one artifact that could show the documented precondition was
satisfied, and that the freeze/worktree machinery left the workspace repository
clean (no stray commits, branches, worktree registrations, or stashes), was
deleted from the frozen proof without being noted anywhere in `REPORT.md` or
`verification.txt` — while `verification.txt:12` continues to assert
"completed-run finalization removed the engine-owned worktrees" and
`verification.txt:16` asserts the final parallel run "retained … workspace
effects". `skills/orchestrate-attractor-loops` requires preserving the artifacts
that make the proof auditable; a deliberately post-processed workspace is the
one thing a freeze-and-worktree proof must not quietly do. The capability is not
in doubt — my own runs at `b365554` and the retained logs both show clean
finalization — so this is evidence integrity, now the third consecutive round in
which the proof tree, not the software, is the weak link. Either retain the
workspace repository as-is, or state in `REPORT.md` that `.git` was removed for
committing and record `git status` / `git worktree list` / `git log --oneline`
output taken immediately after the run instead.

### 2. `live-observation.jsonl` carries no capture-time provenance (nitpick)

`REPORT.md:19-22` makes the round's strongest observability claim — the file
"was captured while both branches were still running" — but
`final/parallel/live-observation.jsonl` is byte-identical to lines 11–14 of
`final/parallel/logs/timeline.jsonl`, with no invocation record, no capture
timestamp, and no surrounding command, unlike every other artifact in `final/`
(which now carries `invocation.txt`). It is therefore indistinguishable from a
post-run `head`, and it proves the property it is named for only if the reader
trusts the prose. One `date -u` stamp beside it, or the capturing command in an
`invocation.txt`, would make it self-evidencing at zero cost. (The property
itself is sound: my own HEAD-side runs read attributed branch stage events from
a live `timeline.jsonl` before fan-in.)

### 3. The example's only LLM stage habitually runs at ~55% of its timeout (nitpick)

`examples/parallel/fan-out-fan-in.json:5` sets a single `defaults.timeout` of
`90s`, which the `combine` fan-in inherits. Across every retained and
independent run the fan-in took `47.3s`, `50.2s`, and `54.1s` — a margin under
1.7×, on a low-effort turn whose latency depends on a hosted model. When that
margin is exceeded the flagship example fails with a timeout on the one node
that cannot be made deterministic, and the failure looks like a broken example
rather than a slow day. Since the deterministic stages need nothing close to
90s, a per-node timeout on `combine` (or a larger default) would make the
self-verifying example robust without weakening any assertion.

### 4. Durable run state embeds a raw NUL byte in a JSON object key (nitpick)

`final/parallel/logs/checkpoint.json:37` records the session binding under
`" none:combine"`, from `noneThreadPrefix = "\x00none:"`
(`harness/backend.go:15`). The prefix is deliberate and predates this branch,
but §5.6/§10 make `checkpoint.json` part of the surface external supervisors
read, and a control character in a key is hostile there: it survives no
round-trip through shell, `grep`, most log pipelines, or any consumer with
C-string semantics, and it renders as an escape no operator can type. A
printable sentinel that cannot collide with a node id (the ids are already
schema-constrained) would carry the same meaning.

## Evaluation notes requested by the caller

**JSON workflow language.** Stable across four rounds. It handles the easy parts
well — `defaults` collapses provider/model/effort/timeout repetition,
single-successor nodes need no `condition`, and `examples/examples_test.go`
parses and lints every committed example. The two structural costs both remain
visible in these files: (a) a `parallel` block must converge on a
`parallel.fan_in`, which is an LLM node by definition (spec §2.4, §4.9), so this
example must spend a real Codex turn to join two zero-token tool branches, and
that turn is also the example's only source of latency risk (Finding 3); (b) no
multi-line strings and no comments means non-trivial `tool` nodes become long
single-line shell programs — `verify` is now a ~700-character line encoding nine
distinct assertions, and only the worklog explains why any of them are there.
`defaults` is also all-or-nothing per file: there is no way to express "90s for
tools, 5m for the model turn" without repeating the field on the node, which is
exactly the ergonomic pressure that produced Finding 3.

**Ongoing-run observability.** Best state so far, all verified live at HEAD:
every `timeline.jsonl` event carries an RFC 3339 nano `ts`; branch stage events
carry `branch` + `workdir` on `StageStarted`/`Completed`/`Failed`/`Retrying`,
emitted before fan-in, so a supervisor can attribute concurrent stages without
waiting for `branches.json`; `manifest.json` now advertises the absolute main
`workdir` alongside `control_socket`, closing round-03's gap and letting an
external operator identify what a run is mutating; `worktrees.jsonl`,
`events/index.jsonl`, `current.jsonl`, and the `stages/latest/*` relative
symlinks all track the run as it proceeds; `steering.jsonl` is written only for
accepted requests, and rejections are the status code alone, as §3.9 permits.
Remaining gaps, all spec-permitted: no SSE projections (`/events/lifecycle` →
`404`), so remote supervision still requires filesystem access to the run
directory; stage events still omit harness/model/session, so an observer sees
that a stage runs but not what runs it — the session binding exists in
`checkpoint.json` but only under the key of Finding 4; and rejected steering
leaves no optional `timeline.jsonl` note, so a supervisor's failed attempts are
invisible in the run's own record.

## Residual gap (from the excluded manager node)

The in-graph manager node remains omitted by user decision and excluded from
this task; not re-litigated. Recorded consequence, unchanged: the operator half
of a manager loop is proven and has now been reproduced independently in four
rounds (invoke → observe durable surfaces → `POST /steer` → steering received
and acted on), and the rejection semantics a manager must handle are confirmed
at HEAD (`409` with no audit for no live target and for an active fan-out,
`400` for an empty or malformed part array, `405`/`404` off the endpoint). What
a manager driving a child pipeline would still lack is remote observation (no
SSE) and turn-identity fields on stage events; both become blocking if a typed
manager node is restored later.

## Outcome

`material findings remain`
