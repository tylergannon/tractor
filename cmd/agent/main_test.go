package main

import "testing"

func TestDetectCallerPrefersClaude(t *testing.T) {
	environment := map[string]string{
		"CLAUDE_CODE_SESSION_ID": "claude-session",
		"CODEX_THREAD_ID":        "codex-thread",
	}
	if got := detectCaller(func(name string) string { return environment[name] }); got != callerClaude {
		t.Fatalf("detectCaller() = %q, want %q", got, callerClaude)
	}
}

func TestResolveSelection(t *testing.T) {
	delegated, err := resolveSelection(callerClaude, options{})
	if err != nil {
		t.Fatal(err)
	}
	if delegated.provider != "openai" || delegated.model != "gpt-5.6-sol" || delegated.reasoningEffort != "medium" {
		t.Fatalf("delegated selection = %#v", delegated)
	}

	standalone, err := resolveSelection(callerNone, options{
		provider: "openai", model: "gpt-explicit", reasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if standalone.model != "gpt-explicit" || standalone.reasoningEffort != "high" {
		t.Fatalf("standalone selection = %#v", standalone)
	}

	if _, err := resolveSelection(callerNone, options{}); err == nil {
		t.Fatal("standalone selection accepted missing provider/model/effort")
	}
	if _, err := resolveSelection(callerCodex, options{}); err == nil {
		t.Fatal("Codex caller selected an unimplemented Claude direction")
	}
}
