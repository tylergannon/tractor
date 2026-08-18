package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/tylergannon/tractor/internal/runlog"
)

var outcomeSchema = json.RawMessage(`{
	"type":"object",
	"properties":{"next":{"type":"string"},"notes":{"type":"string"}},
	"required":["next","notes"],
	"additionalProperties":false
}`)

var verdictSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"verdict":{"type":"string","enum":["ok","steer"]},
		"target":{"type":"string"},
		"message":{"type":"string"}
	},
	"required":["verdict"],
	"additionalProperties":false
}`)

func TestHarnessBackendFidelityBindingsAndInvariants(t *testing.T) {
	workdir := t.TempDir()
	primary := &scriptedAdapter{name: "primary"}
	secondary := &scriptedAdapter{name: "secondary"}
	backend := newTestBackend(t, map[string]HarnessAdapter{
		"primary":   primary,
		"secondary": secondary,
	}, map[string]string{"provider-a": "primary", "provider-b": "secondary"}, map[string]ThreadBinding{
		"restored": {Harness: "primary", SessionID: "restored-session", Workdir: workdir},
	})

	restored := testTurn("restored-node", "restored", FidelityFull, "provider-a", workdir)
	if _, err := backend.Run(allocateTestTurn(t, backend, restored)); err != nil {
		t.Fatalf("run restored binding: %v", err)
	}
	if got := primary.createdSessions(); len(got) != 0 {
		t.Fatalf("sessions created for restored binding = %v, want none", got)
	}

	turn := testTurn("review", "shared", FidelityFull, "provider-a", workdir)
	outcome, err := backend.Run(allocateTestTurn(t, backend, turn))
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if outcome != (Outcome{Next: "done", Notes: "primary result"}) {
		t.Fatalf("first Run() = %#v", outcome)
	}

	turn.Fidelity = FidelityCompacted
	if _, err := backend.Run(allocateTestTurn(t, backend, turn)); err != nil {
		t.Fatalf("compacted revisit Run() error = %v", err)
	}
	if got, want := primary.operations(), []string{
		"run:restored-session",
		"create:primary-1",
		"run:primary-1",
		"compact:primary-1",
		"run:primary-1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("adapter operations = %v, want %v", got, want)
	}
	routed := testTurn("claude-task", "claude-task", FidelityFull, "provider-b", workdir)
	routedOutcome, routeErr := backend.Run(allocateTestTurn(t, backend, routed))
	if routeErr != nil {
		t.Fatalf("provider-b Run() error = %v", routeErr)
	}
	if routedOutcome.Notes != "secondary result" {
		t.Fatalf("provider-b outcome = %#v", routedOutcome)
	}

	wrongHarness := turn
	wrongHarness.Provider = "provider-b"
	assertTerminalError(t, runError(t, backend, wrongHarness))
	if got := secondary.createdSessions(); !reflect.DeepEqual(got, []string{"secondary-1"}) {
		t.Fatalf("secondary sessions after harness mismatch = %v", got)
	}

	primary.setCompactError(&Error{Category: ErrorRetryable, Message: "compact unavailable"})
	beforeRuns := primary.runCount()
	_, compactErr := backend.Run(allocateTestTurn(t, backend, turn))
	if compactErr == nil || compactErr.Category != ErrorRetryable {
		t.Fatalf("compact failure = %v, want retryable error", compactErr)
	}
	if got := primary.runCount(); got != beforeRuns {
		t.Fatalf("run count after compact failure = %d, want %d", got, beforeRuns)
	}

	primary.setCompactError(nil)
	none := testTurn("isolated", "", FidelityNone, "provider-a", workdir)
	if _, err := backend.Run(allocateTestTurn(t, backend, none)); err != nil {
		t.Fatalf("first none Run() error = %v", err)
	}
	if _, err := backend.Run(allocateTestTurn(t, backend, none)); err != nil {
		t.Fatalf("second none Run() error = %v", err)
	}
	created := primary.createdSessions()
	if got := created[len(created)-2:]; !reflect.DeepEqual(got, []string{"primary-2", "primary-3"}) {
		t.Fatalf("none sessions = %v, want fresh sessions", got)
	}
	bindings := backend.Bindings()
	if bindings["shared"].SessionID != "primary-1" {
		t.Fatalf("shared binding = %#v", bindings["shared"])
	}
	if bindings[noneThreadPrefix+"isolated"].SessionID != "primary-3" {
		t.Fatalf("none binding = %#v", bindings[noneThreadPrefix+"isolated"])
	}
}

func TestHarnessBackendStaleWorkdirRebindsAfterHarnessCheck(t *testing.T) {
	oldWorkdir := t.TempDir()
	newWorkdir := t.TempDir()
	primary := &scriptedAdapter{name: "primary"}
	secondary := &scriptedAdapter{name: "secondary"}
	backend := newTestBackend(t, map[string]HarnessAdapter{
		"primary":   primary,
		"secondary": secondary,
	}, map[string]string{"provider-a": "primary", "provider-b": "secondary"}, map[string]ThreadBinding{
		"shared": {Harness: "primary", SessionID: "stale-session", Workdir: oldWorkdir},
	})

	stale := testTurn("node", "shared", FidelityCompacted, "provider-a", newWorkdir)
	if _, err := backend.Run(allocateTestTurn(t, backend, stale)); err != nil {
		t.Fatalf("Run() with stale workdir: %v", err)
	}
	stale.Fidelity = FidelityFull
	if _, err := backend.Run(allocateTestTurn(t, backend, stale)); err != nil {
		t.Fatalf("Run() after stale replacement: %v", err)
	}
	if got, want := primary.operations(), []string{
		"create:primary-1",
		"run:primary-1",
		"run:primary-1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary operations = %v, want %v", got, want)
	}
	if got := backend.Bindings()["shared"]; got != (ThreadBinding{Harness: "primary", SessionID: "primary-1", Workdir: newWorkdir}) {
		t.Fatalf("replacement binding = %#v", got)
	}

	wrongHarness := stale
	wrongHarness.Provider = "provider-b"
	wrongHarness.Workdir = t.TempDir()
	assertTerminalError(t, runError(t, backend, wrongHarness))
	if got := secondary.createdSessions(); len(got) != 0 {
		t.Fatalf("secondary sessions after harness and workdir mismatch = %v", got)
	}
}

func TestHarnessBackendRecoversEventSequenceAcrossReconstruction(t *testing.T) {
	logsRoot := t.TempDir()
	eventsRoot := filepath.Join(logsRoot, "events")
	if err := os.MkdirAll(eventsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000002-old.jsonl", "000009-later.jsonl", "index.jsonl", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(eventsRoot, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(eventsRoot, "000099-directory.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	adapter := &scriptedAdapter{name: "scripted"}
	construct := func(bindings map[string]ThreadBinding) *HarnessBackend {
		backend, err := NewHarnessBackend(
			logsRoot,
			map[string]HarnessAdapter{"scripted": adapter},
			map[string]string{"provider": "scripted"},
			bindings,
		)
		if err != nil {
			t.Fatalf("NewHarnessBackend() error = %v", err)
		}
		return backend
	}

	first := construct(nil)
	workdir := t.TempDir()
	if _, err := first.Run(allocateTestTurn(t, first, testTurn("first", "shared", FidelityFull, "provider", workdir))); err != nil {
		t.Fatalf("first reconstructed Run(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(eventsRoot, "000010-first.jsonl")); err != nil {
		t.Fatalf("recovered segment 10: %v", err)
	}

	second := construct(first.Bindings())
	if _, err := second.Run(allocateTestTurn(t, second, testTurn("second", "shared", FidelityFull, "provider", workdir))); err != nil {
		t.Fatalf("second reconstructed Run(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(eventsRoot, "000011-second.jsonl")); err != nil {
		t.Fatalf("recovered segment 11: %v", err)
	}
	index := readJSONLines(t, filepath.Join(eventsRoot, "index.jsonl"))
	if got := []any{index[0]["seq"], index[1]["seq"]}; !reflect.DeepEqual(got, []any{float64(10), float64(11)}) {
		t.Fatalf("reconstructed index sequences = %v", got)
	}
}

func TestHarnessBackendConcurrentControlsAndRunLogDiscovery(t *testing.T) {
	workdir := t.TempDir()
	started := make(chan string, 2)
	gateOne := make(chan struct{})
	gateTwo := make(chan struct{})
	adapter := &scriptedAdapter{name: "scripted"}
	adapter.run = func(input RunTurnInput, onEvent OnEvent) (Result, *Error) {
		text := input.Parts[0].Text
		onEvent(Event{"type": EventUser, "parts": input.Parts})
		started <- text
		switch text {
		case "one":
			<-gateOne
		case "two":
			<-gateTwo
		}
		onEvent(Event{"type": EventAssistant, "text": "finished " + text})
		return Result{"next": "done", "notes": text}, nil
	}
	backend := newTestBackend(t, map[string]HarnessAdapter{"scripted": adapter}, map[string]string{"provider": "scripted"}, nil)
	if got := backend.Steer(textParts("before")); got != SteerNotActive {
		t.Fatalf("Steer() before run = %q", got)
	}

	results := make(chan runResult, 2)
	go runAsync(backend, allocateTestTurn(t, backend, testTurnWithPrompt("node-one", "thread-one", "one", workdir)), results)
	waitStarted(t, started, "one")
	firstTarget := readCurrentTarget(t, backend.logsRoot)
	if firstTarget != filepath.Join("events", "000001-node-one.jsonl") {
		t.Fatalf("current after first start = %q", firstTarget)
	}
	if got := backend.Steer(textParts("first steer")); got != SteerAccepted {
		t.Fatalf("Steer() with one live turn = %q", got)
	}

	go runAsync(backend, allocateTestTurn(t, backend, testTurnWithPrompt("node-two", "thread-two", "two", workdir)), results)
	waitStarted(t, started, "two")
	if got := readCurrentTarget(t, backend.logsRoot); got != filepath.Join("events", "index.jsonl") {
		t.Fatalf("current with two live turns = %q", got)
	}
	if got := backend.Steer(textParts("ambiguous")); got != SteerAmbiguousTarget {
		t.Fatalf("Steer() with two live turns = %q", got)
	}

	backend.InterruptAll()
	if got := adapter.interruptedSessions(); !reflect.DeepEqual(got, []string{"scripted-1", "scripted-2"}) {
		t.Fatalf("interrupted sessions = %v", got)
	}

	close(gateOne)
	first := <-results
	if first.node != "node-one" || first.err != nil {
		t.Fatalf("first completion = %#v", first)
	}
	if got := readCurrentTarget(t, backend.logsRoot); got != filepath.Join("events", "index.jsonl") {
		t.Fatalf("current with second turn remaining = %q", got)
	}
	if got := backend.Steer(textParts("second steer")); got != SteerAccepted {
		t.Fatalf("Steer() after first completion = %q", got)
	}

	close(gateTwo)
	second := <-results
	if second.node != "node-two" || second.err != nil {
		t.Fatalf("second completion = %#v", second)
	}
	if got := backend.Steer(textParts("after")); got != SteerNotActive {
		t.Fatalf("Steer() after completion = %q", got)
	}
	if got := readCurrentTarget(t, backend.logsRoot); got != filepath.Join("events", "index.jsonl") {
		t.Fatalf("current after completion = %q", got)
	}
	if _, err := backend.Run(allocateTestTurn(t, backend, testTurnWithPrompt("node-three", "thread-three", "three", workdir))); err != nil {
		t.Fatalf("Run() after overlap drains: %v", err)
	}
	if got := readCurrentTarget(t, backend.logsRoot); got != filepath.Join("events", "000003-node-three.jsonl") {
		t.Fatalf("current after next lone turn = %q", got)
	}

	steers := adapter.steeredSessions()
	if got, want := steers, []string{"scripted-1", "scripted-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steered sessions = %v, want %v", got, want)
	}
	assertRunLog(t, backend.logsRoot, 3)
}

func TestHarnessBackendSupervisorBindingVerdictAndCallback(t *testing.T) {
	workdir := t.TempDir()
	adapter := &scriptedAdapter{name: "scripted"}
	adapter.run = func(RunTurnInput, OnEvent) (Result, *Error) {
		return Result{"verdict": "steer", "target": "build", "message": "inspect the failure"}, nil
	}
	backend := newTestBackend(t, map[string]HarnessAdapter{"scripted": adapter}, map[string]string{"provider": "scripted"}, nil)
	var opened []ThreadBinding
	backend.SetBindingOpened(func(key string, binding ThreadBinding) *Error {
		if key != "coach" {
			t.Fatalf("opened key = %q", key)
		}
		if got := backend.Bindings()[key]; got != binding {
			t.Fatalf("binding during callback = %#v, want %#v", got, binding)
		}
		if got := adapter.runCount(); got != 0 {
			t.Fatalf("turn dispatched before binding callback: run count = %d", got)
		}
		opened = append(opened, binding)
		return nil
	})

	turn := testSupervisorTurn("coach", "provider", workdir)
	verdict, err := backend.RunSupervisor(allocateTestSupervisorTurn(t, backend, turn))
	if err != nil {
		t.Fatalf("RunSupervisor() error = %v", err)
	}
	if verdict != (Verdict{Verdict: "steer", Target: "build", Message: "inspect the failure"}) {
		t.Fatalf("RunSupervisor() = %#v", verdict)
	}
	if _, err := backend.RunSupervisor(allocateTestSupervisorTurn(t, backend, turn)); err != nil {
		t.Fatalf("second RunSupervisor() error = %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("binding callbacks = %d, want 1", len(opened))
	}
	if got, want := adapter.operations(), []string{"create:scripted-1", "run:scripted-1", "run:scripted-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("adapter operations = %v, want %v", got, want)
	}
}

func TestHarnessBackendSupervisorIsNotSteerableButIsInterruptibleAndLive(t *testing.T) {
	workdir := t.TempDir()
	started := make(chan string, 2)
	supervisorGate := make(chan struct{})
	walkGate := make(chan struct{})
	adapter := &scriptedAdapter{name: "scripted"}
	adapter.run = func(input RunTurnInput, onEvent OnEvent) (Result, *Error) {
		text := input.Parts[0].Text
		onEvent(Event{"type": EventUser, "parts": input.Parts})
		started <- text
		if text == "supervise" {
			<-supervisorGate
			onEvent(Event{"type": EventAssistant, "text": "supervisor done"})
			return Result{"verdict": "ok"}, nil
		}
		<-walkGate
		onEvent(Event{"type": EventAssistant, "text": "walk done"})
		return Result{"next": "done", "notes": "walk"}, nil
	}
	backend := newTestBackend(t, map[string]HarnessAdapter{"scripted": adapter}, map[string]string{"provider": "scripted"}, nil)

	supervisorResults := make(chan supervisorRunResult, 1)
	go runSupervisorAsync(backend, allocateTestSupervisorTurn(t, backend, testSupervisorTurn("coach", "provider", workdir)), supervisorResults)
	waitStarted(t, started, "supervise")
	if got := backend.Steer(textParts("must not reach supervisor")); got != SteerNotActive {
		t.Fatalf("Steer() with only supervisor live = %q", got)
	}
	if got := readCurrentTarget(t, backend.logsRoot); got != filepath.Join("events", "000001-coach.jsonl") {
		t.Fatalf("current with supervisor live = %q", got)
	}

	walkResults := make(chan runResult, 1)
	go runAsync(backend, allocateTestTurn(t, backend, testTurnWithPrompt("build", "build", "walk", workdir)), walkResults)
	waitStarted(t, started, "walk")
	if got := readCurrentTarget(t, backend.logsRoot); got != filepath.Join("events", "index.jsonl") {
		t.Fatalf("current with walk and supervisor live = %q", got)
	}
	if got := backend.Steer(textParts("walk only")); got != SteerAccepted {
		t.Fatalf("Steer() with walk and supervisor live = %q", got)
	}
	backend.InterruptAll()
	if got, want := adapter.interruptedSessions(), []string{"scripted-1", "scripted-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interrupted sessions = %v, want %v", got, want)
	}
	if got, want := adapter.steeredSessions(), []string{"scripted-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steered sessions = %v, want %v", got, want)
	}

	close(walkGate)
	if result := <-walkResults; result.err != nil {
		t.Fatalf("walk completion = %#v", result)
	}
	close(supervisorGate)
	if result := <-supervisorResults; result.err != nil || result.verdict.Verdict != "ok" {
		t.Fatalf("supervisor completion = %#v", result)
	}
}

func TestHarnessBackendBindingCallbackFailurePreventsDispatch(t *testing.T) {
	adapter := &scriptedAdapter{name: "scripted"}
	backend := newTestBackend(t, map[string]HarnessAdapter{"scripted": adapter}, map[string]string{"provider": "scripted"}, nil)
	backend.SetBindingOpened(func(string, ThreadBinding) *Error {
		return &Error{Category: ErrorTerminal, Message: "checkpoint failed"}
	})
	_, err := backend.RunSupervisor(allocateTestSupervisorTurn(t, backend, testSupervisorTurn("coach", "provider", t.TempDir())))
	assertTerminalError(t, err)
	if got := adapter.runCount(); got != 0 {
		t.Fatalf("turns dispatched = %d, want 0", got)
	}
	if _, exists := backend.Bindings()["coach"]; exists {
		t.Fatal("failed callback left supervisor binding published")
	}
}

func TestHarnessBackendRejectsUndecodableOutcome(t *testing.T) {
	adapter := &scriptedAdapter{name: "scripted"}
	adapter.run = func(RunTurnInput, OnEvent) (Result, *Error) {
		return Result{"next": "done", "notes": 42}, nil
	}
	backend := newTestBackend(t, map[string]HarnessAdapter{"scripted": adapter}, map[string]string{"provider": "scripted"}, nil)
	assertTerminalError(t, runError(t, backend, testTurn("node", "thread", FidelityFull, "provider", t.TempDir())))
}

func TestHarnessBackendValidatesAdapterResultAgainstTurnSchema(t *testing.T) {
	choiceSchema := json.RawMessage(`{
		"type":"object",
		"properties":{"next":{"type":"string","enum":["done"]},"notes":{"type":"string"}},
		"required":["next","notes"],
		"additionalProperties":false
	}`)
	linearSchema := json.RawMessage(`{
		"type":"object",
		"properties":{"notes":{"type":"string"}},
		"required":["notes"],
		"additionalProperties":false
	}`)
	tests := []struct {
		name    string
		schema  json.RawMessage
		result  Result
		want    Outcome
		wantErr bool
	}{
		{name: "missing required next", schema: choiceSchema, result: Result{"notes": "missing"}, wantErr: true},
		{name: "extra forbidden next", schema: linearSchema, result: Result{"next": "done", "notes": "extra"}, wantErr: true},
		{name: "conforming choice", schema: choiceSchema, result: Result{"next": "done", "notes": "chosen"}, want: Outcome{Next: "done", Notes: "chosen"}},
		{name: "conforming linear", schema: linearSchema, result: Result{"notes": "continued"}, want: Outcome{Notes: "continued"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &scriptedAdapter{name: "scripted"}
			adapter.run = func(RunTurnInput, OnEvent) (Result, *Error) {
				return test.result, nil
			}
			backend := newTestBackend(
				t,
				map[string]HarnessAdapter{"scripted": adapter},
				map[string]string{"provider": "scripted"},
				nil,
			)
			turn := testTurn("node", "thread", FidelityFull, "provider", t.TempDir())
			turn.OutputSchema = test.schema
			got, err := backend.Run(allocateTestTurn(t, backend, turn))
			if test.wantErr {
				assertTerminalError(t, err)
				return
			}
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Run() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func assertRunLog(t *testing.T, logsRoot string, wantSegments int) {
	t.Helper()
	index := readJSONLines(t, filepath.Join(logsRoot, "events", "index.jsonl"))
	if len(index) != wantSegments {
		t.Fatalf("index entries = %d, want %d", len(index), wantSegments)
	}
	for i, entry := range index {
		wantSeq := float64(i + 1)
		if entry["seq"] != wantSeq || entry["ts"] == "" || entry["node_id"] == "" {
			t.Fatalf("index entry %d = %#v", i, entry)
		}
		path, ok := entry["path"].(string)
		if !ok || !strings.HasPrefix(path, "events/") {
			t.Fatalf("index path = %#v", entry["path"])
		}
		events := readJSONLines(t, filepath.Join(logsRoot, path))
		if len(events) != 2 {
			t.Fatalf("events in %s = %d, want 2", path, len(events))
		}
		if events[0]["type"] != EventUser || events[1]["type"] != EventAssistant {
			t.Fatalf("events in %s = %#v", path, events)
		}
		for _, event := range events {
			if event["node_id"] != entry["node_id"] || event["ts"] == "" {
				t.Fatalf("unstamped event in %s: %#v", path, event)
			}
		}
	}
}

type scriptedAdapter struct {
	name string
	run  func(RunTurnInput, OnEvent) (Result, *Error)

	mu            sync.Mutex
	next          int
	created       []string
	operationsLog []string
	compactErr    *Error
	steered       []string
	interrupted   []string
}

func (a *scriptedAdapter) CreateSession(_, _ string) (string, *Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	sessionID := fmt.Sprintf("%s-%d", a.name, a.next)
	a.created = append(a.created, sessionID)
	a.operationsLog = append(a.operationsLog, "create:"+sessionID)
	return sessionID, nil
}

func (a *scriptedAdapter) RunTurn(input RunTurnInput, onEvent OnEvent) (Result, *Error) {
	a.mu.Lock()
	a.operationsLog = append(a.operationsLog, "run:"+input.SessionID)
	run := a.run
	a.mu.Unlock()
	if run != nil {
		return run(input, onEvent)
	}
	return Result{"next": "done", "notes": a.name + " result"}, nil
}

func (a *scriptedAdapter) Steer(sessionID string, _ []ContentPart) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steered = append(a.steered, sessionID)
}

func (a *scriptedAdapter) Interrupt(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.interrupted = append(a.interrupted, sessionID)
}

func (a *scriptedAdapter) Compact(sessionID, _ string) *Error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.operationsLog = append(a.operationsLog, "compact:"+sessionID)
	return a.compactErr
}

func (a *scriptedAdapter) createdSessions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.created...)
}

func (a *scriptedAdapter) operations() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.operationsLog...)
}

func (a *scriptedAdapter) runCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, operation := range a.operationsLog {
		if strings.HasPrefix(operation, "run:") {
			count++
		}
	}
	return count
}

func (a *scriptedAdapter) setCompactError(err *Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.compactErr = err
}

func (a *scriptedAdapter) steeredSessions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.steered...)
}

func (a *scriptedAdapter) interruptedSessions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := append([]string(nil), a.interrupted...)
	sort.Strings(result)
	return result
}

type runResult struct {
	node    string
	outcome Outcome
	err     *Error
}

type supervisorRunResult struct {
	verdict Verdict
	err     *Error
}

func runAsync(backend *HarnessBackend, turn CodergenTurn, results chan<- runResult) {
	outcome, err := backend.Run(turn)
	results <- runResult{node: turn.NodeID, outcome: outcome, err: err}
}

func runSupervisorAsync(backend *HarnessBackend, turn SupervisorTurn, results chan<- supervisorRunResult) {
	verdict, err := backend.RunSupervisor(turn)
	results <- supervisorRunResult{verdict: verdict, err: err}
}

func newTestBackend(
	t *testing.T,
	adapters map[string]HarnessAdapter,
	routes map[string]string,
	bindings map[string]ThreadBinding,
) *HarnessBackend {
	t.Helper()
	backend, err := NewHarnessBackend(t.TempDir(), adapters, routes, bindings)
	if err != nil {
		t.Fatalf("NewHarnessBackend() error = %v", err)
	}
	return backend
}

func testTurn(nodeID, threadKey string, fidelity FidelityMode, provider, workdir string) CodergenTurn {
	return testTurnWithPromptAndFidelity(nodeID, threadKey, nodeID, fidelity, provider, workdir)
}

func testTurnWithPrompt(nodeID, threadKey, prompt, workdir string) CodergenTurn {
	return testTurnWithPromptAndFidelity(nodeID, threadKey, prompt, FidelityFull, "provider", workdir)
}

func testTurnWithPromptAndFidelity(
	nodeID, threadKey, prompt string,
	fidelity FidelityMode,
	provider, workdir string,
) CodergenTurn {
	return CodergenTurn{
		NodeID:          nodeID,
		Parts:           textParts(prompt),
		OutputSchema:    outcomeSchema,
		Model:           "model",
		Provider:        provider,
		ReasoningEffort: "medium",
		Fidelity:        fidelity,
		ThreadKey:       threadKey,
		Workdir:         workdir,
	}
}

func testSupervisorTurn(nodeID, provider, workdir string) SupervisorTurn {
	return SupervisorTurn{
		NodeID:          nodeID,
		Parts:           textParts("supervise"),
		OutputSchema:    verdictSchema,
		Model:           "model",
		Provider:        provider,
		ReasoningEffort: "medium",
		Workdir:         workdir,
	}
}

func textParts(text string) []ContentPart {
	return []ContentPart{{Type: ContentPartText, Text: text}}
}

func runError(t *testing.T, backend *HarnessBackend, turn CodergenTurn) *Error {
	t.Helper()
	_, err := backend.Run(allocateTestTurn(t, backend, turn))
	return err
}

func allocateTestTurn(t *testing.T, backend *HarnessBackend, turn CodergenTurn) CodergenTurn {
	t.Helper()
	allocator, err := runlog.New(backend.logsRoot)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := allocator.Allocate(turn.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	turn.RunLog = segment.Path
	return turn
}

func allocateTestSupervisorTurn(t *testing.T, backend *HarnessBackend, turn SupervisorTurn) SupervisorTurn {
	t.Helper()
	allocator, err := runlog.New(backend.logsRoot)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := allocator.Allocate(turn.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	turn.RunLog = segment.Path
	return turn
}

func waitStarted(t *testing.T, started <-chan string, want string) {
	t.Helper()
	if got := <-started; got != want {
		t.Fatalf("started turn = %q, want %q", got, want)
	}
}

func readCurrentTarget(t *testing.T, logsRoot string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(logsRoot, "current.jsonl"))
	if err != nil {
		t.Fatalf("Readlink(current.jsonl): %v", err)
	}
	return target
}

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	})
	var lines []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}
