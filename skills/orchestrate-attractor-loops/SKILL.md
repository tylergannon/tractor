---
name: orchestrate-attractor-loops
description: Manually shepherd a substantial repository change through independent design, critique, implementation, review, and live validation loops. Use when an agent must act as the manager for subagents, translate review outcomes into the next assignment, converge before advancing phases, and prove the real software works rather than relying on tests alone.
---

# Orchestrate Attractor Loops

Own the outcome. Treat routing as a manager judgment informed by artifacts, not as a scripted state machine.

## Establish authority and proof

1. Read the complete governing specification and repository instructions before delegating.
2. Copy or pin external authority locally when the task requires a stable reference.
3. After context compaction, re-read this skill and the specification sections governing the active phase before doing more work.
4. State observable proof claims. Separate hygiene checks from proof through the running software.
5. Keep designs, reviews, transcripts, and proof under the repository's ephemeral-work path unless another location is prescribed.

## Run the design loop

1. Give a fresh author agent the authoritative specification, current repository, constraints, and requested design path. Ask for a complete design, not implementation.
2. Launch an independent reviewer through the repository's `agent` CLI. Let the CLI select the reviewer; do not name a model positionally.
3. Ask for an unconditional review of the entire current design against its authorities. Do not seed expected findings or protected areas.
4. Read the review artifact. Accept supported material findings, reject incorrect ones with evidence, and send the author a bounded revision assignment.
5. Resume the same reviewer with the full current design and a new output path. Advance only when the reviewer reports no material findings or only nits.

## Run the implementation loop

1. Split work only at coherent feature boundaries. Give each implementer exact authority and a writable scope; avoid fragmenting work into bookkeeping-sized tasks.
2. Inspect each result and integrate it before launching work that depends on it.
3. Run focused checks during development and the repository's full hook suite before review.
4. Launch an independent, unconditional implementation review. Require inspection of the complete diff, surrounding code, and executable behavior.
5. Include these concerns as authoritative acceptance context when they are user requirements: remove over-engineering, belt-and-suspenders logic, unrequested features or safety, non-free future-proofing, and unnecessary abstraction; require idiomatic language use and actual functionality.
6. Resolve material findings and re-review the whole implementation in the same reviewer session. Stop only at no findings or nits.

## Validate independently

1. Give a fresh validation agent the built artifact and user-level task, not the intended answer or internal implementation notes.
2. Require non-trivial use through the real entry point and every required provider or mode. Have the validator inspect resulting workspace effects and outputs.
3. Preserve commands, versions, exit status, outputs, and artifacts needed to make the proof auditable.
4. Route any functional failure back to implementation, then repeat review and independent validation. Passing tests never overrides failed live proof.

## Manage agents deliberately

- Prefer complete initial assignments and natural completion over frequent steering.
- Send a steering message only when new authoritative information appears or the agent is clearly leaving scope.
- Never ask a reviewer to confirm a fix or desired verdict; ask for a new complete review.
- Do not advance a phase because the schedule is inconvenient. Advance when the artifact meets the phase gate.
- Report the final state in claims: what works, what independently demonstrated it, what checks passed, and any residual limitation.
