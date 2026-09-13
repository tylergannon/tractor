package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tylergannon/gimble"
)

func TestRawProjectorUsesNestedMessageIDAndExactToolUseID(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("session-1", "claude-test", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	fixtures := []string{
		`{"type":"stream_event","uuid":"stream-envelope","session_id":"session-1","event":{"type":"message_start","message":{"id":"msg_native","model":"claude-native","usage":{"input_tokens":40,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}}`,
		`{"type":"stream_event","uuid":"stream-envelope","session_id":"session-1","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_actual","name":"Bash","input":{}}}}`,
		`{"type":"stream_event","uuid":"stream-envelope","session_id":"session-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"go test\"}"}}}`,
		`{"type":"stream_event","uuid":"stream-envelope","session_id":"session-1","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","uuid":"stream-envelope","session_id":"session-1","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12,"output_tokens_details":{"thinking_tokens":2}}}}`,
		`{"type":"stream_event","uuid":"stream-envelope","session_id":"session-1","event":{"type":"message_stop"}}`,
		`{"type":"assistant","uuid":"outer-assistant-uuid","session_id":"session-1","parent_tool_use_id":null,"message":{"id":"msg_native","content":[{"type":"tool_use","id":"toolu_actual","name":"Bash","input":{"command":"go test"}}]}}`,
	}
	for _, fixture := range fixtures {
		mustRaw(t, p.raw(json.RawMessage(fixture)))
	}
	if slices.Contains(claudeTypes(events), "session.step.ended") {
		t.Fatal("message_stop ended the step before its tool result")
	}
	mustRaw(t, p.raw(json.RawMessage(`{"type":"tool_progress","uuid":"progress-uuid","session_id":"session-1","tool_use_id":"toolu_actual","tool_name":"Bash","parent_tool_use_id":"ancestry-only","elapsed_time_seconds":1.5}`)))
	mustRaw(t, p.raw(json.RawMessage(`{"type":"user","uuid":"tool-result-envelope","session_id":"session-1","parent_tool_use_id":"ancestry-only","message":{"id":"user-message","content":[{"type":"tool_result","tool_use_id":"toolu_actual","content":"ok","is_error":false}]}}`)))

	wantTail := []string{"session.tool.progress", "session.tool.success", "session.step.ended"}
	got := claudeTypes(events)
	if !slices.Equal(got[len(got)-3:], wantTail) {
		t.Fatalf("event types = %v", got)
	}
	var start, success, ended map[string]any
	claudeData(t, events[0], &start)
	claudeData(t, events[len(events)-2], &success)
	claudeData(t, events[len(events)-1], &ended)
	if start["assistantMessageID"] != "msg_native" || success["id"] != "toolu_actual" {
		t.Fatalf("identity normalization used envelope/ancestry IDs: start=%#v success=%#v", start, success)
	}
	if !bytes.Contains(events[len(events)-2].NativeRef, []byte(`"parentToolUseID":"ancestry-only"`)) {
		t.Fatalf("native tool identity missing: %s", events[len(events)-2].NativeRef)
	}
	if ended["finish"] != "tool-calls" {
		t.Fatalf("finish = %v", ended["finish"])
	}
	tokens := ended["tokens"].(map[string]any)
	cache := tokens["cache"].(map[string]any)
	if tokens["input"] != float64(40) || tokens["output"] != float64(10) || tokens["reasoning"] != float64(2) || cache["read"] != float64(10) || cache["write"] != float64(5) {
		t.Fatalf("tokens = %#v", tokens)
	}
}

func TestRawProjectorStreamsTextAndZeroFillsAbsentUsage(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("session", "model", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	for _, fixture := range []string{
		`{"type":"stream_event","uuid":"outer","event":{"type":"message_start","message":{"id":"nested","model":"model"}}}`,
		`{"type":"stream_event","uuid":"outer","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","uuid":"outer","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"stream_event","uuid":"outer","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","uuid":"outer","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
		`{"type":"stream_event","uuid":"outer","event":{"type":"message_stop"}}`,
	} {
		mustRaw(t, p.raw(json.RawMessage(fixture)))
	}
	got := claudeTypes(events)
	want := []string{"session.step.started", "session.text.started", "session.text.delta", "session.text.ended", "session.step.streamed", "session.step.ended"}
	if !slices.Equal(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	ended := events[len(events)-1]
	if !bytes.Contains(ended.Data, []byte(`"cost":0`)) || !bytes.Contains(ended.Data, []byte(`"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}`)) {
		t.Fatalf("absent usage was not zero-filled: %s", ended.Data)
	}
	if bytes.Contains(ended.NativeRef, []byte("accounting")) {
		t.Fatalf("native ref still carries an accounting sidecar: %s", ended.NativeRef)
	}
}

// The fixture is the accounting half of the `result` message recorded in
// ephemeral/research/issue-149/claude/turn1.jsonl, verbatim.
const resultFixture = `{"type":"result","subtype":"success","session_id":"session","total_cost_usd":0.0241153,` +
	`"usage":{"input_tokens":10,"cache_creation_input_tokens":10341,"cache_read_input_tokens":25183,"output_tokens":181,` +
	`"output_tokens_details":{"thinking_tokens":118},"service_tier":"standard","iterations":[{"input_tokens":10,"output_tokens":181,` +
	`"cache_read_input_tokens":25183,"cache_creation_input_tokens":10341,"type":"message"}]},` +
	`"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":10,"outputTokens":181,"cacheReadInputTokens":25183,` +
	`"cacheCreationInputTokens":10341,"webSearchRequests":0,"costUSD":0.0241153,"contextWindow":200000,"maxOutputTokens":32000,` +
	`"thinkingTokens":118,"canonicalModel":"claude-haiku-4-5","provider":"firstParty","costBasis":"list"}},` +
	`"num_turns":1,"is_error":false,"result":"Paris is the capital of France."}`

func TestRawProjectorReportsTurnUsageFromResult(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("session", "haiku", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	mustRaw(t, p.raw(json.RawMessage(resultFixture)))
	if len(events) != 0 {
		t.Fatalf("result emitted events: %v", claudeTypes(events))
	}

	want := gimble.Usage{Cost: 0.0241153}
	want.Tokens.Input, want.Tokens.Output, want.Tokens.Reasoning = 10, 63, 118
	want.Tokens.Cache.Read, want.Tokens.Cache.Write = 25183, 10341
	got := p.turnUsage()
	if len(got) != 1 || got["claude-haiku-4-5-20251001"] != want {
		t.Fatalf("turn report = %#v, want one entry %#v", got, want)
	}

	// With no modelUsage the fallback is one entry under the session's model,
	// from result.usage and total_cost_usd.
	var trimmed map[string]any
	if err := json.Unmarshal([]byte(resultFixture), &trimmed); err != nil {
		t.Fatal(err)
	}
	delete(trimmed, "modelUsage")
	raw, err := json.Marshal(trimmed)
	if err != nil {
		t.Fatal(err)
	}
	p = newProjector("session", "haiku", func(gimble.AgentEvent) error { return nil })
	mustRaw(t, p.raw(raw))
	if got := p.turnUsage(); len(got) != 1 || got["haiku"] != want {
		t.Fatalf("fallback turn report = %#v, want one entry %#v", got, want)
	}

	// A turn that states nothing reports nothing.
	p = newProjector("session", "haiku", func(gimble.AgentEvent) error { return nil })
	mustRaw(t, p.raw(json.RawMessage(`{"type":"result","subtype":"success","session_id":"session","result":"done"}`)))
	if got := p.turnUsage(); got != nil {
		t.Fatalf("turn report = %#v, want nil", got)
	}
}

func TestRawProjectorDistinguishesToolErrorAndGuardsSingleOpen(t *testing.T) {
	var events []gimble.AgentEvent
	p := newProjector("session", "model", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	mustRaw(t, p.raw(json.RawMessage(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"nested","model":"model","usage":{}}}}`)))
	mustRaw(t, p.raw(json.RawMessage(`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`)))
	if err := p.raw(json.RawMessage(`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text"}}}`)); err == nil {
		t.Fatal("second open text block was accepted")
	}

	events = nil
	p = newProjector("session", "model", func(event gimble.AgentEvent) error { events = append(events, event); return nil })
	for _, fixture := range []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"tool-message","model":"model","usage":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_error","name":"Bash","input":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"user","parent_tool_use_id":"ancestry","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_error","content":"permission denied","is_error":true}]}}`,
	} {
		mustRaw(t, p.raw(json.RawMessage(fixture)))
	}
	if got := claudeTypes(events); got[len(got)-2] != "session.tool.failed" || got[len(got)-1] != "session.step.ended" {
		t.Fatalf("tool error event types = %v", got)
	}

	boom := errors.New("observer stopped")
	p = newProjector("session", "model", func(gimble.AgentEvent) error { return boom })
	if err := p.raw(json.RawMessage(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"nested","model":"model"}}}`)); !errors.Is(err, boom) {
		t.Fatalf("callback error = %v, want %v", err, boom)
	}
}

func claudeTypes(events []gimble.AgentEvent) []string {
	var result []string
	for _, event := range events {
		if event.Type != "" {
			result = append(result, event.Type)
		}
	}
	return result
}

func claudeData(t *testing.T, event gimble.AgentEvent, target any) {
	t.Helper()
	if err := json.Unmarshal(event.Data, target); err != nil {
		t.Fatal(err)
	}
}

func mustRaw(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
