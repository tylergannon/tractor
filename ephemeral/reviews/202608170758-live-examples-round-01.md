# Adversarial review — live examples (steering, fan-out/fan-in), round 01

## Review target

The work since `12749f1` on branch/worktree `examples-live-validation`:

- committed at `1944160` ("Add live steering and fan-in examples"):
  `examples/README.md`, `examples/examples_test.go`,
  `examples/steering/{README.md,external-steering.json}`,
  `examples/parallel/{README.md,fan-out-fan-in.json}`,
  `ephemeral/worklog/202608170739-live-examples.md`;
- uncommitted working-tree changes: `engine/store.go` (RFC 3339 `ts` stamped on
  every timeline event), `engine/observability_test.go` (asserts the stamp),
  worklog friction line;
- untracked retained proof: `ephemeral/projects/tractor/live-examples/`
  (`REPORT.md`, `verification.txt`, `steering/`, `parallel/`,
  `timestamp-smoke/`).

Authorities used: `docs/spec.md` (Sections 3.9, 4.8–4.9, 5.6, 10, 11),
`lefthook.yml` (default-config linters, no suppressions),
`skills/orchestrate-attractor-loops/SKILL.md` (live proof over tests), and the
conversation requirements restated by the caller: token-conscious examples that
are *actually run*, proving (a) an operator can invoke a workflow, steer its
live turn, and have the steering received, and (b) observable parallel
fan-out/fan-in; report must also support an evaluation of the JSON workflow
language and ongoing-run observability.

Constraints honored: read-only apart from this artifact. The caller's "fix
discovered bugs" instruction is incompatible with a read-only review pass; no
implementation, test, doc, or config file was modified — the findings below are
reported for the implementer instead. The manager-node exclusion is treated as
an implementation constraint; the gap it leaves is still reported (see
"Residual gap"). No scope narrowing was requested or applied.

## Evidence inspected

- Full diff `12749f1..HEAD` plus `git diff` of the uncommitted engine change.
- Surrounding implementation: `engine/store.go`, `engine/control.go`,
  `engine/runner.go`, `engine/parallel.go`, `harness/backend.go` (`Steer`),
  `harness/codex/adapter.go` (`Steer`, `controlTimeout`).
- Retained proof: both run trees (`timeline.jsonl`, `manifest.json`,
  `checkpoint.json`, `worktrees.jsonl`, `events/index.jsonl`, per-stage
  `steering.jsonl`/`branches.json`/`tool.log`, native event segments), the HTTP
  status/body captures, and `REPORT.md`/`verification.txt` claims.
- Repo hygiene: `go build ./...`, `go vet ./...`, `go test ./...` (all pass),
  `golangci-lint run ./...` → 0 issues, `goimports -l .` → clean.
  (`modernize` flags only pre-existing generated `harness/codex/schema/types_gen.go`.)
- **Independent live re-runs by this review** (binary built from the current
  working tree, `/tmp/tractor-review`):
  - Steering example, following `examples/steering/README.md` verbatim:
    run `02ceb6af9cba4e418a2938edf7cb0dd2`, `POST /steer` → `200` with empty
    body, workspace gained `steering-received.txt` = `TRACTOR_STEERING_RECEIVED`,
    `stages/latest/work/steering.jsonl` recorded the instruction, the native
    segment contained the injected `user` part plus the resulting file change,
    `verify` passed, process exited `0` printing `COMPLETED`.
  - Parallel example: first attempt following `examples/parallel/README.md`
    verbatim **failed** (Finding 2); after adding one commit, run
    `b41a86e9ddf97aa8e07b9e96c37670b4` exited `0`, produced
    `tractor-fan-in-proof/{left,right,summary}.txt` with
    `isolation=verified` / `overlap=verified`, timeline showed both
    `ParallelBranchStarted` before either `ParallelBranchCompleted`, and the
    workspace was left clean (`git worktree list` shows only the main
    workspace; no stray commits or stashes).

