package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tylergannon/gimble"
)

func TestProjectorBindsRawResponseAndNormalizesTokens(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("thread-1", "turn-1", "gpt-test", func(event gimble.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	mustProject(t, p.itemStarted(json.RawMessage(`{"item":{"id":"msg-item","type":"agentMessage"}}`)))
	mustProject(t, p.textDelta(json.RawMessage(`{"delta":"hel"}`)))
	_, _, err := p.itemCompleted(json.RawMessage(`{"item":{"id":"msg-item","type":"agentMessage","text":"hello"}}`))
	mustProject(t, err)
	mustProject(t, p.rawResponseCompleted(json.RawMessage(`{"responseId":"resp_123","usage":{"inputTokens":100,"cachedInputTokens":25,"cacheWriteInputTokens":5,"outputTokens":30,"reasoningOutputTokens":10}}`)))
	if !slices.Contains(types(events), "session.step.ended") {
		t.Fatal("settled rawResponse/completed did not end the tool-free step")
	}

	want := []string{"session.step.started", "session.text.started", "session.text.delta", "session.text.ended", "session.step.streamed", "session.step.ended"}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	var started, streamed, ended map[string]any
	startedEvent := firstType(t, events, "session.step.started")
	streamedEvent := firstType(t, events, "session.step.streamed")
	endedEvent := firstType(t, events, "session.step.ended")
	decodeData(t, startedEvent, &started)
	decodeData(t, streamedEvent, &streamed)
	decodeData(t, endedEvent, &ended)
	if started["assistantMessageID"] != streamed["assistantMessageID"] || streamed["assistantMessageID"] != ended["assistantMessageID"] {
		t.Fatalf("provisional identity changed: %#v %#v %#v", started, streamed, ended)
	}
	if !jsonContains(streamedEvent.NativeRef, `"responseID":"resp_123"`) {
		t.Fatalf("response boundary not bound: streamed=%s", streamedEvent.NativeRef)
	}
	if ended["cost"] != float64(0) {
		t.Fatalf("step cost = %#v, want 0: Codex states no cost", ended["cost"])
	}
	tokens := ended["tokens"].(map[string]any)
	cache := tokens["cache"].(map[string]any)
	if tokens["input"] != float64(75) || tokens["output"] != float64(20) || tokens["reasoning"] != float64(10) || cache["read"] != float64(25) || cache["write"] != float64(5) {
		t.Fatalf("normalized tokens = %#v", tokens)
	}
}

func TestProjectorWaitsForToolAndDistinguishesFailure(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("thread-1", "turn-1", "gpt-test", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	mustProject(t, p.itemStarted(json.RawMessage(`{"item":{"id":"call-1","type":"commandExecution","command":"false","cwd":"/w"}}`)))
	mustProject(t, p.rawResponseCompleted(json.RawMessage(`{"responseId":"resp_tool"}`)))
	if slices.Contains(types(events), "session.step.ended") {
		t.Fatal("step ended with a tool still open even without tokenUsage")
	}
	_, _, err := p.itemCompleted(json.RawMessage(`{"item":{"id":"call-1","type":"commandExecution","status":"failed","aggregatedOutput":"boom"}}`))
	mustProject(t, err)
	if got := types(events); got[len(got)-2] != "session.tool.failed" || got[len(got)-1] != "session.step.ended" {
		t.Fatalf("terminal event types = %v", got)
	}

	before := len(events)
	mustProject(t, p.itemStarted(json.RawMessage(`{"item":{"id":"compact-1","type":"contextCompaction"}}`)))
	mustProject(t, p.rawResponseCompleted(json.RawMessage(`{"responseId":"resp_compact","usage":{}}`)))
	_, _, err = p.itemCompleted(json.RawMessage(`{"item":{"id":"compact-1","type":"contextCompaction"}}`))
	mustProject(t, err)
	if got := types(events[before:]); len(got) != 0 {
		t.Fatalf("compaction produced ordinary assistant events: %v", types(events[before:]))
	}
}

func TestProjectorCompletesTwoResponsesWithoutTokenUsage(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("thread", "turn", "model", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	for i, response := range []string{"resp_one", "resp_two"} {
		item := fmt.Sprintf("item-%d", i)
		mustProject(t, p.itemStarted(json.RawMessage(fmt.Sprintf(`{"item":{"id":%q,"type":"agentMessage"}}`, item))))
		_, _, err := p.itemCompleted(json.RawMessage(fmt.Sprintf(`{"item":{"id":%q,"type":"agentMessage","text":"done"}}`, item)))
		mustProject(t, err)
		mustProject(t, p.rawResponseCompleted(json.RawMessage(fmt.Sprintf(`{"responseId":%q}`, response))))
	}
	if got := countType(events, "session.step.ended"); got != 2 {
		t.Fatalf("step.ended count = %d, want 2; types=%v", got, types(events))
	}
	var ids []string
	for _, event := range events {
		if event.Type != "session.step.ended" {
			continue
		}
		var data map[string]any
		decodeData(t, event, &data)
		ids = append(ids, data["assistantMessageID"].(string))
	}
	if ids[0] == ids[1] {
		t.Fatalf("responses reused message identity: %v", ids)
	}
}

func TestProjectorCompletesResumedTurnWithoutRawResponse(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("thread", "turn", "model", func(event gimble.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	mustProject(t, p.itemStarted(json.RawMessage(`{"item":{"id":"message","type":"agentMessage"}}`)))
	_, _, err := p.itemCompleted(json.RawMessage(`{"item":{"id":"message","type":"agentMessage","text":"done"}}`))
	mustProject(t, err)
	mustProject(t, p.turnCompleted(json.RawMessage(`{"turn":{"status":"completed"}}`)))

	if got := countType(events, "session.step.ended"); got != 1 {
		t.Fatalf("step.ended count = %d, want 1; types=%v", got, types(events))
	}
}

// The sample is thread/tokenUsage/updated verbatim from
// ephemeral/research/issue-149/codex/turn2-sameproc.jsonl, where total is the
// process's running sum and last is that turn's one model call.
func TestProjectorFillsStepFromRecordedTokenUsage(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("01a0988c-e92a-7be3-a0dd-98e708d1b6e5", "01a0988d-1c26-78b2-a0fc-5e97d2295747", "gpt-5.6-luna", func(event gimble.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	mustProject(t, p.itemStarted(json.RawMessage(`{"item":{"id":"message","type":"agentMessage"}}`)))
	_, _, err := p.itemCompleted(json.RawMessage(`{"item":{"id":"message","type":"agentMessage","text":"done"}}`))
	mustProject(t, err)
	mustProject(t, p.tokenUsageUpdated(json.RawMessage(`{"threadId":"01a0988c-e92a-7be3-a0dd-98e708d1b6e5","turnId":"01a0988d-1c26-78b2-a0fc-5e97d2295747","tokenUsage":{"total":{"totalTokens":40531,"inputTokens":40508,"cachedInputTokens":29184,"cacheWriteInputTokens":0,"outputTokens":23,"reasoningOutputTokens":11},"last":{"totalTokens":20285,"inputTokens":20267,"cachedInputTokens":19200,"cacheWriteInputTokens":0,"outputTokens":18,"reasoningOutputTokens":11},"modelContextWindow":258400}}`)))
	mustProject(t, p.turnCompleted(json.RawMessage(`{"turn":{"status":"completed"}}`)))

	var ended map[string]any
	decodeData(t, firstType(t, events, "session.step.ended"), &ended)
	tokens := ended["tokens"].(map[string]any)
	cache := tokens["cache"].(map[string]any)
	if tokens["input"] != float64(1067) || tokens["output"] != float64(7) || tokens["reasoning"] != float64(11) || cache["read"] != float64(19200) || cache["write"] != float64(0) {
		t.Fatalf("tokens from tokenUsage.last = %#v", tokens)
	}
}

func TestProjectorRejectsSecondOpenTextAndPropagatesCallbackError(t *testing.T) {
	p := newProjector("thread", "turn", "model", func(gimble.AgentEvent) error { return nil })
	mustProject(t, p.itemStarted(json.RawMessage(`{"item":{"id":"one","type":"agentMessage"}}`)))
	if err := p.itemStarted(json.RawMessage(`{"item":{"id":"two","type":"agentMessage"}}`)); err == nil {
		t.Fatal("second open text part was accepted")
	}

	boom := errors.New("observer stopped")
	p = newProjector("thread", "turn", "model", func(gimble.AgentEvent) error { return boom })
	if err := p.itemStarted(json.RawMessage(`{"item":{"id":"one","type":"agentMessage"}}`)); !errors.Is(err, boom) {
		t.Fatalf("callback error = %v, want %v", err, boom)
	}
}

func types(events []gimble.AgentEvent) []string {
	var out []string
	for _, event := range events {
		if event.Type != "" {
			out = append(out, event.Type)
		}
	}
	return out
}

func decodeData(t *testing.T, event gimble.AgentEvent, target any) {
	t.Helper()
	if err := json.Unmarshal(event.Data, target); err != nil {
		t.Fatal(err)
	}
}

func firstType(t *testing.T, events []gimble.AgentEvent, eventType string) gimble.AgentEvent {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("event %s not found", eventType)
	return gimble.AgentEvent{}
}

func countType(events []gimble.AgentEvent, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func mustProject(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func jsonContains(raw json.RawMessage, text string) bool { return bytes.Contains(raw, []byte(text)) }
