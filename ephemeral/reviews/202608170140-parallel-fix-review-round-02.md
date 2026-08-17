# Parallel repair review, round 2

Review target: stop-aware Git cancellation and crash-idempotent branch-worktree cleanup.

Evidence inspected:

- `engine/parallel.go`
- `engine/git_workspace.go`
- focused tests in `engine/git_workspace_test.go`
- caller-reported focused race run

## Finding

### Issue: cancellation can orphan an uninventoried worktree

`createBranchWorktreesWithStop` appends the durable inventory entry only after
`gitOutputWithStop` returns success. `gitOutputWithStop`, however, reports
`errGitStopped` whenever the stop signal is set after `command.Run` returns,
even if `git worktree add` completed successfully. A stop racing with command
completion can therefore create and register the worktree, return the stopped
error, and bypass the inventory append. The failed run then has no durable path
for Finalize to sweep. The same outcome is possible when cancellation lands
after Git has performed the worktree side effect but before the process exits.
The fake-Git cancellation test has no worktree side effect and does not cover
this boundary.

The inventory must exist before a cancellable worktree-add side effect can be
lost, or the error path must detect and inventory/remove the created worktree.

The cleanup repair itself correctly asks Git to remove a missing-directory
registration and tolerates the already-unregistered repeat.

Outcome: material findings remain
