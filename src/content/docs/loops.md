---
title: Loops
description: A loop is a thing that runs iteratively until it's done — in Tractor, two nodes pointing at each other. Start from a copy-and-run example and change two strings.
eyebrow: Pattern guide
order: 1
sourceLabel: Browse the runnable examples
sourceUrl: https://github.com/tylergannon/tractor/tree/main/examples/loops
---

A loop is a thing that runs iteratively until it's done. In Tractor that is
two nodes pointing at each other:

```yaml
goal: Implement the TODO in cmd/server/routes.go and make the tests pass
start: implement
nodes:
  - id: implement
    type: codergen          # an agent turn
    max_visits: 5           # the budget; nothing else stops a loop
    prompt: Work toward the goal. Make the smallest change that could satisfy the check.
    edges:
      - to: check
  - id: check
    type: tool              # a command decides what "done" means
    tool_command: go test ./...
    on_success: success
    on_error: implement     # failure routes back — that's the loop
```

An agent works, a command decides, failure routes back. That's a complete
pipeline. Everything below is a variation on it, and every one ships as a
runnable file in [`examples/loops/`](https://github.com/tylergannon/tractor/tree/main/examples/loops):
copy it, change the goal and the check, run it.

## Pick your moment

| You want | Example |
|---|---|
| "Don't stop until it actually works" | [`fix-until-green.yaml`](https://github.com/tylergannon/tractor/blob/main/examples/loops/fix-until-green.yaml) |
| "Have another model check this" | [`critique-circle.yaml`](https://github.com/tylergannon/tractor/blob/main/examples/loops/critique-circle.yaml) |
| "Try a couple of approaches in parallel" | [`bake-off.yaml`](https://github.com/tylergannon/tractor/blob/main/examples/loops/bake-off.yaml) |
| "Keep working on this after I leave" | [`milestone-loop.yaml`](https://github.com/tylergannon/tractor/blob/main/examples/loops/milestone-loop.yaml) |

**Fix-until-green** is the pipeline above.

**Critique circle** fans one topic out to three providers — Claude, Codex,
and Gemini through their real CLIs — each writes a proposal, then each
critiques the other two. Multi-model checks and balances, one `parallel`
node at a time. Mutual critique across labs is the point: a reviewer from a
different lab inherits none of the author's framing.

**Bake-off** gives the same task to three isolated worktrees and lets a
judge run each result before merging the winner. The judge is told to
decide on demonstrated behavior, not on the reports.

**Milestone loop** is for far-off goals: a chooser looks at the goal and
the repository *as it is now*, names the smallest next step that ends in
something runnable, an implementer does it, a command checks it, repeat.
No upfront plan to go stale. When you want to read and approve a plan
before the tokens burn, add a planner node in front — the two shapes differ
by exactly one node.

## Make "done" honest

The tool node's command is what "done" means. Point it at the closest
observable proof of the outcome you asked for — run the app, curl the
endpoint, assert on the artifact. Tests and linters are worth requiring,
but they prove the claim only when they exercise that behavior.

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
| Agent guesses at a decision that wasn't its to make | Give it a door: an edge whose condition is "this decision isn't mine," leading to a node that asks a human or writes a report and routes to `failure`. Agents improvise when forward is the only offered route. |
| Builds everything, nothing runs until the end | Steer the chooser to vertical slices: "the step is done when you can run something that proves it." Stack-order plans (schema → services → API → UI) are the model's default tic; say no to them in the prompt. |
| A long run starts believing its own stale plans | Per-lap plans are working notes, not authority; they live with the run, the code is the record. Don't commit them. |

Every run leaves its evidence — prompts, responses, routing decisions,
collected artifacts — in a browsable run directory, so when a loop
misbehaves you can read exactly what it was thinking.

## Going further

The [authoring guide](/docs/authoring-pipelines/) covers the graph language
when an example isn't enough. The engineering philosophy behind these
patterns — proof of work, vertical slices, adversarial review — is
distilled from the skills in
[tylergannon/agents](https://github.com/tylergannon/agents).
