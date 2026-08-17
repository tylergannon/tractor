Use the supplied branch evidence and worktree paths. Inspect both worktrees: the left worktree must contain parallel-left.txt and not parallel-right.txt; the right worktree must contain parallel-right.txt and not parallel-left.txt. In this main workspace create tractor-fan-in-proof, copy those files to tractor-fan-in-proof/left.txt and tractor-fan-in-proof/right.txt, and verify from their started and finished values that the intervals overlap. Only after every check passes, write tractor-fan-in-proof/summary.txt with exactly two lines: isolation=verified and overlap=verified. Do not alter either branch worktree.

Branch ID: left
Notes: exit 0: left completed
Worktree: /tmp/r2-par/logs/worktrees-883991409/branch-001

Branch ID: right
Notes: exit 0: right completed
Worktree: /tmp/r2-par/logs/worktrees-883991409/branch-002