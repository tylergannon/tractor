package claude

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	claudeagent "github.com/roasbeef/claude-agent-sdk-go"
	"github.com/tylergannon/tractor/harness"
)

var testSchema = json.RawMessage(`{
	"type":"object",
	"properties":{"next":{"type":"string"},"notes":{"type":"string"}},
	"required":["next","notes"],
	"additionalProperties":false
}`)

type fakeSession struct {
	messages  chan claudeagent.Message
	onSend    func(string)
	onStop    func()
	closeOnce sync.Once
	closed    chan struct{}
}

func newFakeSession(buffer int) *fakeSession {
	return &fakeSession{messages: make(chan claudeagent.Message, buffer), closed: make(chan struct{})}
}

func (s *fakeSession) Send(_ context.Context, prompt string) error {
	if s.onSend != nil {
		s.onSend(prompt)
	}
	return nil
}

func (s *fakeSession) Messages() iter.Seq[claudeagent.Message] {
	return func(yield func(claudeagent.Message) bool) {
		for {
			select {
			case message := <-s.messages:
				if !yield(message) {
					return
				}
			case <-s.closed:
				return
			}
		}
	}
}

func (s *fakeSession) Errors() <-chan error { return nil }

func (s *fakeSession) InterruptWithReceipt(context.Context) (*claudeagent.InterruptReceipt, error) {
	if s.onStop != nil {
		s.onStop()
	}
	return &claudeagent.InterruptReceipt{}, nil
}

