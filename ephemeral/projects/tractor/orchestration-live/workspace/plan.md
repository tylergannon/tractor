# Plan: Independently Reviewed Implementation Artifact via Claude Code + Codex Orchestration

## Goal
Produce a small, working implementation artifact whose correctness is verified by an
independent reviewer, using real orchestration across Claude Code and Codex agents.

## Steps

1. **Define the artifact.** Pick a small, self-contained deliverable with clear
   acceptance criteria — e.g. a single-module utility (CLI or library function) with
   unit tests. Write the spec and acceptance criteria to `spec.md`.

2. **Implement with Claude Code.** Drive a Claude Code session (headless: `claude -p`)
   to implement the module and its tests on a feature branch in the workspace.
   Capture the session transcript as orchestration evidence.

3. **Verify locally.** Run the test suite and any linters; record output to
   `verification.log`. Fix failures before requesting review.

4. **Independent review with Codex.** Launch a Codex agent (e.g. `codex exec`) with
   read access to the diff and spec, prompted to review adversarially: correctness
   against acceptance criteria, edge cases, and test adequacy. It must not share
   context with the implementer. Save its findings to `review-codex.md`.

5. **Resolve findings.** Feed confirmed findings back to the implementing Claude Code
   session; apply fixes and re-run tests. Repeat review if changes are substantive.

6. **Assemble the proof.** Package the artifact: final diff/commit, `spec.md`,
   `verification.log`, `review-codex.md`, and a short `SUMMARY.md` linking each
   acceptance criterion to its evidence (test + reviewer sign-off), plus the
   orchestration transcripts showing both agents genuinely ran.

## Evidence of real orchestration
- Claude Code transcript (implementer) and Codex transcript (reviewer), with
  timestamps and distinct session IDs.
- Reviewer findings that provably influenced the final diff (before/after commits).
