# Synthesis of Independent Plan Reviews

## Overall assessment

Both reviews agree that the plan has a sound high-level sequence—specify, implement,
verify, review, fix, and package—and a useful separation between Claude Code as the
implementer and Codex as the reviewer. Both also reach the same central conclusion:
the plan is not execution-ready because it can produce persuasive orchestration
records without proving that an independent reviewer verified the exact final
artifact.

The reports are complementary. The Codex review focuses on pinning the artifact
contract and binding evidence to commits. The Claude review focuses on enforcing
review independence, making finding adjudication auditable, and handling tool and
loop failures. Together they imply the following prioritized changes.

## Required changes

### 1. Pin the deliverable and acceptance contract before invoking either agent

Replace the example-based artifact choice with a finite contract in `spec.md`. It
should state:

- the language and supported runtime;
- exact source and test paths;
- the public API or CLI syntax, outputs, and exit codes;
- explicit normal, error, and edge-case behavior;
- the exact test, lint, and other verification commands required for acceptance.

The artifact should be small enough for complete review but substantial enough to
exercise meaningful behavior. This prevents the implementer or orchestrator from
redefining “done” during the run and gives the reviewer an objective checklist.

### 2. Preflight the orchestration tools and evidence capture

Before implementation begins, smoke-test `claude -p` and `codex exec` for
installation, authentication, and non-interactive operation. Define the exact
commands and log paths used to capture stdout, stderr, exit status, timestamp, and
session identity for both agents.

Because the stated goal specifically requires Claude Code plus Codex, either CLI
being unavailable should fail the run rather than silently degrade it to
self-review. An alternative reviewer may be used only if the goal is explicitly
changed and the resulting proof is labeled accordingly.

### 3. Address every proof artifact to an immutable commit

Create a machine-readable `run-manifest.json` containing at least the repository
path, base SHA, implementation SHA, every fix SHA, final SHA, agent/session IDs,
timestamps, invoked commands, and exit codes. Capture both stdout and stderr from
verification commands.

Use explicit commit points:

1. base state;
2. initial implementation and tests;
3. each accepted-finding fix round;
4. final candidate reviewed by Codex.

Every `verification.log`, review, and summary claim must name the SHA it covers. The
delivered commit must exactly match the final verified and reviewed SHA.

### 4. Make the review operationally independent

Give Codex a fresh, context-isolated session with the pinned `spec.md`, the
base-to-candidate committed diff, and the documented verification commands. The
reviewer should run the required checks itself in a clean checkout or worktree,
rather than merely reading an implementer-produced log. It should also assess test
adequacy and may run additional black-box or adversarial probes without modifying
the candidate artifact.

`review-codex.md` should identify the reviewed SHA and record a pass/fail result for
each acceptance criterion, plus any findings with severity and evidence. Agent
transcripts remain useful provenance, but they are supporting evidence that the
agents ran—not proof of correctness or independence by themselves.

### 5. Define adjudication and terminate the review loop without false sign-off

Track every reviewer finding as accepted, disputed, or dismissed. Accepted findings
must map to a fix commit. Dismissed findings require written rationale. A disputed
material finding must be escalated to an independent arbiter or the user; the
implementer/orchestrator cannot unilaterally call it resolved.

Cap the fix-and-review cycle at two rounds. Any code change after a review requires
the full verification suite and a fresh context-isolated Codex review of the new
final SHA. If material findings remain after the cap, stop with an inconclusive or
failed verification result and document the open findings; do not claim independent
sign-off.

## Recommended execution sequence

1. Write the pinned contract and acceptance matrix in `spec.md`.
2. Preflight both CLIs and transcript/log capture; abort on unmet prerequisites.
3. Record the base SHA, then have Claude implement code and tests and commit them.
4. Run the specified checks, recording commands, outputs, statuses, and candidate
   SHA in the manifest.
5. Have a fresh Codex session independently execute the checks and review every
   acceptance criterion against that SHA.
6. Resolve findings through explicit statuses and fix commits, repeating full
   verification and fresh review for at most two rounds.
7. Assemble `SUMMARY.md` as an acceptance-criterion-to-evidence map only after the
   final SHA has both passing verification and reviewer sign-off.

This revised workflow preserves the original plan’s useful two-agent structure while
making its central claim falsifiable: a specific final commit either has reproducible
verification and independent criterion-by-criterion approval, or it does not.
