package observation

import (
	"encoding/json"
	"testing"
)

// usageUpdatedEvent is the running total the core publishes for one session.
func usageUpdatedEvent(native string, cost float64, input, output, reasoning, read, write int) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"id":      "evt-" + native,
		"type":    usageUpdated,
		"created": 10,
		"data": map[string]any{
			"sessionID": native,
			"cost":      cost,
			"tokens": map[string]any{
				"input":     input,
				"output":    output,
				"reasoning": reasoning,
				"cache":     map[string]any{"read": read, "write": write},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func inScopeSession(session, scope string) Lifecycle {
	entry := sessionStarted(session, "m")
	entry.Session.Scope = scope
	entry.Placement.Scope = scope
	return entry
}

// TestScopeUsageSumsTheSessionsInScope is definition-of-done item 4's
// question: the usage of a scope is the latest running total of every
// session in it or nested under it, and the root scope is the whole run.
func TestScopeUsageSumsTheSessionsInScope(t *testing.T) {
	store := openStore(t)
	// Three sessions in three scopes, one of them nested under another, plus
	// one whose name merely starts with the same letters and is not nested.
	store.Lifecycle(inScopeSession("root", ""))
	store.Lifecycle(inScopeSession("lap", "lap.1"))
	store.Lifecycle(inScopeSession("nested", "lap.1/bakeoff.1/attempt.2"))
	store.Lifecycle(inScopeSession("sibling", "lap.10"))

	events := []struct {
		session string
		native  string
		event   json.RawMessage
	}{
		{"root", "ses_root", usageUpdatedEvent("ses_root", 1, 10, 1, 0, 0, 0)},
		{"lap", "ses_lap", usageUpdatedEvent("ses_lap", 0.5, 20, 2, 3, 4, 5)},
		// The session's second report replaces its first: the event is the
		// running total, not an increment.
		{"lap", "ses_lap", usageUpdatedEvent("ses_lap", 2, 200, 20, 30, 40, 50)},
		{"nested", "ses_nested", usageUpdatedEvent("ses_nested", 0, 7, 0, 0, 0, 0)},
		{"sibling", "ses_sib", usageUpdatedEvent("ses_sib", 100, 1000, 0, 0, 0, 0)},
	}
	for _, entry := range events {
		at := Placement{Scope: store.Snapshot().Run.Sessions[entry.session].Scope, Session: entry.session, Turn: entry.session + "-turn"}
		if err := store.Event(at, entry.event, nil); err != nil {
			t.Fatalf("usage event for %s: %v", entry.session, err)
		}
	}

	for _, test := range []struct {
		name  string
		scope string
		want  Usage
	}{
		{"the root scope is the whole run", "", Usage{
			Cost:   103,
			Tokens: Tokens{Input: 1217, Output: 21, Reasoning: 30, Cache: Cache{Read: 40, Write: 50}},
		}},
		{"a scope carries the scopes nested under it", "lap.1", Usage{
			Cost:   2,
			Tokens: Tokens{Input: 207, Output: 20, Reasoning: 30, Cache: Cache{Read: 40, Write: 50}},
		}},
		{"a nested scope carries only itself", "lap.1/bakeoff.1/attempt.2", Usage{
			Tokens: Tokens{Input: 7},
		}},
		{"a shared prefix is not a nesting", "lap.10", Usage{
			Cost:   100,
			Tokens: Tokens{Input: 1000},
		}},
		{"a scope no session ran in is zero", "lap.2", Usage{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := store.ScopeUsage(test.scope); got != test.want {
				t.Fatalf("scope %q usage is %+v, want %+v", test.scope, got, test.want)
			}
			// The snapshot answers the same question, which is what a
			// finished run's checkpoint is read back for.
			if got := store.Snapshot().Run.ScopeUsage(test.scope); got != test.want {
				t.Fatalf("scope %q usage from the snapshot is %+v, want %+v", test.scope, got, test.want)
			}
		})
	}
}
