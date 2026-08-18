# agy steering and compaction worklog

correction: Treating agy print mode's lack of a live injection channel as a permanent steering limitation was overly precious; interrupting the process and resuming the same native conversation with the steering message is a practical candidate and must be tested.
decision: Test `/compact` as a real prompt on a non-empty agy conversation before retaining the service-managed no-op.
proof: Completion requires the unchanged shared HarnessAdapter conformance scenarios to run against real agy, not adapter-only unit tests.
friction: The first conformance run failed before steering because agy rejects a tiered model slug such as gemini-3.7-flash-low combined with a conflicting --effort=medium; the adapter then masked the ID-less ERROR result as a conversation mismatch -> omit --effort for tiered model slugs and apply conversation-ID invariants only to successful results.
proof: A direct interrupted print turn resumed on the same conversation ID, applied only the steering prompt, wrote the requested steering file, and returned the original structured result.
proof: A non-empty conversation accepted `/compact`, emitted a context-compaction summary retaining the private token, and recalled that token exactly on the following turn; the command also worked without repeating model or effort flags.
friction: The first post-fix full conformance run had one transient backend-supervisor failure from agy's internal missing toolSummary/toolAction arguments; the identical native two-turn sequence passed in isolation and the next complete eight-scenario run passed, so no speculative retry layer was added.
