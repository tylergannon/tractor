package gimble_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tylergannon/gimble"
)

func TestPublishedEventTypesAreUsableOutsideGimble(t *testing.T) {
	lifecycle := []gimble.LifecycleEvent{
		gimble.RunStarted{},
		gimble.RunEnded{},
		gimble.RunCancelled{},
		gimble.ScopeBegan{},
		gimble.ScopeEnded{},
		gimble.PlannerDecision{},
		gimble.ValueSet{},
		gimble.SessionCreated{},
		gimble.SessionClosed{},
		gimble.TurnStarted{},
		gimble.TurnEnded{Usage: []gimble.ModelUsage{}},
		gimble.SuperviseAttached{},
		gimble.Steer{},
		gimble.Interrupt{},
		gimble.Complete{},
	}
	agent := gimble.AgentEvent{Type: "session.execution.started", ID: "evt_1", Created: 1, Data: json.RawMessage(`{"sessionID":"ses_1"}`)}

	lifecycleRaw, err := json.Marshal(gimble.LifecycleRecord{Seq: 1, Time: time.Now().UTC(), Event: lifecycle[0]})
	if err != nil {
		t.Fatal(err)
	}
	var lifecycleRecord gimble.LifecycleRecord
	if err := json.Unmarshal(lifecycleRaw, &lifecycleRecord); err != nil {
		t.Fatal(err)
	}
	if _, ok := lifecycleRecord.Event.(gimble.RunStarted); !ok {
		t.Fatalf("decoded lifecycle event = %T", lifecycleRecord.Event)
	}

	agentRaw, err := json.Marshal(gimble.AgentRecord{Seq: 1, Time: time.Now().UTC(), Session: "coder.1", Turn: "coder.1/turn.1", Event: agent})
	if err != nil {
		t.Fatal(err)
	}
	var agentRecord gimble.AgentRecord
	if err := json.Unmarshal(agentRaw, &agentRecord); err != nil {
		t.Fatal(err)
	}
	if agentRecord.Event.Type != "session.execution.started" {
		t.Fatalf("decoded agent event = %#v", agentRecord.Event)
	}
}
