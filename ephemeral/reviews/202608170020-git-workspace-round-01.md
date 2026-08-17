# Adversarial Review — Git Workspace Foundation (round 01)

Date: 2026-08-17 00:20 local
Reviewer: adversarial-review (self-performed)

## Review target

The uncommitted git-workspace foundation in the `orchestration` worktree
(`/Users/tyler/src/.worktrees/tractor/orchestration`):

- `engine/git_workspace.go` (new, 225 lines)
- `engine/git_workspace_test.go` (new, 297 lines)

Reviewed against `docs/spec.md` Section 4.8 (Parallel Handler) and the caller's
authoritative scope: freeze one parent snapshot without changing the real Git
index, refs, or worktree; create inventoried detached branch worktrees; clean
them only through a completed-run helper. Branch walking is intentionally not
integrated in this slice, and the review does not treat its absence as a defect.

### Scope handling note

The launch prompt supplied valid operating constraints (read-only outside this
artifact, artifact path, five-finding cap) and one statement of intent — that a
hook had already surfaced an inherited-Git-context defect now "corrected" by
stripping repository-local Git environment variables. That statement was treated
as context, not as a declaration that the Git-environment area is settled or
off-limits; the area was re-inspected from scratch. Finding 1 below reports that
the correction addresses only one half of the defect class.

## Evidence inspected

- `docs/spec.md` §4.8 lines 1224–1360 (freeze_workspace, create_worktree,
  worktree ownership, `worktrees.jsonl`, Finalize-only removal), §4.9, and the
  Finalize clause at line 414.
- `engine/git_workspace.go` in full.
- `engine/git_workspace_test.go` in full, including the corrected
  `GIT_CEILING_DIRECTORIES` sandbox (line 22) and the corrected `gitTest` helper
  (line 292).
- Surrounding uncommitted work for integration context: `engine/runner.go`,
  `engine/store.go`, `engine/fan_in.go`, `lint/topology.go`.
- Repository-wide grep for callers of `freezeGitWorkspace`,
  `createBranchWorktrees`, `cleanupBranchWorktrees`, `worktrees.jsonl`.
- `ephemeral/worklog/202608162136-build-orchestration.md`.
- Proof re-run: `go test ./engine/ -run 'GitWorkspace|Freeze' -count=1 -v` — both
  tests PASS (0.30s).
- Four targeted Git experiments in throwaway repositories (transcripts
  summarized inline below).

## Findings

### 1. CRITICAL — `gitOutput` folds stderr into the value it parses; the
environment scrub does not cover output-affecting Git variables

`engine/git_workspace.go:187` reads the child process with `CombinedOutput()`,
and the trimmed result is then used *as a value*: the git-repository predicate
(line 34–35), the repository root (line 38), the HEAD commit (line 42), the
snapshot tree ID (line 60), and the snapshot commit ID (line 73). Any byte Git
writes to stderr on a successful command is silently prepended to that value.

`cleanGitEnvironment` (lines 198–224) strips the fifteen repository-local
variables, which fixes the inherited-repo-context half of the problem, but Git's
output-affecting variables — `GIT_TRACE`, `GIT_TRACE2`, `GIT_TRACE_PERFORMANCE`,
`GIT_TRACE_SETUP`, and friends — are not stripped and are inherited from
`os.Environ()` at line 185.

Reproduction (verified):

```
$ GIT_TRACE=1 git -C repo rev-parse --show-toplevel 2>&1 | head -2
22:57:12.986097 git.c:502  trace: built-in: git rev-parse --show-toplevel
/private/var/folders/.../repo
```

The same leak reaches the code under review. Running the existing suite with the
variable exported:

```
$ GIT_TRACE=1 go test ./engine/ -run GitWorkspace -count=1
--- FAIL: TestGitWorkspaceSnapshotFanoutInventoryAndCleanup (0.08s)
    git_workspace_test.go:42: open .../001/22:57:27.167847 git.c:502  trace:
        built-in: git rev-parse --git-path HEAD
        .git/HEAD: no such file or directory
```

