# Independent `agent` CLI validation

Result: **PASS** at clean HEAD
`05c064f3ed1de0f0745efcae2f768c75e8f2e563`.

The binary was built exactly once with `go build -o
/tmp/tractor-agent-final-validation-05c064f ./cmd/agent`. Its SHA-256 is
`5dbeb7216208c74182a9643c8f571975f1921f29b620f7ed01c0e8f807a0a6ed`.
Native harness versions were `codex-cli 0.147.0` and Claude Code `2.1.233`.

## Codex caller delegated to Claude Code/Fable

The validation process inherited a nonempty `CODEX_THREAD_ID`, explicitly
removed `CLAUDE_CODE_SESSION_ID`, and invoked the built binary with only
`--logs`, `WORKDIR`, and `PROMPT`; no provider or model flags were supplied.

- Exit status: `0`.
- Random input SHA-256: `73f7851a6e40b49d12dc3419fde852bcae211830ffbe5bad7f3904cd199ad1ef`.
- Native Claude session ID: `6524c863-645a-4dbe-8eff-85af71a3b422`.
- Returned JSON: `outcome.next` was `done`, `notes` was nonempty, and
  `logs_root` named the requested case log directory.
- Independent post-run aggregation of `inventory.json` structurally matched
  the agent-written `audit.json`: grand total `217560` cents and largest
  warehouse `west`.
- Public log: one user event, two complete assistant events, one usage event,
  and four matched tool-call/result pairs. Tool names were `Read`, `Bash`,
  `Write`, and `StructuredOutput`; every `toolu_...` call ID had exactly one
  result.

Evidence is in `codex-caller-claude/`, including the exact prompt, randomized
input, independently computed expected object, agent workspace effect, stdout,
stderr, exit status, and complete public segment/index logs.

## Claude caller delegated to Codex

The validation process set a fresh nonempty `CLAUDE_CODE_SESSION_ID`
(`4ba39e0b-eadb-4626-bc04-efddf1a1c0bb`) and invoked the same built binary with
only `--logs`, `WORKDIR`, and `PROMPT`; no provider or model flags were
supplied.

- Exit status: `0`.
- Random input SHA-256: `e299edef0704369391ba2dcfbd6d66e763f0c3bf4613ae17742002c32c1768d6`.
- Native Codex session ID: `01a00dbe-1a45-7792-adaa-51a042c0a52d`.
- Returned JSON: `outcome.next` was `done`, `notes` was nonempty, and
  `logs_root` named the requested case log directory.
- Independent post-run aggregation of `ledger.json` exactly matched the
  agent-written `reconciliation.json`: net `238` cents and largest absolute
  balance account `delta`.
- Public log: one user event, three complete assistant events, five usage
  events, and four matched tool-call/result pairs. Tool names were
  `commandExecution`, `commandExecution`, `fileChange`, and
  `commandExecution`; every `exec-...` call ID had exactly one result.

Evidence is in `claude-caller-codex/` with the same artifact classes as the
first case.

## Excluded capture

An initial Claude-backed provider call produced valid stdout, workspace effect,
and public logs, but the surrounding zsh wrapper then assigned to its reserved
read-only variable `status`. Because that prevented reliable exit-code capture,
the run is excluded rather than treated as proof. It is preserved under
`codex-caller-claude-aborted-capture/`; the required case was repeated with a
fresh randomized input and a corrected wrapper.
