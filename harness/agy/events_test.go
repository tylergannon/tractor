package agy

import (
	"testing"

	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/agy/schema"
)

func TestEventProjectorCoalescesAndPairs(t *testing.T) {
	var events []harness.Event
	projector := newEventProjector(func(event harness.Event) { events = append(events, event) })
	projector.user([]harness.ContentPart{{Type: harness.ContentPartText, Text: "hello"}})
	projector.envelope(schema.Envelope{StepUpdate: &schema.StepUpdate{StepIndex: new(1), StepType: new("agent_response"), State: new("ACTIVE"), TextDelta: new("hel")}})
	projector.envelope(schema.Envelope{StepUpdate: &schema.StepUpdate{StepIndex: new(1), StepType: new("agent_response"), State: new("DONE"), TextDelta: new("lo")}})
	projector.envelope(schema.Envelope{StepUpdate: &schema.StepUpdate{StepIndex: new(2), StepType: new("tool"), State: new("DONE"), ToolInfo: &schema.ToolInfo{Name: new("read"), Parameters: map[string]any{"path": "x"}, Output: "ok"}}})
	if len(events) != 4 {
		t.Fatalf("events = %#v", events)
	}
	if events[1]["type"] != harness.EventAssistant || events[1]["text"] != "hello" {
		t.Fatalf("assistant = %#v", events[1])
	}
	if events[2]["call_id"] != "step-2" || events[3]["call_id"] != "step-2" {
		t.Fatalf("tool pair = %#v %#v", events[2], events[3])
	}
}
