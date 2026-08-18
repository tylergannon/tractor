package agy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylergannon/tractor/harness"
)

var testSchema = json.RawMessage(`{
  "type":"object",
  "properties":{"answer":{"type":"string"}},
  "required":["answer"],
  "additionalProperties":false
}`)

func TestCreateSessionAndRunTurn(t *testing.T) {
	workdir := t.TempDir()
	record := filepath.Join(t.TempDir(), "args.jsonl")
	adapter := testAdapter(t, "success", record)
	defer adapter.Close()

	sessionID, createErr := adapter.CreateSession("gemini-test", workdir)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if sessionID != "conversation-test" {
		t.Fatalf("session ID = %q", sessionID)
	}

	var events []harness.Event
	result, runErr := adapter.RunTurn(validInput(sessionID, workdir, 5*time.Second), func(event harness.Event) {
		events = append(events, event)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if result["answer"] != "valid" {
		t.Fatalf("result = %#v", result)
	}
	if len(events) < 5 || events[0]["type"] != harness.EventUser {
		t.Fatalf("events = %#v", events)
	}

	invocations := readInvocations(t, record)
	if len(invocations) != 2 {
		t.Fatalf("invocations = %d, want 2", len(invocations))
	}
	for _, args := range invocations {
		assertFlagValue(t, args, "--add-dir", workdir)
	}
	if !slices.Contains(invocations[0], "--new-project") {
		t.Fatalf("create args omit --new-project: %#v", invocations[0])
	}
	assertFlagValue(t, invocations[1], "--conversation", sessionID)
}

func TestRunTurnReconstructsAndRejectsConversationMismatch(t *testing.T) {
	workdir := t.TempDir()
	adapter := testAdapter(t, "success", "")
	result, runErr := adapter.RunTurn(validInput("conversation-test", workdir, 5*time.Second), func(harness.Event) {})
	if runErr != nil || result["answer"] != "valid" {
		t.Fatalf("reconstructed result=%#v err=%v", result, runErr)
	}
	adapter.Close()

	mismatched := testAdapter(t, "mismatch", "")
	defer mismatched.Close()
	_, runErr = mismatched.RunTurn(validInput("conversation-test", workdir, 5*time.Second), func(harness.Event) {})
	if runErr == nil || runErr.Category != harness.ErrorTerminal || !strings.Contains(strings.ToLower(runErr.Message), "different conversation") {
		t.Fatalf("mismatch error = %#v", runErr)
	}
}

func TestRunTurnRepairsAtMostOnce(t *testing.T) {
	workdir := t.TempDir()
	record := filepath.Join(t.TempDir(), "args.jsonl")
	adapter := testAdapter(t, "repair", record)
	defer adapter.Close()
	var users int
	result, runErr := adapter.RunTurn(validInput("conversation-test", workdir, 5*time.Second), func(event harness.Event) {
		if event["type"] == harness.EventUser {
			users++
		}
	})
	if runErr != nil || result["answer"] != "valid" {
		t.Fatalf("repair result=%#v err=%v", result, runErr)
	}
	if users != 2 {
		t.Fatalf("user events = %d, want initial plus one repair", users)
	}
	if invocations := readInvocations(t, record); len(invocations) != 2 {
		t.Fatalf("invocations = %d, want 2", len(invocations))
	}
}

func TestRunTurnTimeoutInterruptsProcess(t *testing.T) {
	adapter := testAdapter(t, "sleep", "")
	defer adapter.Close()
	started := time.Now()
	_, runErr := adapter.RunTurn(validInput("conversation-test", t.TempDir(), 50*time.Millisecond), func(harness.Event) {})
	if runErr == nil || runErr.Category != harness.ErrorInterrupted {
		t.Fatalf("timeout error = %#v", runErr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestSteerInterruptsAndResumesSameRunTurn(t *testing.T) {
	workdir := t.TempDir()
	record := filepath.Join(t.TempDir(), "args.jsonl")
	adapter := testAdapter(t, "steer", record)
	defer adapter.Close()

	var mu sync.Mutex
	var events []harness.Event
	type outcome struct {
		result harness.Result
		err    *harness.Error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := adapter.RunTurn(validInput("conversation-test", workdir, 5*time.Second), func(event harness.Event) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		})
		done <- outcome{result: result, err: runErr}
	}()
	waitForInvocations(t, record, 1)
	steerParts := []harness.ContentPart{{Type: harness.ContentPartText, Text: "apply the steering message"}}
	adapter.Steer("conversation-test", steerParts)

	got := <-done
	if got.err != nil || got.result["answer"] != "valid" {
		t.Fatalf("steered result=%#v err=%v", got.result, got.err)
	}
	invocations := readInvocations(t, record)
	if len(invocations) != 2 {
		t.Fatalf("invocations = %d, want interrupted turn plus resume", len(invocations))
	}
	if prompt := flagValue(invocations[1], "-p"); prompt != steerParts[0].Text {
		t.Fatalf("steering prompt = %q", prompt)
	}
	for _, args := range invocations {
		assertFlagValue(t, args, "--conversation", "conversation-test")
	}
	mu.Lock()
	defer mu.Unlock()
	var users []harness.Event
	for _, event := range events {
		if event["type"] == harness.EventUser {
			users = append(users, event)
		}
	}
	if len(users) != 2 {
		t.Fatalf("user events = %d, want initial plus steering", len(users))
	}
}

func TestSteerInactiveSessionDoesNotQueue(t *testing.T) {
	workdir := t.TempDir()
	record := filepath.Join(t.TempDir(), "args.jsonl")
	adapter := testAdapter(t, "success", record)
	defer adapter.Close()
	adapter.states["conversation-test"] = &sessionState{workdir: workdir}
	adapter.Steer("conversation-test", []harness.ContentPart{{Type: harness.ContentPartText, Text: "do not queue me"}})

	var users int
	_, runErr := adapter.RunTurn(validInput("conversation-test", workdir, 5*time.Second), func(event harness.Event) {
		if event["type"] == harness.EventUser {
			users++
		}
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if users != 1 || len(readInvocations(t, record)) != 1 {
		t.Fatalf("inactive steering leaked: users=%d invocations=%d", users, len(readInvocations(t, record)))
	}
}

func TestCompactSendsNativeCommand(t *testing.T) {
	workdir := t.TempDir()
	record := filepath.Join(t.TempDir(), "args.jsonl")
	adapter := testAdapter(t, "success", record)
	defer adapter.Close()
	adapter.states["conversation-test"] = &sessionState{workdir: workdir}

	if compactErr := adapter.Compact("conversation-test", workdir); compactErr != nil {
		t.Fatal(compactErr)
	}
	invocations := readInvocations(t, record)
	if len(invocations) != 1 || flagValue(invocations[0], "-p") != "/compact" {
		t.Fatalf("compact invocations = %#v", invocations)
	}
	assertFlagValue(t, invocations[0], "--conversation", "conversation-test")
}

func TestCreateSessionClassifiesIDLessServiceFailure(t *testing.T) {
	adapter := testAdapter(t, "internal", "")
	defer adapter.Close()
	_, createErr := adapter.CreateSession("gemini-test", t.TempDir())
	if createErr == nil || createErr.Category != harness.ErrorRetryable {
		t.Fatalf("create error = %#v", createErr)
	}
}

func TestCategorize(t *testing.T) {
	for message, want := range map[string]harness.ErrorCategory{
		"quota exhausted": harness.ErrorRetryable,
		"Eligibility check failed: INTERNAL (code 500): We can't connect to Gemini Code Assist": harness.ErrorRetryable,
		"unknown model x":        harness.ErrorTerminal,
		"unexpected local fault": harness.ErrorRetryable,
	} {
		if got := categorize(fmt.Errorf("%s", message), false); got.Category != want {
			t.Errorf("categorize(%q) = %q, want %q", message, got.Category, want)
		}
	}
}

func TestModelIncludesEffort(t *testing.T) {
	for _, model := range []string{"gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.1-pro-high"} {
		if !modelIncludesEffort(model) {
			t.Errorf("modelIncludesEffort(%q) = false", model)
		}
	}
	if modelIncludesEffort("gemini-test") {
		t.Error("modelIncludesEffort(gemini-test) = true")
	}
}

func validInput(sessionID, workdir string, timeout time.Duration) harness.RunTurnInput {
	return harness.RunTurnInput{
		SessionID:       sessionID,
		Model:           "gemini-test",
		ReasoningEffort: "low",
		OutputSchema:    testSchema,
		Workdir:         workdir,
		Parts:           []harness.ContentPart{{Type: harness.ContentPartText, Text: "do the task"}},
		Timeout:         timeout,
	}
}

func testAdapter(t *testing.T, mode, record string) *Adapter {
	t.Helper()
	environment := append(os.Environ(), "GO_WANT_AGY_HELPER=1", "AGY_HELPER_MODE="+mode)
	if record != "" {
		environment = append(environment, "AGY_HELPER_RECORD="+record)
	}
	return newAdapter(runnerConfig{
		binary:   os.Args[0],
		baseArgs: []string{"-test.run=TestAgyHelperProcess", "--"},
		env:      environment,
	})
}

func TestAgyHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AGY_HELPER") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	args := os.Args[separator+1:]
	if record := os.Getenv("AGY_HELPER_RECORD"); record != "" {
		file, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_ = json.NewEncoder(file).Encode(args)
		_ = file.Close()
	}
	sessionID := flagValue(args, "--conversation")
	if sessionID == "" {
		sessionID = "conversation-test"
	}
	mode := os.Getenv("AGY_HELPER_MODE")
	if mode == "internal" {
		writeJSON(map[string]any{"event": "result", "result": map[string]any{
			"status": "ERROR", "error": "Eligibility check failed: INTERNAL (code 500): We can't connect to Gemini Code Assist",
		}})
		os.Exit(1)
	}
	if mode == "mismatch" {
		sessionID = "conversation-other"
	}
	writeJSON(map[string]any{"event": "init", "conversation_id": sessionID, "init": map[string]any{"conversation_id": sessionID}})
	if mode == "sleep" {
		time.Sleep(30 * time.Second)
		os.Exit(3)
	}
	prompt := flagValue(args, "-p")
	if mode == "steer" && prompt != "apply the steering message" {
		time.Sleep(30 * time.Second)
		os.Exit(3)
	}
	schemaValue := any(nil)
	if path := flagValue(args, "--json-schema"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &schemaValue) != nil {
			os.Exit(4)
		}
		writeJSON(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 1, "step_type": "tool", "state": "ACTIVE", "tool_info": map[string]any{"name": "write_file", "parameters": map[string]any{"path": "x"}}}})
		writeJSON(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 1, "step_type": "tool", "state": "DONE", "tool_info": map[string]any{"name": "write_file", "output": "ok"}}})
		writeJSON(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 2, "step_type": "agent_response", "state": "ACTIVE", "text_delta": "done"}})
		writeJSON(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": 2, "step_type": "agent_response", "state": "DONE", "usage": map[string]any{"input_tokens": 2}}})
	}
	structured := any(map[string]any{"answer": "valid"})
	if mode == "repair" && !strings.Contains(prompt, "previous structured output") {
		structured = map[string]any{"wrong": true}
	}
	writeJSON(map[string]any{"event": "result", "conversation_id": sessionID, "result": map[string]any{
		"conversation_id": sessionID, "status": "SUCCESS", "structured_output": structured, "json_schema": schemaValue,
	}})
	os.Exit(0)
}

func writeJSON(value any) {
	_ = json.NewEncoder(os.Stdout).Encode(value)
}

func flagValue(args []string, flag string) string {
	index := slices.Index(args, flag)
	if index < 0 || index+1 >= len(args) {
		return ""
	}
	return args[index+1]
}

func readInvocations(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		result = append(result, args)
	}
	return result
}

func assertFlagValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	if got := flagValue(args, flag); got != want {
		t.Fatalf("%s = %q, want %q in %#v", flag, got, want, args)
	}
}

func waitForInvocations(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			trimmed := strings.TrimSpace(string(raw))
			if trimmed != "" && strings.Count(trimmed, "\n")+1 >= want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d invocation(s)", want)
}