So the two headline claims of the task are genuinely demonstrated and
independently reproducible: a live Codex turn received external steering and
acted on it, and fan-out/fan-in ran with observable, isolated, overlapping
branches. The findings below are defects around that core, not refutations of it.

## Findings

### 1. `examples/README.md` misdescribes the parallel example (issue)

`examples/README.md:11-13` states the example "proves that two Claude turns
execute in isolated Git worktrees and that a fan-in turn reads both products
into the main workspace." The committed graph does nothing of the sort:
`examples/parallel/fan-out-fan-in.json` branches `left`/`right` are `tool`
nodes (lines 26–41) and the only agent turn is the fan-in, pinned to
`"llm_provider": "openai"`, `"gpt-5.6-sol"` (lines 47–49). No Claude turn runs
anywhere in `examples/`. `examples/parallel/README.md` describes the graph
correctly, so the index and the example contradict each other.

Impact: the entry-point document asserts the strongest and most expensive
property (an *agent* turn confined to an engine-owned worktree) as proven by
these examples. It is not proven here — the only live evidence for
agent-in-worktree branches is the earlier, untracked
`ephemeral/projects/tractor/orchestration-live/REPORT.md` run. A reader
evaluating the system from `examples/` will over-credit the committed proof.

### 2. The documented parallel setup fails on a fresh disposable repository (issue, reproduced)

`examples/parallel/README.md:7-13` says only: "The workspace must be a
disposable Git repository", then gives the `tractor run` command. Following
that literally:

```sh
mkdir -p /tmp/rv-par/workspace && git -C /tmp/rv-par/workspace init -q
/tmp/tractor-review run examples/parallel/fan-out-fan-in.json \
  --workdir /tmp/rv-par/workspace --logs /tmp/rv-par/logs
# exit 1
# tractor: pipeline failed: resolve workspace HEAD: git rev-parse --verify HEAD^{commit}:
#   fatal: Needed a single revision: exit status 128
```

The engine's worktree freeze requires a repository with at least one commit
(`engine/git_workspace.go`, reached from `engine/parallel.go:63`). Adding
`git commit --allow-empty -m init` makes the identical invocation succeed
(exit 0, verified above). Impact: the only newly committed parallel example is
not runnable as documented, and the failure surfaces as a raw plumbing error
rather than a stated precondition — precisely the first-run experience an
example exists to protect. Either the README must state the commit
precondition or the engine should reject an unborn-HEAD workspace with an
actionable message.

### 3. Ongoing-run observability cannot attribute branch stages while a fan-out is live (issue)

During a fan-out, `engine/runner.go:413` and `:438` emit `StageStarted` /
`StageCompleted` with only `name` and `index`, identical in shape to top-level
stage events; nothing in the live stream says which branch the stage belongs
to. The branch→workdir→stage-dir mapping exists only in `branches.json`, which
`engine/parallel.go:78` writes **after** every branch has converged. In the
reproduced run, timeline lines for `left`/`right` (indices 4 and 5) are
distinguishable only because each branch happens to be a single node whose id
equals the branch id; a branch that walks several nodes emits stage events that
an external supervisor cannot map to a branch until the whole parallel stage
ends. `worktrees.jsonl` is written up front and carries branch→workdir, but no
stage event carries either field.

