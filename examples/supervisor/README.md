# Live supervisor steering

`live-steering.json` completes one supervised setup stage, then starts a Codex
worker and a Claude supervisor on its own patrol clock. The completed setup
digest is waiting when the worker makes the scope live, so the first patrol
also exercises durable inbox rotation and tallying. Run it with a fresh
workspace and logs directory:

```sh
tractor run examples/supervisor/live-steering.json \
  --workdir "$WORKSPACE" --logs "$LOGS"
```

Success requires more than a completed process:

- the supervisor must return a targeted `steer` while `work` is live;
- the worker must create `supervisor-received.txt` from that instruction;
- the deterministic `verify` node must pass;
- a numbered inbox batch must contain the `prepare` outcome digest;
- `timeline.jsonl` must contain delivered `SupervisorVerdict` and
  `SupervisorFlushed` events; and
- `logs/supervisors/coach/` must contain its durable inbox or numbered batch
  and its binding-specific briefing record.
