package sessionstate

import (
	"encoding/json"
	"testing"
)

// TestUsageUpdatedIsTheSessionRunningTotal replays the shared
// usage-running-total fixture, which the TypeScript suite replays too. The
// core publishes session.usage.updated after every step.ended, after every
// step.failed that carries usage, and again after the harness turn report, so
// the last one is the session total and each assistant message keeps the
// tokens of its own step.
func TestUsageUpdatedIsTheSessionRunningTotal(t *testing.T) {
	const sessionID = "ses_usage"
	fixture := loadFixture(t, "usage-running-total.json")
	projection := newFromFixture(t, fixture)
	for _, entry := range arrOf(fixture.Get("events")) {
		projection.Apply(objOf(entry))
	}

	info := projection.state.Info[sessionID]
	if info == nil {
		t.Fatalf("session %s is missing from the projection", sessionID)
	}
	requireJSON(t, "session cost", info.Get("cost"), `0.0301`)
	requireJSON(t, "session tokens", info.Get("tokens"),
		`{"input":147,"output":33,"reasoning":5,"cache":{"read":210,"write":30}}`)

	for _, want := range []struct {
		id     string
		cost   string
		tokens string
	}{
		{"msg_step_1", `0`, `{"input":100,"output":20,"reasoning":5,"cache":{"read":10,"write":30}}`},
		{"msg_step_2", `0.0125`, `{"input":40,"output":10,"reasoning":0,"cache":{"read":200,"write":0}}`},
		{"msg_step_3", `0.004`, `{"input":7,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}`},
	} {
		message := projection.message(sessionID, want.id)
		if message == nil {
			t.Fatalf("assistant message %s is missing", want.id)
		}
		requireJSON(t, want.id+" cost", message.Get("cost"), want.cost)
		requireJSON(t, want.id+" tokens", message.Get("tokens"), want.tokens)
	}
}

// TestUsageUpdatedWithoutTheSessionChangesNothing holds the guard both
// reducers share: a total for a session this projection has never seen is
// dropped rather than creating a row.
func TestUsageUpdatedWithoutTheSessionChangesNothing(t *testing.T) {
	projection := New(NewProjectionState())
	projection.Apply(nativeEvent("evt_1", "session.usage.updated",
		"sessionID", "ses_absent", "cost", json.Number("1"),
		"tokens", obj("input", json.Number("1"), "output", json.Number("1"),
			"reasoning", json.Number("0"), "cache", obj("read", json.Number("0"), "write", json.Number("0")))))
	if _, ok := projection.state.Info["ses_absent"]; ok {
		t.Fatal("a usage total invented a session row")
	}
}

func requireJSON(t *testing.T, label string, got any, want string) {
	t.Helper()
	encoded, err := marshalValue(got)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if !jsonEqual(t, string(encoded), want) {
		t.Fatalf("%s is %s, want %s", label, encoded, want)
	}
}

func jsonEqual(t *testing.T, got, want string) bool {
	t.Helper()
	left, err := json.Marshal(canonical(t, got))
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(canonical(t, want))
	if err != nil {
		t.Fatal(err)
	}
	return string(left) == string(right)
}