Failure scenario in production: an operator with `GIT_TRACE=1` (or a CI image
that sets `GIT_TRACE2`, or any Git build/config that emits a `warning:` on these
commands) runs a parallel node. `rev-parse --is-inside-work-tree` returns
`"<trace line>\ntrue"`, which fails the `inside != "true"` compare at line 35, so
a perfectly valid Git repository is rejected with the terminal error "parallel
execution requires a git worktree". If the leak instead lands on a later command,
`repoRoot` becomes an unusable path, or — worse — `tree`/`commit` become
non-SHA strings that are handed to `commit-tree` and `worktree add`, so the
diagnostic names the wrong cause entirely.

Two aggravating details in the same code:

- Line 35 collapses `err != nil` and `inside != "true"` into one message, so
  every genuine Git failure here (git absent, dubious-ownership refusal,
  permission error) is misreported as "not a git worktree". The spec makes
  non-git a terminal Error (§4.8 line ~1249); it does not make every Git failure
  wear that label.
- The test helper `gitTest` (line 292) now applies `cleanGitEnvironment` but also
  uses `CombinedOutput()`, so the proof shares the defect and cannot detect it.

Fix direction: capture stdout and stderr separately (`cmd.Stdout`/`cmd.Stderr`
into distinct buffers), parse only stdout, and report stderr only inside error
messages. Add the trace family to `cleanGitEnvironment` as defense in depth, and
separate the "not a repository" verdict from "git failed".

### 2. ISSUE — cleanup runs a repository-global `git worktree prune` that
destroys administrative state for worktrees the engine never created

`engine/git_workspace.go:144` issues `git worktree prune` against the whole
repository after the inventoried removals. The authoritative scope is to clean
*inventoried* worktrees; `git worktree remove --force` at line 140 already
deletes each one's admin entry, so the prune adds no in-scope work and only
widens the blast radius.

Reproduction (verified): a user worktree that is momentarily unreachable — an
unmounted volume, a renamed directory, a network mount that dropped — is
"prunable", and the engine's cleanup deletes it:

```
$ git worktree list
.../repo            8982450 [main]
.../user-important  8982450 [user-feature] prunable
$ git worktree prune -v
Removing worktrees/user-important: gitdir file points to non-existent location
$ ls .git/worktrees
ls: .../repo/.git/worktrees: No such file or directory
```

The user's per-worktree HEAD, `ORIG_HEAD`, reflog, and any commits anchored only
by that worktree's HEAD are un-referenced and become gc candidates; remounting
the volume yields a worktree Git no longer recognizes. Section 4.8 grants the
engine ownership of the worktrees it creates, not of the repository's worktree
administration. Delete line 144.

### 3. ISSUE — the completed-run cleanup helper is not idempotent and hard-fails
on entries that are already gone

`worktrees.jsonl` is append-only by design (§4.8) and `cleanupBranchWorktrees`
never records that an entry was swept, so every invocation replays the entire
inventory. `git worktree remove --force` on a path whose admin entry no longer
exists is a hard error:

```
$ git worktree remove --force $T/fan1/branch-001 ; echo exit=$?
exit=0
$ git worktree remove --force $T/fan1/branch-001 ; echo exit=$?
fatal: '.../fan1/branch-001' is not a working tree
exit=128
```

Failure scenario: a fan-out whose worktree removal partially failed (one busy
file, one permission error) leaves `os.Remove(parent)` at line 148 failing too,
so cleanup returns a non-nil error; the operator re-runs, or a later completed
run's Finalize sweeps the same `logsRoot`, and now the *already removed* entries
each contribute a `fatal: ... is not a working tree` — a healthy completed run
reports a Finalize failure it cannot clear without hand-editing an append-only
evidence file. The same happens whenever anything else pruned the entries first
(auto-gc, or the prune in Finding 2 running before a later sweep).

Cleanup should treat "already absent" as success — check `git worktree list
--porcelain` (or stat the path and the admin entry) before removing, or classify
the `is not a working tree` outcome as a no-op rather than an error.

### 4. ISSUE — untracked embedded Git repositories are silently reduced to empty
directories in every branch worktree

Section 4.8 requires the frozen state to carry "tracked modifications, plus
untracked-but-not-ignored files". `git add -A -- .` (line 57) records an
untracked nested repository as a gitlink instead of as files, and the outer
object store has none of the inner repo's objects, so `git worktree add` checks
out an empty directory — without error.

