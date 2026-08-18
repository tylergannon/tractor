package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylergannon/tractor/harness"
)

var testSchema = json.RawMessage(`{
	"type":"object",
	"properties":{"next":{"const":"done"},"notes":{"type":"string"}},
	"required":["next","notes"],
	"additionalProperties":false
}`)

var optionalVerdictSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"verdict":{"type":"string","enum":["ok","steer"]},
		"target":{"type":"string","enum":["build","verify"]},
		"message":{"type":"string"}
	},
	"required":["verdict"],
	"additionalProperties":false
}`)

var requiredNullableVerdictSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"verdict":{"type":["string","null"]},
		"target":{"type":"string"}
	},
	"required":["verdict"],
	"additionalProperties":false
}`)

func TestAdapterRetainsFreshProcessThenResumesWithCompleteEvents(t *testing.T) {
	adapter, logPath := newProtocolTestAdapter(t)
	defer adapter.Close()
	workdir := t.TempDir()
	sessionID, createErr := adapter.CreateSession("gpt-test", workdir)
	if createErr != nil {
		t.Fatalf("CreateSession() error = %v", createErr)
	}

	var events []harness.Event
	result, runErr := adapter.RunTurn(validInput(sessionID, workdir, "first"), func(event harness.Event) {
		events = append(events, event)
	})
	if runErr != nil {
		t.Fatalf("first RunTurn() error = %v", runErr)
	}
	if result["next"] != "done" || result["notes"] != "first result" {
		t.Fatalf("first result = %#v", result)
	}
	wantTypes := []any{
		harness.EventUser,
		harness.EventToolCall,
		harness.EventToolResult,
		harness.EventThinking,
		harness.EventAssistant,
		harness.EventUsage,
	}
	if got := eventTypes(events); !equalAny(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[1]["call_id"] != events[2]["call_id"] {
		t.Fatalf("tool events are not paired: %#v %#v", events[1], events[2])
	}
	parts, ok := events[0]["parts"].([]harness.ContentPart)
	if !ok || len(parts) != 1 || parts[0].Text != "first" {
		t.Fatalf("opening user event = %#v", events[0])
	}

	if _, runErr := adapter.RunTurn(validInput(sessionID, workdir, "second"), func(harness.Event) {}); runErr != nil {
		t.Fatalf("second RunTurn() error = %v", runErr)
	}
	records := readProtocolLog(t, logPath)
	start := firstRecord(t, records, "thread/start")
	firstTurn := nthRecord(t, records, "turn/start", 0)
	resume := firstRecord(t, records, "thread/resume")
	secondTurn := nthRecord(t, records, "turn/start", 1)
	if start.PID != firstTurn.PID {
		t.Fatalf("fresh thread/start pid = %d, first turn pid = %d", start.PID, firstTurn.PID)
	}
	if resume.PID == start.PID || resume.PID != secondTurn.PID {
		t.Fatalf("resume pid=%d start pid=%d second turn pid=%d", resume.PID, start.PID, secondTurn.PID)
	}
	assertTurnStartParams(t, firstTurn.Params, workdir)
}

func TestAdapterCodexStrictSchemaCompatibilityPreservesCallerSemantics(t *testing.T) {
	adapter, logPath := newProtocolTestAdapter(t)
	defer adapter.Close()
	workdir := t.TempDir()
	sessionID, createErr := adapter.CreateSession("gpt-test", workdir)
	if createErr != nil {
		t.Fatal(createErr)
	}

	input := validInput(sessionID, workdir, "optional null verdict")
	input.OutputSchema = optionalVerdictSchema
	result, runErr := adapter.RunTurn(input, func(harness.Event) {})
	if runErr != nil {
		t.Fatalf("optional verdict RunTurn() error = %v", runErr)
	}
	if len(result) != 2 || result["verdict"] != "ok" || result["message"] != "observed" {
		t.Fatalf("normalized optional verdict = %#v", result)
	}
	if _, exists := result["target"]; exists {
		t.Fatalf("optional null target was retained: %#v", result)
	}
	assertCodexVerdictSchema(t, nthRecord(t, readProtocolLog(t, logPath), "turn/start", 0).Params)

	input = validInput(sessionID, workdir, "required nullable verdict")
	input.OutputSchema = requiredNullableVerdictSchema
	result, runErr = adapter.RunTurn(input, func(harness.Event) {})
	if runErr != nil {
		t.Fatalf("required nullable verdict RunTurn() error = %v", runErr)
	}
	if value, exists := result["verdict"]; !exists || value != nil {
		t.Fatalf("required null verdict was stripped: %#v", result)
	}
	if _, exists := result["target"]; exists {
		t.Fatalf("optional null target was retained: %#v", result)
	}
}

func TestCodexSchemaWithoutPropertiesPassesThrough(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	input := validInput("thread", t.TempDir(), "empty object schema")
	input.OutputSchema = raw
	params, optional, err := turnStartParams(input)
	if err != nil {
		t.Fatalf("turnStartParams() error = %v", err)
	}
	if len(optional) != 0 {
		t.Fatalf("optional properties = %v, want none", optional)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(params.OutputSchema, want) {
		t.Fatalf("output schema = %#v, want %#v", params.OutputSchema, want)
	}
}

func TestAdapterInterruptTimeoutRecoveryAndActiveCompaction(t *testing.T) {
	adapter, logPath := newProtocolTestAdapter(t)
	defer adapter.Close()
	workdir := t.TempDir()
	sessionID, createErr := adapter.CreateSession("gpt-test", workdir)
	if createErr != nil {
		t.Fatal(createErr)
	}

	result := make(chan *harness.Error, 1)
	var eventMu sync.Mutex
	var events []harness.Event
	go func() {
		input := validInput(sessionID, workdir, "hang until interrupted")
		_, err := adapter.RunTurn(input, func(event harness.Event) {
			eventMu.Lock()
			events = append(events, event)
			eventMu.Unlock()
		})
		result <- err
	}()
	waitForMethod(t, logPath, "turn/start")
	steering := []harness.ContentPart{{Type: harness.ContentPartText, Text: "steer-value-91"}}
	adapter.Steer(sessionID, steering)
	waitForMethod(t, logPath, "turn/steer")
	if err := adapter.Compact(sessionID, workdir); err == nil || err.Category != harness.ErrorTerminal {
		t.Fatalf("Compact(active) = %v, want terminal error", err)
	}
	adapter.Interrupt(sessionID)
	if err := <-result; err == nil || err.Category != harness.ErrorInterrupted {
		t.Fatalf("interrupted RunTurn() = %v", err)
	}
	eventMu.Lock()
	if len(events) != 2 || events[0]["type"] != harness.EventUser || events[1]["type"] != harness.EventUser {
		t.Fatalf("steered user events = %#v", events)
	}
	steeredParts, _ := events[1]["parts"].([]harness.ContentPart)
	if len(steeredParts) != 1 || steeredParts[0].Text != "steer-value-91" {
		t.Fatalf("steering event parts = %#v", events[1]["parts"])
	}
	eventMu.Unlock()

	if _, err := adapter.RunTurn(validInput(sessionID, workdir, "recovered"), func(harness.Event) {}); err != nil {
		t.Fatalf("post-interrupt RunTurn() = %v", err)
	}

	timeoutInput := validInput(sessionID, workdir, "hang for timeout")
	timeoutInput.Timeout = 30 * time.Millisecond
	started := time.Now()
	if _, err := adapter.RunTurn(timeoutInput, func(harness.Event) {}); err == nil || err.Category != harness.ErrorInterrupted {
		t.Fatalf("timed out RunTurn() = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestCategorizeInvalidSchemaIsTerminal(t *testing.T) {
	err := categorize(errors.New(`invalid_request_error: invalid_json_schema`), false)
	if err.Category != harness.ErrorTerminal {
		t.Fatalf("categorize(invalid_json_schema) = %v", err)
	}
}

func TestAdapterCompactionUnknownSessionAndCleanup(t *testing.T) {
	adapter, logPath := newProtocolTestAdapter(t)
	workdir := t.TempDir()
	sessionID, createErr := adapter.CreateSession("gpt-test", workdir)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if _, err := adapter.RunTurn(validInput(sessionID, workdir, "remember"), func(harness.Event) {}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Compact(sessionID, workdir); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	methods := protocolMethods(readProtocolLog(t, logPath))
	if !containsSubsequence(methods, []string{"thread/resume", "thread/compact/start"}) {
		t.Fatalf("protocol methods = %v", methods)
	}
	if err := adapter.Compact("missing-thread", workdir); err == nil || err.Category != harness.ErrorTerminal {
		t.Fatalf("Compact(unknown) = %v, want terminal error", err)
	}

	retained, retainedLog := newProtocolTestAdapter(t)
	if _, err := retained.CreateSession("gpt-test", workdir); err != nil {
		t.Fatal(err)
	}
	retained.Close()
	waitForMethod(t, retainedLog, "__exit__")
	adapter.Close()
}

func validInput(sessionID, workdir, prompt string) harness.RunTurnInput {
	return harness.RunTurnInput{
		SessionID:       sessionID,
		Model:           "gpt-test",
		ReasoningEffort: "medium",
		OutputSchema:    testSchema,
		Workdir:         workdir,
		Parts:           []harness.ContentPart{{Type: harness.ContentPartText, Text: prompt}},
	}
}

func newProtocolTestAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	logPath := t.TempDir() + "/protocol.jsonl"
	config := processConfig{
		command: os.Args[0],
		args:    []string{"-test.run=TestCodexProtocolHelperProcess"},
		env: append(os.Environ(),
			"TRACTOR_CODEX_HELPER=1",
			"TRACTOR_CODEX_HELPER_LOG="+logPath,
		),
	}
	return newAdapter(config), logPath
}

type protocolRecord struct {
	PID    int            `json:"pid"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

func readProtocolLog(t *testing.T, path string) []protocolRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open protocol log: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close protocol log: %v", err)
		}
	}()
	var records []protocolRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record protocolRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode protocol record: %v", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func waitForMethod(t *testing.T, path, method string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, record := range readProtocolLogIfExists(path) {
			if record.Method == method {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for protocol method %q", method)
}

func readProtocolLogIfExists(path string) []protocolRecord {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	var records []protocolRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record protocolRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil {
			records = append(records, record)
		}
	}
	return records
}

func firstRecord(t *testing.T, records []protocolRecord, method string) protocolRecord {
	t.Helper()
	return nthRecord(t, records, method, 0)
}

func nthRecord(t *testing.T, records []protocolRecord, method string, index int) protocolRecord {
	t.Helper()
	for _, record := range records {
		if record.Method != method {
			continue
		}
		if index == 0 {
			return record
		}
		index--
	}
	t.Fatalf("protocol method %q not found", method)
	return protocolRecord{}
}

func assertTurnStartParams(t *testing.T, params map[string]any, workdir string) {
	t.Helper()
	if params["model"] != "gpt-test" || params["effort"] != "medium" || params["cwd"] != workdir {
		t.Fatalf("turn/start forwarding = %#v", params)
	}
	if params["approvalPolicy"] != "never" {
		t.Fatalf("approvalPolicy = %#v", params["approvalPolicy"])
	}
	sandbox, _ := params["sandboxPolicy"].(map[string]any)
	if sandbox["type"] != "dangerFullAccess" {
		t.Fatalf("sandboxPolicy = %#v", params["sandboxPolicy"])
	}
	if _, ok := params["outputSchema"].(map[string]any); !ok {
		t.Fatalf("outputSchema = %#v", params["outputSchema"])
	}
}

func assertCodexVerdictSchema(t *testing.T, params map[string]any) {
	t.Helper()
	outputSchema, ok := params["outputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("outputSchema = %#v", params["outputSchema"])
	}
	required, _ := outputSchema["required"].([]any)
	if !sameStringSet(required, []string{"verdict", "target", "message"}) {
		t.Fatalf("Codex required = %#v", required)
	}
	properties, _ := outputSchema["properties"].(map[string]any)
	verdict, _ := properties["verdict"].(map[string]any)
	if verdict["type"] != "string" {
		t.Fatalf("required verdict schema = %#v", verdict)
	}
	target, _ := properties["target"].(map[string]any)
	if !sameStringSet(target["type"].([]any), []string{"string", "null"}) {
		t.Fatalf("optional target type = %#v", target["type"])
	}
	if !containsNil(target["enum"].([]any)) {
		t.Fatalf("optional target enum = %#v", target["enum"])
	}
	message, _ := properties["message"].(map[string]any)
	if !sameStringSet(message["type"].([]any), []string{"string", "null"}) {
		t.Fatalf("optional message type = %#v", message["type"])
	}
}

func sameStringSet(values []any, want []string) bool {
	if len(values) != len(want) {
		return false
	}
	got := make(map[string]bool, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return false
		}
		got[text] = true
	}
	for _, value := range want {
		if !got[value] {
			return false
		}
	}
	return true
}

func containsNil(values []any) bool {
	for _, value := range values {
		if value == nil {
			return true
		}
	}
	return false
}

func eventTypes(events []harness.Event) []any {
	result := make([]any, 0, len(events))
	for _, event := range events {
		result = append(result, event["type"])
	}
	return result
}

func equalAny(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func protocolMethods(records []protocolRecord) []string {
	methods := make([]string, 0, len(records))
	for _, record := range records {
		methods = append(methods, record.Method)
	}
	return methods
}

func containsSubsequence(values, want []string) bool {
	index := 0
	for _, value := range values {
		if index < len(want) && value == want[index] {
			index++
		}
	}
	return index == len(want)
}

func TestCodexProtocolHelperProcess(t *testing.T) {
	if os.Getenv("TRACTOR_CODEX_HELPER") != "1" {
		return
	}
	logPath := os.Getenv("TRACTOR_CODEX_HELPER_LOG")
	defer appendProtocolRecord(logPath, protocolRecord{PID: os.Getpid(), Method: "__exit__"})

	scanner := bufio.NewScanner(os.Stdin)
	writer := json.NewEncoder(os.Stdout)
	turn := 0
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		appendProtocolRecord(logPath, protocolRecord{PID: os.Getpid(), Method: request.Method, Params: request.Params})
		if len(request.ID) == 0 {
			continue
		}
		respond := func(result any) {
			_ = writer.Encode(map[string]any{"id": json.RawMessage(request.ID), "result": result})
		}
		switch request.Method {
		case "initialize":
			respond(map[string]any{})
		case "thread/start":
			respond(map[string]any{"thread": map[string]any{"id": "thread-1"}})
		case "thread/resume":
			if request.Params["threadId"] != "thread-1" {
				_ = writer.Encode(map[string]any{
					"id":    json.RawMessage(request.ID),
					"error": map[string]any{"code": -32602, "message": "thread not found"},
				})
				continue
			}
			respond(map[string]any{"thread": map[string]any{"id": "thread-1"}})
		case "turn/start":
			turn++
			turnID := "turn-" + strconv.Itoa(turn)
			respond(map[string]any{"turn": map[string]any{"id": turnID}})
			prompt := protocolPrompt(request.Params)
			if strings.Contains(prompt, "hang") {
				continue
			}
			emitCompletedProtocolTurn(writer, turnID, prompt)
		case "turn/interrupt":
			respond(map[string]any{})
			turnID, _ := request.Params["turnId"].(string)
			_ = writer.Encode(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": "thread-1",
					"turn":     map[string]any{"id": turnID, "status": "interrupted"},
				},
			})
		case "turn/steer":
			respond(map[string]any{"turnId": request.Params["expectedTurnId"]})
		case "thread/compact/start":
			respond(map[string]any{})
			turn++
			turnID := "compact-" + strconv.Itoa(turn)
			_ = writer.Encode(map[string]any{
				"method": "turn/started",
				"params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turnID}},
			})
			_ = writer.Encode(map[string]any{
				"method": "item/completed",
				"params": map[string]any{
					"threadId": "thread-1",
					"turnId":   turnID,
					"item":     map[string]any{"id": "compact-item", "type": "contextCompaction"},
				},
			})
			_ = writer.Encode(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": "thread-1",
					"turn":     map[string]any{"id": turnID, "status": "completed"},
				},
			})
		default:
			_ = writer.Encode(map[string]any{
				"id":    json.RawMessage(request.ID),
				"error": map[string]any{"code": -32601, "message": "unknown method"},
			})
		}
	}
}

func emitCompletedProtocolTurn(writer *json.Encoder, turnID, prompt string) {
	envelope := func(item map[string]any) map[string]any {
		return map[string]any{"threadId": "thread-1", "turnId": turnID, "item": item}
	}
	_ = writer.Encode(map[string]any{
		"method": "item/started",
		"params": envelope(map[string]any{
			"id": "call-1", "type": "commandExecution", "command": "printf test", "cwd": "/tmp",
		}),
	})
	_ = writer.Encode(map[string]any{
		"method": "item/completed",
		"params": envelope(map[string]any{
			"id": "call-1", "type": "commandExecution", "command": "printf test", "cwd": "/tmp",
			"aggregatedOutput": "test", "status": "completed",
		}),
	})
	_ = writer.Encode(map[string]any{
		"method": "item/completed",
		"params": envelope(map[string]any{
			"id": "reason-1", "type": "reasoning", "summary": []string{"checked the workspace"},
		}),
	})
	result := fmt.Sprintf(`{"next":"done","notes":%q}`, prompt+" result")
	switch prompt {
	case "optional null verdict":
		result = `{"verdict":"ok","target":null,"message":"observed"}`
	case "required nullable verdict":
		result = `{"verdict":null,"target":null}`
	}
	_ = writer.Encode(map[string]any{
		"method": "item/completed",
		"params": envelope(map[string]any{"id": "message-1", "type": "agentMessage", "text": result}),
	})
	_ = writer.Encode(map[string]any{
		"method": "thread/tokenUsage/updated",
		"params": map[string]any{
			"threadId": "thread-1", "turnId": turnID,
			"tokenUsage": map[string]any{"total": map[string]any{"totalTokens": 10}},
		},
	})
	_ = writer.Encode(map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"threadId": "thread-1", "turn": map[string]any{"id": turnID, "status": "completed"},
		},
	})
}

func protocolPrompt(params map[string]any) string {
	inputs, _ := params["input"].([]any)
	if len(inputs) == 0 {
		return ""
	}
	input, _ := inputs[0].(map[string]any)
	text, _ := input["text"].(string)
	return text
}

var protocolLogMu sync.Mutex

func appendProtocolRecord(path string, record protocolRecord) {
	protocolLogMu.Lock()
	defer protocolLogMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = json.NewEncoder(file).Encode(record)
	_ = file.Close()
}
