package agy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tylergannon/gimble"
)

func TestAdapterCreatesResumesStructuresAndTranslates(t *testing.T) {
	adapter, record := testAdapter(t)
	workdir := t.TempDir()
	sessionID, err := adapter.CreateSession(t.Context(), "gemini-test-low", workdir)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID == "" || sessionID == "conversation-test" {
		t.Fatalf("adapter session ID = %q", sessionID)
	}

	var events []gimble.AgentEvent
	raw, err := adapter.RunTurn(t.Context(), sessionID, "STRUCTURED", json.RawMessage(`{"type":"object"}`), func(event gimble.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw.Output) != `{"answer":"valid"}` {
		t.Fatalf("result = %s", raw.Output)
	}
	if !slices.Contains(eventTypes(events), "session.text.ended") || !slices.Contains(eventTypes(events), "session.step.ended") {
		t.Fatalf("events = %v", eventTypes(events))
	}
	var nativeRef map[string]any
	if err := json.Unmarshal(events[0].NativeRef, &nativeRef); err != nil {
		t.Fatal(err)
	}
	if nativeRef["sessionID"] != "conversation-test" {
		t.Fatalf("native session provenance = %#v", nativeRef)
	}
	text, err := adapter.RunTurn(t.Context(), sessionID, "TEXT", nil, func(gimble.AgentEvent) error { return nil })
	if err != nil || string(text.Output) != `"OK"` {
		t.Fatalf("text result=%s error=%v", text.Output, err)
	}

	invocations := readInvocations(t, record)
	if len(invocations) != 2 || !slices.Contains(invocations[0], "--new-project") {
		t.Fatalf("invocations = %#v", invocations)
	}
	assertFlag(t, invocations[1], "--conversation", "conversation-test")
	assertFlag(t, invocations[0], "--model", "gemini-test-low")
	assertFlag(t, invocations[0], "--add-dir", workdir)
	if _, err := adapter.Fork(t.Context(), sessionID); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Fork error = %v", err)
	}
}

func TestAdapterSteerInterruptsAndResumesInsideTurn(t *testing.T) {
	adapter, record := testAdapter(t)
	workdir := t.TempDir()
	sessionID, err := adapter.CreateSession(t.Context(), "gemini-test-low", workdir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var raw gimble.TurnResult
	var runErr error
	go func() {
		raw, runErr = adapter.RunTurn(context.Background(), sessionID, "WAIT", nil, func(gimble.AgentEvent) error { return nil })
		close(done)
	}()
	waitInvocations(t, record, 1)
	if err := adapter.Steer(t.Context(), sessionID, "STEER"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("steered turn did not finish")
	}
	if runErr != nil || string(raw.Output) != `"steered"` {
		t.Fatalf("result=%s error=%v", raw.Output, runErr)
	}
	invocations := readInvocations(t, record)
	if len(invocations) != 2 || flagValue(invocations[1], "-p") != "STEER" {
		t.Fatalf("invocations = %#v", invocations)
	}
	assertFlag(t, invocations[1], "--conversation", "conversation-test")
}

func TestAdapterCancellationAfterStdoutClosesStillInterruptsProcess(t *testing.T) {
	adapter, record := testAdapter(t)
	workdir := t.TempDir()
	sessionID, err := adapter.CreateSession(t.Context(), "gemini-test-low", workdir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := adapter.RunTurn(ctx, sessionID, "CLOSE_STDOUT", nil, func(gimble.AgentEvent) error { return nil })
		done <- err
	}()
	waitInvocations(t, record, 1)
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTurn error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled process did not finish")
	}
}

func TestAdapterPropagatesObserverFailure(t *testing.T) {
	adapter, _ := testAdapter(t)
	workdir := t.TempDir()
	sessionID, err := adapter.CreateSession(t.Context(), "gemini-test-low", workdir)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("observer stopped")
	_, err = adapter.RunTurn(t.Context(), sessionID, "TEXT", nil, func(gimble.AgentEvent) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("RunTurn error = %v", err)
	}
}

func testAdapter(t *testing.T) (*adapter, string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "invocations.jsonl")
	env := append(os.Environ(), "GO_WANT_AGY_HELPER=1", "AGY_HELPER_RECORD="+record)
	return newAdapter(config{binary: os.Args[0], baseArgs: []string{"-test.run=TestAgyHelperProcess", "--"}, env: env}), record
}

func TestAgyHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AGY_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	file, _ := os.OpenFile(os.Getenv("AGY_HELPER_RECORD"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	_ = json.NewEncoder(file).Encode(args)
	_ = file.Close()
	prompt := flagValue(args, "-p")
	conversation := flagValue(args, "--conversation")
	if conversation == "" {
		conversation = "conversation-test"
	}
	writeEnvelope := func(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
	writeEnvelope(map[string]any{"event": "init", "conversation_id": conversation})
	writeEnvelope(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": conversation, "step_index": 0, "state": "DONE", "step_type": "user_input"}})
	if prompt == "WAIT" {
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		<-interrupt
		os.Exit(130)
	}
	if prompt == "CLOSE_STDOUT" {
		_ = os.Stdout.Close()
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		<-interrupt
		os.Exit(130)
	}
	response := "OK"
	structured := any(nil)
	if prompt == "STRUCTURED" {
		response = `{"answer":"valid"}`
		structured = map[string]any{"answer": "valid"}
	}
	if prompt == "STEER" {
		response = "steered"
	}
	writeEnvelope(map[string]any{"event": "step_update", "step_update": map[string]any{
		"conversation_id": conversation, "step_index": 1, "state": "DONE", "step_type": "agent_response", "text_delta": response,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "thinking_tokens": 0, "cache_read_tokens": 0},
	}})
	result := map[string]any{"conversation_id": conversation, "status": "SUCCESS", "response": response}
	if structured != nil {
		result["structured_output"] = structured
	}
	writeEnvelope(map[string]any{"event": "result", "result": result})
	os.Exit(0)
}

func readInvocations(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out [][]string
	decoder := json.NewDecoder(file)
	for decoder.More() {
		var args []string
		if err := decoder.Decode(&args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args)
	}
	return out
}

func waitInvocations(t *testing.T, path string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.Count(string(raw), "\n") >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe %d invocations", count)
}

func assertFlag(t *testing.T, args []string, name, want string) {
	t.Helper()
	if got := flagValue(args, name); got != want {
		t.Fatalf("%s = %q, want %q in %#v", name, got, want, args)
	}
}

func flagValue(args []string, name string) string {
	for i := range len(args) - 1 {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
