package gimble

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAgentRecordUsesNativeEventAndOuterNativeRef(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	record := AgentRecord{
		Seq: 1, Time: when, Scope: "lap.1", Session: "coder.1", Turn: "coder.1/turn.1",
		Event: AgentEvent{
			Type: "session.execution.started", ID: "evt_1", Created: when.UnixMilli(),
			Data: json.RawMessage(`{"sessionID":"ses_1"}`),
		},
		NativeRef: json.RawMessage(`{"provider":"fixture"}`),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"kind"`)) || !bytes.Contains(raw, []byte(`"type":"session.execution.started"`)) {
		t.Fatalf("record is not a native session event: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"native_ref":{"provider":"fixture"}`)) {
		t.Fatalf("record dropped outer native_ref: %s", raw)
	}
	var decoded AgentRecord
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Event.Type != record.Event.Type || !bytes.Equal(decoded.NativeRef, record.NativeRef) {
		t.Fatalf("decoded record = %#v", decoded)
	}
}

func TestRootIngressStampsAndMapsIdentityMonotonically(t *testing.T) {
	s := &Session{id: "lap.1/coder.1", name: "coder"}
	first, err := s.stampAgentEvent(AgentEvent{
		Type: "session.step.started", ID: "provider-id", Created: 1,
		Data:      json.RawMessage(`{"sessionID":"native-session","assistantMessageID":"response-1","agent":"provider","model":{"providerID":"openai","id":"gpt"}}`),
		NativeRef: json.RawMessage(`{"provider":"fixture","sessionID":"native-session","messageID":"response-1","responseID":"resp_1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.stampAgentEvent(AgentEvent{Type: "session.step.streamed", Data: json.RawMessage(`{"sessionID":"native-session","assistantMessageID":"response-1"}`), NativeRef: json.RawMessage(`{"provider":"fixture","sessionID":"native-session","messageID":"response-1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.stampAgentEvent(AgentEvent{Type: "session.step.started", Data: json.RawMessage(`{"sessionID":"native-session","assistantMessageID":"response-2","agent":"provider","model":{"providerID":"openai","id":"gpt"}}`), NativeRef: json.RawMessage(`{"provider":"fixture","sessionID":"native-session","messageID":"response-2"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var a, b, c map[string]any
	_ = json.Unmarshal(first.Data, &a)
	_ = json.Unmarshal(second.Data, &b)
	_ = json.Unmarshal(third.Data, &c)
	if a["sessionID"] != b["sessionID"] || a["agent"] != "coder" || !strings.HasPrefix(fmt.Sprint(a["sessionID"]), "ses_") {
		t.Fatalf("root mapping = %#v then %#v", a, b)
	}
	if a["assistantMessageID"] != b["assistantMessageID"] || !(fmt.Sprint(a["assistantMessageID"]) < fmt.Sprint(c["assistantMessageID"])) {
		t.Fatalf("message mappings are not stable and monotonic: %v %v %v", a["assistantMessageID"], b["assistantMessageID"], c["assistantMessageID"])
	}
	if a["assistantMessageID"] != messageIDFromEvent(first.ID) || c["assistantMessageID"] != messageIDFromEvent(third.ID) {
		t.Fatalf("first-seen messages did not inherit their event order: %v from %s, %v from %s", a["assistantMessageID"], first.ID, c["assistantMessageID"], third.ID)
	}
	if first.ID == "provider-id" || first.Created == 1 || !bytes.Contains(first.NativeRef, []byte(`"responseID":"resp_1"`)) || bytes.Contains(first.NativeRef, []byte(`"identity"`)) || !bytes.Contains(first.NativeRef, []byte(`"normalizedMessageID":"msg_`)) {
		t.Fatalf("root did not replace envelope ownership while preserving native ref: %#v", first)
	}
}

func messageIDFromEvent(eventID string) string {
	return "msg_" + strings.TrimPrefix(eventID, "evt_")
}

func TestLifecycleEventUnionRoundTripsEveryVariant(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		kind  string
		event LifecycleEvent
	}{
		{"run_started", RunStarted{Name: "sprint"}},
		{"run_ended", RunEnded{Name: "sprint", Error: ""}},
		{"run_cancelled", RunCancelled{Name: "sprint", Source: "operator", Error: "context canceled"}},
		{"scope_began", ScopeBegan{Name: "task", Task: optionalTask(Task{Name: "Implement", Description: "Make it work.", DefinitionOfDone: "It works."})}},
		{"scope_ended", ScopeEnded{Error: ""}},
		{"planner_decision", PlannerDecision{Task: optionalTask(Task{Name: "Implement", Description: "Make events durable.", DefinitionOfDone: "The event is recorded."})}},
		{"value_set", ValueSet{Key: "goal", Value: JSONText(`"ship"`)}},
		{"session_created", SessionCreated{Name: "coder", Adapter: "codex", Model: "gpt", Workdir: "/work", Parent: "researcher.1"}},
		{"session_closed", SessionClosed{}},
		{"turn_started", TurnStarted{Prompt: "build", OutputType: "gimble.Text"}},
		{"turn_ended", TurnEnded{Result: JSONText(`"done"`), Usage: []ModelUsage{{Model: "m", Cost: 0.5, Tokens: Tokens{Input: 1}}}, Duration: time.Second}},
		{"supervise_attached", SuperviseAttached{Reviewer: "reviewer.1", Worker: "worker.1/turn.1", Instruction: "watch", Interval: time.Minute}},
		{"steer", Steer{Target: "worker.1", Source: "reviewer.1", Message: "fix it", Landed: true}},
		{"interrupt", Interrupt{Target: "worker.1", Source: "operator"}},
		{"complete", Complete{}},
	}

	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			record := LifecycleRecord{Seq: 1, Time: when, Scope: "lap.1", Session: optionalString("coder.1"), Turn: optionalString("coder.1/turn.1"), Event: test.event}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte(`"kind":"`+test.kind+`"`)) {
				t.Fatalf("record lacks discriminator %q: %s", test.kind, raw)
			}
			if err := record.ValidateJSON(raw); err != nil {
				t.Fatalf("generated schema rejected %s: %v", raw, err)
			}
			var decoded LifecycleRecord
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if got := lifecycleKind(decoded.Event); got != test.kind {
				t.Fatalf("decoded kind = %q, want %q", got, test.kind)
			}
			roundTrip, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(roundTrip, raw) {
				t.Fatalf("round trip changed record:\n%s\n%s", raw, roundTrip)
			}
		})
	}
}