Reproduction (verified):

```
$ GIT_INDEX_FILE=$IDX git add -A -- .
warning: adding embedded git repository: nested-repo
$ git ls-tree $TREE
100644 blob 45b983b...  f
160000 commit 5beb15b...  nested-repo
$ git worktree add --detach $T/wt $C && ls -a $T/wt/nested-repo
.  ..                      # every file inside nested-repo is gone
```

Failure scenario: a workdir containing a vendored checkout, a `node_modules`
package with a `.git`, or a sibling clone that is untracked and not ignored. Each
branch agent starts from a workspace that silently lacks that content, produces a
candidate built against missing files, and nothing in `branches.json` or the run
log indicates the omission. The freeze must either capture such trees as ordinary
files or fail loudly; silent partial capture is the worst of the three options.
(Note that the warning above lands on `add`, whose output is discarded — so once
Finding 1 is fixed by parsing stdout only, this still needs its own detection.)

### 5. ISSUE — branch worktrees are created under the system temp directory, which
undercuts the spec's durable-evidence guarantee for failed runs

`createBranchWorktrees` places the fan-out root at `os.MkdirTemp("", ...)`
(line 84), i.e. under `TMPDIR`. Section 4.8 makes these worktrees durable
evidence: "a failed run leaves all its worktrees on disk, conservatively", and a
resumed fan-in reads `branches.json`, which names each branch's **worktree path**
(§4.9, lines ~1379–1390), because the fan-in agent is asked to evaluate the
candidates those worktrees hold.

The system temp directory does not offer that durability. `systemd-tmpfiles`
clears `/tmp` on a default ~10-day age on most Linux distributions and many
distributions clear it on reboot; macOS periodically purges `/var/folders`. A run
that fails on Friday and is resumed after a reboot finds `branches.json` pointing
at paths that no longer exist, and the fan-in has nothing to evaluate — the exact
outcome the "leave worktrees in place on failure" rule exists to prevent. It also
makes Finding 3 fire routinely rather than rarely.

The spec allows an "implementation-chosen location"; the run directory
(`logsRoot`, which already holds `worktrees.jsonl` and `branches.json`) is the
location that satisfies the guarantee, keeps evidence co-located with the run it
belongs to, and survives the same events the rest of the run directory survives.

## Nitpicks

- `cleanupBranchWorktrees` (line 123) has no caller anywhere in the repository
  and no notion of run completion — its signature is `(workdir, logsRoot)`, so
  nothing structurally prevents a future failure path from calling it and
  destroying the evidence §4.8 requires a failed run to retain. The absent
  Finalize wiring is an accepted slice boundary, but taking a completed-run
  `RunResult`/status, or asserting it, would make the "completed runs only"
  invariant the helper's own property rather than a convention its future caller
  must remember.
- `freezeGitWorkspace` resolves to the repository top level (line 38) and returns
  only the worktree root in `branchWorktree.Path` (line 24). When `scope.workdir`
  is a subdirectory of the repository, the branch's workdir must be
  `wt + rel(workdir, repoRoot)`; nothing in the returned structure carries that
  relative path, so the branch-walking slice will have to recompute it.
- The snapshot commit is unreferenced between `commit-tree` (line 73) and the
  first `worktree add` (line 91), and again after cleanup removes the last
  worktree. A concurrent `git gc --prune=now` in that window can drop it. Low
  probability; worth a comment if not a temporary ref.

## Verification of the stated correction

The described fix is real and works as far as it goes: `cleanGitEnvironment`
(lines 198–224) strips the fifteen repository-local variables in production;
`gitTest` (line 292) applies the same scrub so the proof no longer runs Git under
inherited repository context; and
`TestFreezeGitWorkspaceRejectsNonGitDirectory` now builds a sandbox directory and
sets `GIT_CEILING_DIRECTORIES` (lines 17–22) so the non-repository assertion no
longer depends on the test host's ancestry — it correctly fails without the
ceiling when the temp directory sits inside a repository. Both tests pass on
re-run. Finding 1 is the unaddressed remainder of the same defect class, not a
regression of this fix.

## Outcome

material findings remain