Impact on the requested observability evaluation: the surface a spec-blessed
external supervisor is told to tail (§3.9 "Supervision is external", §10) is
sufficient for a top-level walk but degrades exactly where supervision matters
most — during concurrent work, which is also the interval in which steering is
rejected (409). Spec §10 makes extra fields a MAY, so this is a design gap, not
a violation; adding `branch`/`workdir` (or the branch's stage-dir) to branch
stage events would close it for free, since the handler already holds both.

### 4. `appendTimeline` mutates and overwrites its caller's event map (nitpick)

`engine/store.go:97` writes `event["ts"] = …` into the caller-owned
`timelineEvent` map. Every current caller passes a fresh literal, so nothing
breaks today, but the function silently (a) mutates an argument the signature
presents as data to be written and (b) clobbers any caller-supplied `ts` — the
spec explicitly invites callers to attach extra fields (§10), so a future
harness-sourced event carrying its own native timestamp would be rewritten to
the append time. Set-if-absent, or stamp a shallow copy.

### 5. The fan-out example's in-graph isolation assertions are vacuous (nitpick)

Both branch commands assert `test ! -e parallel-right.txt` / `test ! -e
parallel-left.txt` (`fan-out-fan-in.json:33,38`). Because each branch runs in
its own worktree, the sibling file cannot exist there even under strictly
sequential, non-isolated-by-time execution, so those two assertions can never
fail and prove nothing. The real, load-bearing isolation and concurrency
evidence is elsewhere and is sound: distinct `workdir`s in `branches.json`,
`worktrees.jsonl`, the interleaved `ParallelBranchStarted`/`Completed` events,
and the `verify` node's overlap test (`ls < rf && rs < lf`), which a sequential
run of two 4-second sleeps cannot satisfy even at 1-second `date +%s`
granularity. Consider dropping the vacuous checks so the example does not teach
a check that cannot fail.

## Evaluation notes requested by the caller

**JSON workflow language.** The two examples exercise `start`, `tool`,
`parallel`, `parallel.fan_in`, `exit`, per-node and file-level `defaults`, and
single-edge routing; parsing, lint, and the new `examples/examples_test.go`
guard (glob → `graph.Parse` → `lint.ValidateOrError`) all hold. Observations
the examples make concrete:

- Every `parallel` block must converge on a `parallel.fan_in`, which is an LLM
  node by definition (spec §2.4, §4.9; lint `parallel_fan_in`,
  `fan_in_single_parallel`). A fully deterministic fan-out therefore cannot
  exist: this example spends a real Codex turn purely to join two zero-token
  tool branches. That is spec-mandated, not an implementation defect, but it is
  the single largest cost/ergonomics property the language imposes.
- JSON has no multi-line strings and no comments, so non-trivial `tool` nodes
  become long single-line shell programs with escaped newlines — the `verify`
  node is ~700 characters on one line, and its semantics (`\n` inside a JSON
  string becoming a literal newline in the shell command) are invisible in
  review. This is where authoring errors will concentrate.
- Routing is pleasantly minimal: single-successor nodes need no `condition`,
  and `defaults` genuinely removed provider/model/effort repetition in the
  steering graph.

**Ongoing-run observability.** Strong: `timeline.jsonl` is appended per event
(observed live), now carries RFC 3339 nanosecond `ts` on every event,
`manifest.json` advertises `control_socket` before the first stage (including
the `/var/folders` fallback when the socket path would exceed 100 bytes,
`engine/control.go:42-49`), `events/index.jsonl` + `current.jsonl` point at the
live native segment, `stages/latest/<node>` symlinks repoint per stage, and
`steering.jsonl` lands in the active stage at accept time. Weak: no SSE
projections (`/events/lifecycle`, `/events/detail`) exist, so a remote operator
needs filesystem access to the run directory — spec-permitted (§3.9 "MAY"), but
it means the "coding agent supervises a child run" story only works co-located;
and branch attribution during fan-out is missing (Finding 3). Stage events also
omit harness/model/session, which §10 reserves as an optional slot and which
would let a supervisor see *what* is running, not just *that* something is.

## Residual gap (from the excluded manager node)

Per `ephemeral/worklog/202608170739-live-examples.md`, the in-graph manager node
remains omitted by user decision, and this pass was told not to implement it.
Noted, not re-litigated — but recording the consequence: the operator half of
the manager loop is now proven (invoke → observe run surfaces → `POST /steer` →
steering received and acted on), while the observation half a manager would
depend on is the weaker one (no SSE, no branch attribution during fan-out).
If the manager node is later restored as a typed node driving a child pipeline,
Finding 3 becomes blocking rather than cosmetic.

Also worth flagging as run state rather than as a defect: the timeline `ts`
implementation, its test, and the entire retained proof tree are uncommitted /
untracked, so the committed tip `1944160` contains examples whose proof and
observability fix live only in the working tree.

## Outcome

`material findings remain`
