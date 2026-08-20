# Loops

A loop is a thing that runs iteratively until it's done. In Tractor that is
two nodes pointing at each other. Start from whichever example matches your
moment, change the goal and the check, and run it:

```sh
tractor run examples/loops/<file>.yaml
```

- [`fix-until-green.yaml`](fix-until-green.yaml) — the hello world. An
  agent works, a command decides, failure routes back, `max_visits` stops
  it from running forever. For *"don't stop until it actually works."*
- [`critique-circle.yaml`](critique-circle.yaml) — three providers write a
  proposal on the same topic, then each critiques the other two. For
  *"have a couple of models cross-check this."*
- [`bake-off.yaml`](bake-off.yaml) — three attempts at the same task in
  isolated worktrees; a judge runs each one and merges the winner. For
  *"try a couple of approaches in parallel."*
- [`milestone-loop.yaml`](milestone-loop.yaml) — a chooser picks the next
  bite-sized runnable step, an implementer does it, a command checks,
  repeat until the goal's claims are demonstrable. For *"keep working on
  this after I close my laptop."*

## When a loop misbehaves

You don't need this table to write a loop; you need it when a loop annoys
you.

| Symptom | Fix |
|---|---|
| Runs forever | `max_visits` on the looping node. That's the budget; there is no other ceremony. |
| Says it's done when it isn't | Make "done" a command (`tool` node). If the tests pass but the feature doesn't work, the command is checking the wrong thing — check the behavior you actually want. |
| Re-derives the same dead end every lap | Tell the prompt to keep a short notes file: "append what the next attempt should do differently; read it first." |
| Reviewer rubber-stamps | Don't tell it what to find or ask it to confirm your fix. Fresh session (`fidelity: none`), whole target, every round. A different provider makes the independence real. |
| Fan-in averages instead of deciding | Tell it to inspect the work itself and adjudicate each finding with evidence — never count votes or concatenate reports. |
| Agent guesses at a decision that wasn't its to make | Give it a door: an edge whose condition is "this decision isn't mine," leading to a node that asks a human (Slack, an issue) or writes a report and routes to `failure`. Agents improvise when forward is the only offered route. |
| Builds everything, nothing runs until the end | Steer the chooser to vertical slices: "the step is done when you can run something that proves it." Stack-order plans (schema → services → API → UI) are the model's default tic; say no to them in the prompt. |
| A long run starts believing its own stale plans | Per-lap plans are working notes, not authority; they live with the run, the code is the record. Don't commit them. |
