# Adversarial review — spec migration and supervisors design, round 07

## Review target

`ephemeral/projects/tractor/spec-supervisors-design.md` (46 lines, md5
`a3878b45092f7382c2260f6b3ec059f8`), with its companion
`ephemeral/projects/tractor/upstream-spec-audit.md` (md5
`449ab707b045ac81d4f7375f8480674a`, unchanged since round 04).

Authorities, unchanged across all rounds:

- upstream Attractor `docs/spec.md` at `0aca8b748e6ecc23446fc690d2b66690b77fe0d3`
  (`git show origin/main:docs/spec.md` from `/Users/tyler/src/attractor`,
  2532 lines), the document this design pins and will copy byte-for-byte;
- repository instructions in `skills/orchestrate-attractor-loops/SKILL.md`,
  `README.md`, `examples/README.md`, `lefthook.yml`;
- `ephemeral/worklog/202608171707-spec-supervisors.md`;
- the current implementation.

The launch prompt supplied only the target, the read-only boundary, and the
artifact path — no narrowing of defects, files, or subject matter, no predicted
findings, no requested verdict — so nothing was refused. Scope is the whole
current design, not only the round-06 deltas.

## Round-06 findings status

All three round-06 findings are addressed:

1. Rejection behaviour now has an observable claim, line 37, covering unknown
   node types, illegal defaults and per-type fields, start/success paths,
   duplicate chooser targets, invalid supervision scopes, supervisor routing
   targets, and supervision cycles.
2. Step 1 (line 25) now records the deviations "and rationale directly in the
   README", so the durable tree no longer depends on an ephemeral artifact.
3. Line 9 now names the `KnownCustomTypes`/`type_known` lint surface as part of
   what the catch-all removal deletes (`lint/lint.go:41,141`,
   `lint/rules.go:403-412`, `lint/lint_test.go:245`).

Nothing regressed.

## Evidence inspected

- Spec §2.5–§2.9, §3.1–§3.10 (supervision in full, 890–1115), §4.3 backend
  interface block (1272–1360), §4.5–§4.7, §5.3, §5.6 run directory structure,
  §7.2 lint table in full, §7.3, §9 extensibility, §10 events, §11.2–§11.3
  conformance checklists, §12.1–§12.4.
- CLI surface: `cmd/tractor/root.go:26-34` (`validate`, `run`, `print-schema`),
  `:37-62` (`validate` command and `validateAndReport`), `:64-…` (`run`,
  including `--resume`), `cliValidator()`.
- `examples/README.md:1-18` and the shipped examples
  `examples/steering/external-steering.json`,
  `examples/parallel/fan-out-fan-in.json`, `examples/yaml/multiline-tool.yaml`;
  `examples/examples_test.go:12-30` (a lint/parse test, not execution).
- Existing live proof for those examples under
  `ephemeral/projects/tractor/live-examples/final/{steering,parallel}` with
  `provenance.txt`, and the six-round review series
  `ephemeral/reviews/202608170758…0850-live-examples-round-0{1..6}.md`.
- `harness/contract.go:146-152`, `harness/backend.go:27-44,157-195,210-252,283-320`,
  `engine/{runner,control,store,parallel}.go`, `graph/{parse,graph}.go`,
  `graph/internal/schemafix/main.go:129-150`, `lint/{rules,lint}.go`,
  `README.md:1-6`, `doc.go`.
- Repository-wide scan for artifacts still written in the outgoing language
  (`start`/`exit` node types): outside `ephemeral/`, the hits are `docs/spec.md`
  (step 1), `graph/jsonschema/Graph.json` (step 2), and the three example
  pipelines (step 2). No skill, README, or Go doc comment embeds an outgoing
  pipeline, so nothing outside the sequence's stated scope goes stale.

## Findings

### 1. issue — the migration invalidates the repository's live example proof, and nothing restores it

`examples/README.md:1-18` presents the three shipped pipelines as runnable,
self-verifying proof, each with its own success criteria: external steering of
one live Codex turn, two tool branches in isolated Git worktrees converging on a
Codex fan-in, and YAML comments and literal block commands through the real CLI.
`README.md:5-6` points users at them as the repository's demonstration surface.
They were earned live — `ephemeral/projects/tractor/live-examples/final/` holds
their run logs and `provenance.txt`, and the series
`ephemeral/reviews/202608170758…0850-live-examples-round-0{1..6}.md` shows six
review rounds spent getting them right.

Sequence step 2 (line 26) rewrites all three into the new language. After that,
every one of them is a document no run has ever executed, and the only automated
coverage they retain is `examples/examples_test.go:12-30`, which parses and
lints — it never runs a pipeline. Nothing in the sequence or the claims restores
the live proof: step 5 (line 29) proves "parsing/routing and supervision"
generically, claim 36 proves "a pipeline authored in the new JSON or YAML
language", and step 6 hands a fresh validator "user-level tasks", not these
files. `skills/orchestrate-attractor-loops/SKILL.md:14-15` separates hygiene
from proof through running software, and `:40` states that passing tests never
override live proof.

Impact: the repository would ship, and the README would advertise, three
self-verifying examples whose verification was destroyed by this change — with
green tests concealing it, which is exactly the substitution SKILL.md:40
forbids. The capabilities they cover (live steering delivery, worktree fan-out
convergence, YAML block scalars) are the ones the language change is most likely
to break, and none is covered by another claim.

### 2. nitpick — claim 37 is phrased at the library seam, where the CLI is the observable one

Line 37 asserts that "the public parser and validator reject" the listed
malformations. Both are library surfaces, so the claim is satisfiable by unit
tests, which SKILL.md:15 classifies as hygiene rather than proof. The repository
already exposes the same behaviour through `tractor validate`
(`cmd/tractor/root.go:37-62`, using `cliValidator()`), which produces an
observable exit status and diagnostic output. Naming that entry point instead
would cost nothing and would put the rejection claim on the same footing as the
rest of the claim set, which is uniformly CLI-observable.

### 3. nitpick — the design never identifies the audit artifact it depends on

Line 18 defers to "the spec audit" for the accepted duplicate-delivery window,
and line 32 describes what "the audit" records, but no line names the artifact
or its path, and the design has no reference section. A reader who has only this
document — the fresh implementer step 2 and step 4 imply — cannot locate
`ephemeral/projects/tractor/upstream-spec-audit.md`, where the rationale for two
of the design's own decisions lives.

## Outcome

material findings remain
