package gimble

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// stepTokens is the five-field token shape a step event carries.
func stepTokens(input, output, reasoning, read, write float64) map[string]any {
	return map[string]any{
		"input": input, "output": output, "reasoning": reasoning,
		"cache": map[string]any{"read": read, "write": write},
	}
}

// TestSessionUsageAccumulates runs one turn whose steps and harness report
// all carry usage, and reads back the session totals it published and the
// turn's usage in the run log.
func TestSessionUsageAccumulates(t *testing.T) {
	f := &fake{
		report: map[string]Usage{"fake-model": {Cost: 0.25}},
		answer: func(_ context.Context, session, _ string, _ json.RawMessage, emit func(AgentEvent) error) (string, error) {
			if err := emit(fakeAgentEvent("session.step.ended", session, "step-1", map[string]any{
				"cost": 1.5, "tokens": stepTokens(10, 4, 2, 3, 1),
			})); err != nil {
				return "", err
			}
			if err := emit(fakeAgentEvent("session.step.failed", session, "step-2", map[string]any{
				"cost": 0.5, "tokens": stepTokens(1, 0, 0, 0, 0),
			})); err != nil {
				return "", err
			}
			// A step that never reached the model carries no usage.
			if err := emit(fakeAgentEvent("session.step.failed", session, "step-3", nil)); err != nil {
				return "", err
			}
			return "done", nil
		},
	}
	var totals []Usage
	var dir string
	err := runTest(t, func(ctx context.Context) error {
		dir = runDir(ctx)
		s := NewSession(ctx, "worker", f, "fake-model", ".")
		_, err := s.turn(ctx, "go", nil, func(event AgentEvent) error {
			if event.Type != "session.usage.updated" {
				return nil
			}
			var total Usage
			if err := json.Unmarshal(event.Data, &total); err != nil {
				return err
			}
			totals = append(totals, total)
			return nil
		}, "gimble.Text", nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// One total after each step that carried usage, one after the harness
	// report, each the session's running sum.
	want := []Usage{
		{Cost: 1.5, Tokens: tokens(10, 4, 2, 3, 1)},
		{Cost: 2, Tokens: tokens(11, 4, 2, 3, 1)},
		{Cost: 2.25, Tokens: tokens(11, 4, 2, 3, 1)},
	}
	if len(totals) != len(want) {
		t.Fatalf("usage.updated totals = %+v, want %d of them", totals, len(want))
	}
	for i, total := range totals {
		if total != want[i] {
			t.Fatalf("usage.updated %d = %+v, want %+v", i, total, want[i])
		}
	}

	var ended []TurnEnded
	for _, record := range readRecords[LifecycleRecord](t, filepath.Join(dir, "run.jsonl")) {
		if turn, ok := record.Event.(TurnEnded); ok {
			ended = append(ended, turn)
		}
	}
	// The harness stated a turn report, so the turn records that report.
	if len(ended) != 1 || len(ended[0].Usage) != 1 || ended[0].Usage[0] != (ModelUsage{Model: "fake-model", Cost: 0.25}) {
		t.Fatalf("turn ended usage = %+v", ended)
	}
}

// TestTurnUsageFallsBackToItsSteps records a turn from its step events when
// the harness states no turn report of its own.
func TestTurnUsageFallsBackToItsSteps(t *testing.T) {
	f := &fake{
		answer: func(_ context.Context, session, _ string, _ json.RawMessage, emit func(AgentEvent) error) (string, error) {
			return "done", emit(fakeAgentEvent("session.step.ended", session, "step-1", map[string]any{
				"cost": 0.75, "tokens": stepTokens(9, 5, 1, 2, 0),
			}))
		},
	}
	var dir string
	if err := runTest(t, func(ctx context.Context) error {
		dir = runDir(ctx)
		s := NewSession(ctx, "worker", f, "fake-model", ".")
		_, err := s.turn(ctx, "go", nil, nil, "gimble.Text", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := ModelUsage{Model: "fake-model", Cost: 0.75, Tokens: tokens(9, 5, 1, 2, 0)}
	for _, record := range readRecords[LifecycleRecord](t, filepath.Join(dir, "run.jsonl")) {
		if turn, ok := record.Event.(TurnEnded); ok {
			if len(turn.Usage) != 1 || turn.Usage[0] != want {
				t.Fatalf("turn ended usage = %+v, want %+v", turn.Usage, want)
			}
			return
		}
	}
	t.Fatal("the run log has no turn_ended")
}

func tokens(input, output, reasoning, read, write float64) Tokens {
	var t Tokens
	t.Input, t.Output, t.Reasoning = input, output, reasoning
	t.Cache.Read, t.Cache.Write = read, write
	return t
}