func (s *fakeSession) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func TestFreshSessionPromotesOnlyAfterPostInitMessage(t *testing.T) {
	var mu sync.Mutex
	var configs []nativeConfig
	adapter := newAdapter(func(_ context.Context, config nativeConfig) (nativeSession, error) {
		mu.Lock()
		configs = append(configs, config)
		mu.Unlock()
		session := newFakeSession(4)
		session.onSend = func(string) {
			session.messages <- claudeagent.SystemMessage{Type: "system", Subtype: "init", SessionID: config.sessionID}
			session.messages <- claudeagent.RateLimitEventMessage{Type: "rate_limit_event", SessionID: config.sessionID}
			session.messages <- successResult(config.sessionID)
		}
		return session, nil
	})

	sessionID, createErr := adapter.CreateSession("fable", t.TempDir())
	if createErr != nil {
		t.Fatal(createErr)
	}
	input := validInput(sessionID, adapter.states[sessionID].workdir)
	for range 2 {
		if _, runErr := adapter.RunTurn(input, func(harness.Event) {}); runErr != nil {
			t.Fatal(runErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(configs) != 2 || !configs[0].fresh || configs[1].fresh {
		t.Fatalf("native configs = %#v, want fresh then resume", configs)
	}
}

func TestRunTurnProjectsCompleteEventsAndValidatesResult(t *testing.T) {
	const sessionID = "550e8400-e29b-41d4-a716-446655440000"
	session := newFakeSession(8)
	session.onSend = func(string) {
		assistant := claudeagent.AssistantMessage{Type: "assistant", SessionID: sessionID}
		assistant.Message.Content = []claudeagent.ContentBlock{
			{Type: "thinking", Text: "considered the file"},
			{Type: "tool_use", ID: "tool-1", Name: "Read", Input: json.RawMessage(`{"file_path":"nonce.txt"}`)},
			{Type: "text", Text: "Finished."},
		}
		session.messages <- claudeagent.SystemMessage{Type: "system", Subtype: "init", SessionID: sessionID}
		session.messages <- assistant
		session.messages <- claudeagent.UserMessage{
			Type: "user", SessionID: sessionID,
			ToolUseResult: map[string]any{"content": "the nonce"},
		}
		session.messages <- successResult(sessionID)
	}
	adapter := newAdapter(func(context.Context, nativeConfig) (nativeSession, error) { return session, nil })
	var events []harness.Event
	result, runErr := adapter.RunTurn(validInput(sessionID, t.TempDir()), func(event harness.Event) {
		events = append(events, event)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if result["next"] != "done" || result["notes"] != "proved" {
		t.Fatalf("result = %#v", result)
	}
	wantTypes := []string{"user", "thinking", "tool_call", "assistant", "tool_result"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v", events)
	}
	for index, want := range wantTypes {
		if events[index]["type"] != want {
			t.Fatalf("event %d type = %v, want %q", index, events[index]["type"], want)
		}
	}
	if events[2]["call_id"] != events[4]["call_id"] {
		t.Fatalf("tool events are not paired: %#v", events)
	}
}

func TestInitSessionMismatchIsTerminal(t *testing.T) {
	session := newFakeSession(2)
	session.onSend = func(string) {
		session.messages <- claudeagent.SystemMessage{Type: "system", Subtype: "init", SessionID: "different"}
	}
	adapter := newAdapter(func(context.Context, nativeConfig) (nativeSession, error) { return session, nil })
	_, runErr := adapter.RunTurn(validInput("expected", t.TempDir()), func(harness.Event) {})
	if runErr == nil || runErr.Category != harness.ErrorTerminal {
		t.Fatalf("run error = %#v, want terminal", runErr)
	}
}

func TestSteerEmitsOneUserEventAfterNativeAcceptance(t *testing.T) {
	const sessionID = "steer-session"
	session := newFakeSession(4)
	sends := make(chan string, 2)
	session.onSend = func(prompt string) { sends <- prompt }
	adapter := newAdapter(func(context.Context, nativeConfig) (nativeSession, error) { return session, nil })
	var mu sync.Mutex
	var events []harness.Event
	done := make(chan *harness.Error, 1)
	go func() {
		_, err := adapter.RunTurn(validInput(sessionID, t.TempDir()), func(event harness.Event) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		})
		done <- err
	}()
	<-sends
	parts := []harness.ContentPart{{Type: harness.ContentPartText, Text: "record steer-nonce"}}
	adapter.Steer(sessionID, parts)
	if got := <-sends; got != parts[0].Text {
		t.Fatalf("steering prompt = %q", got)
	}
	session.messages <- successResult(sessionID)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	userEvents := 0
	for _, event := range events {
		if event["type"] == harness.EventUser {
			userEvents++
		}
	}
	if userEvents != 2 {
		t.Fatalf("user event count = %d, events = %#v", userEvents, events)
	}
}

func TestInterruptAndTimeoutReturnInterrupted(t *testing.T) {
	for _, test := range []struct {
		name      string
		timeout   time.Duration
		interrupt bool
	}{
		{name: "interrupt", interrupt: true},
		{name: "timeout", timeout: 10 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			const sessionID = "interrupt-session"
			session := newFakeSession(3)
			started := make(chan struct{})
			session.onSend = func(string) {
				select {
				case <-started:
				default:
					close(started)
				}
			}
			session.onStop = func() {
				session.messages <- claudeagent.ResultMessage{Type: "result", Subtype: "error_during_execution", SessionID: sessionID}
			}
			adapter := newAdapter(func(context.Context, nativeConfig) (nativeSession, error) { return session, nil })
			input := validInput(sessionID, t.TempDir())
			input.Timeout = test.timeout
			done := make(chan *harness.Error, 1)
			go func() {
				_, err := adapter.RunTurn(input, func(harness.Event) {})
				done <- err
			}()
			<-started
			if test.interrupt {
				adapter.Interrupt(sessionID)
			}
			select {
			case runErr := <-done:
				if runErr == nil || runErr.Category != harness.ErrorInterrupted {
					t.Fatalf("run error = %#v, want interrupted", runErr)
				}
			case <-time.After(time.Second):
				t.Fatal("turn did not stop promptly")
			}
		})
	}
}

func TestCompactUsesTerminalNativeStatus(t *testing.T) {
	const sessionID = "compact-session"
	session := newFakeSession(3)
	session.onSend = func(prompt string) {
		if prompt != "/compact" {
			t.Errorf("compact prompt = %q", prompt)
		}
		compacting := claudeagent.SDKStatusCompacting
		session.messages <- claudeagent.StatusMessage{Type: "system", Subtype: "status", SessionID: sessionID, Status: &compacting}
		session.messages <- claudeagent.StatusMessage{Type: "system", Subtype: "status", SessionID: sessionID, CompactResult: claudeagent.CompactResultSuccess}
	}
	var config nativeConfig
	adapter := newAdapter(func(_ context.Context, got nativeConfig) (nativeSession, error) {
		config = got
		return session, nil
	})
	if err := adapter.Compact(sessionID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if config.fresh || config.outputSchema != nil {
		t.Fatalf("compact config = %#v, want native resume without output schema", config)
	}
}

func TestUnknownSessionFailureIsTerminal(t *testing.T) {
	adapter := newAdapter(func(context.Context, nativeConfig) (nativeSession, error) {
		return nil, errors.New("No conversation found with session ID")
	})
	_, runErr := adapter.RunTurn(validInput("unknown", t.TempDir()), func(harness.Event) {})
	if runErr == nil || runErr.Category != harness.ErrorTerminal {
		t.Fatalf("run error = %#v, want terminal", runErr)
	}
}

func validInput(sessionID, workdir string) harness.RunTurnInput {
	return harness.RunTurnInput{
		SessionID: sessionID, Model: "fable", ReasoningEffort: "high",
		OutputSchema: testSchema, Workdir: workdir,
		Parts: []harness.ContentPart{{Type: harness.ContentPartText, Text: "do the task"}},
	}
}

func successResult(sessionID string) claudeagent.ResultMessage {
	return claudeagent.ResultMessage{
		Type: "result", Status: "success", Subtype: "success", SessionID: sessionID,
		StructuredOutput: map[string]any{"next": "done", "notes": "proved"},
	}
}
