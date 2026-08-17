# Tractor examples

These examples exercise Tractor through its real coding-agent harnesses. They
are intentionally small because their purpose is to prove orchestration, not to
spend tokens on a substantial coding task.

- [`steering/external-steering.json`](steering/external-steering.json) proves
  that an external operator can steer one active Codex turn. The workflow only
  completes if the live agent acts on the instruction.
- [`parallel/fan-out-fan-in.json`](parallel/fan-out-fan-in.json) proves that two
  tool branches execute concurrently in isolated Git worktrees and that one
  Codex fan-in turn reads both products into the main workspace.

Each directory documents the invocation and the observable success criteria.
