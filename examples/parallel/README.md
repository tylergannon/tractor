# Fan-out and fan-in

`fan-out-fan-in.json` runs two deterministic tool branches concurrently in
separate engine-owned Git worktrees. One low-reasoning Codex fan-in turn
receives the branch evidence and must inspect both worktrees before copying the
proof into the main workspace. This spends tokens only on consolidation.

The workspace must be a disposable Git repository with at least one commit:

```sh
mkdir -p "$WORKSPACE"
git -C "$WORKSPACE" init
git -C "$WORKSPACE" -c user.name='Tractor Example' \
  -c user.email='tractor-example@localhost' commit --allow-empty -m base
```

Then run:

```sh
tractor run examples/parallel/fan-out-fan-in.json \
  --workdir "$WORKSPACE" --logs "$LOGS"
```

The run is successful only when:

- `tractor-fan-in-proof/` in the main workspace contains both branch records
  and the fan-in's verification summary;
- the deterministic `verify` node passes;
- the parallel stage's `branches.json` records distinct worktrees and run-log
  stage directories for `left` and `right`; and
- the branch-recorded intervals overlap, proving concurrent execution rather
  than merely quick sequential execution; and
- live branch stage events identify their branch and worktree before fan-in.
