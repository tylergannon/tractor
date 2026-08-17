// Command harness-conformance runs the shared live HarnessAdapter conformance
// scenarios from docs/spec.md against one native harness.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/claude"
	"github.com/tylergannon/tractor/harness/codex"
)

const (
	turnTimeout     = 8 * time.Minute
	markerDeadline  = 90 * time.Second
	postReturnWatch = 750 * time.Millisecond
	longTaskSeconds = 12
)

type options struct {
	adapter  string
	model    string
	evidence string
}

type scenarioResult struct {
	Name      string        `json:"name"`
	Passed    bool          `json:"passed"`
	Duration  time.Duration `json:"duration"`
	Failure   string        `json:"failure,omitempty"`
	Workspace string        `json:"workspace"`
}

type report struct {
	Adapter       string           `json:"adapter"`
	NativeHarness string           `json:"native_harness"`
	NativeVersion string           `json:"native_version"`
	Model         string           `json:"model"`
	StartedAt     time.Time        `json:"started_at"`
	FinishedAt    time.Time        `json:"finished_at"`
	Passed        bool             `json:"passed"`
	Scenarios     []scenarioResult `json:"scenarios"`
}

type adapterFactory struct {
	name    string
	model   string
	new     func() harness.HarnessAdapter
	mu      sync.Mutex
	closers []interface{ Close() }
}

func (f *adapterFactory) create() harness.HarnessAdapter {
	a := f.new()
	if closer, ok := a.(interface{ Close() }); ok {
		f.mu.Lock()
		f.closers = append(f.closers, closer)
		f.mu.Unlock()
	}
	return a
}

func (f *adapterFactory) close() {
	f.mu.Lock()
	closers := append([]interface{ Close() }(nil), f.closers...)
	f.closers = nil
	f.mu.Unlock()
	for _, closer := range closers {
		closer.Close()
	}
}

type eventLog struct {
	mu     sync.Mutex
	events []harness.Event
}

func (l *eventLog) callback(event harness.Event) {
	raw, _ := json.Marshal(event)
	var copied harness.Event
	_ = json.Unmarshal(raw, &copied)
	l.mu.Lock()
	l.events = append(l.events, copied)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []harness.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]harness.Event(nil), l.events...)
}

func (l *eventLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "harness-conformance:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("harness-conformance", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var values options
	flags.StringVar(&values.adapter, "adapter", "", "native adapter: codex or claude")
	flags.StringVar(&values.model, "model", "", "configured real model")
	flags.StringVar(&values.evidence, "evidence", "", "JSON evidence output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if values.adapter == "" || values.model == "" || values.evidence == "" {
		return errors.New("-adapter, -model, and -evidence are required")
	}
	factory, nativeName, version, err := selectAdapter(values)
	if err != nil {
		return err
	}
	defer factory.close()

	started := time.Now().UTC()
	report := report{
		Adapter:       values.adapter,
		NativeHarness: nativeName,
		NativeVersion: version,
		Model:         values.model,
		StartedAt:     started,
		Passed:        true,
	}
	scenarios := []struct {
		name string
		run  func(*adapterFactory, string) error
	}{
		{"real_workspace_turn", scenarioWorkspace},
		{"continuation_and_reconstruction", scenarioContinuation},
		{"isolation_and_serialization", scenarioIsolation},
		{"invalid_inputs_and_public_errors", scenarioInvalidInputs},
		{"steering", scenarioSteering},
		{"interruption_and_timeout", scenarioInterruption},
		{"compaction", scenarioCompaction},
	}
	root, err := os.MkdirTemp("", "tractor-harness-conformance-")
	if err != nil {
		return fmt.Errorf("create conformance root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	for i, scenario := range scenarios {
		workspace := filepath.Join(root, fmt.Sprintf("%02d-%s", i+1, scenario.name))
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			return fmt.Errorf("create scenario workspace: %w", err)
		}
		begin := time.Now()
		runErr := scenario.run(factory, workspace)
		result := scenarioResult{
			Name:      scenario.name,
			Passed:    runErr == nil,
			Duration:  time.Since(begin).Round(time.Millisecond),
			Workspace: workspace,
		}
		if runErr != nil {
			result.Failure = runErr.Error()
			report.Passed = false
		}
		report.Scenarios = append(report.Scenarios, result)
		status := "PASS"
		if runErr != nil {
			status = "FAIL: " + runErr.Error()
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s %s (%s)\n", scenario.name, status, result.Duration)
	}
	report.FinishedAt = time.Now().UTC()
	if err := writeReport(values.evidence, report); err != nil {
		return err
	}
	if !report.Passed {
		return fmt.Errorf("one or more scenarios failed; evidence: %s", values.evidence)
	}
	_, _ = fmt.Fprintf(os.Stdout, "all conformance scenarios passed; evidence: %s\n", values.evidence)
	return nil
}

func selectAdapter(values options) (*adapterFactory, string, string, error) {
	var command string
	var args []string
	var construct func() harness.HarnessAdapter
	switch values.adapter {
	case "codex":
		command = "codex"
		args = []string{"--version"}
		construct = func() harness.HarnessAdapter { return codex.New() }
	case "claude":
		command = "claude"
		args = []string{"--version"}
		construct = func() harness.HarnessAdapter { return claude.New() }
	default:
		return nil, "", "", fmt.Errorf("unsupported adapter %q", values.adapter)
	}
	output, err := exec.Command(command, args...).CombinedOutput()
	if err != nil {
		return nil, "", "", fmt.Errorf("read %s version: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return nil, "", "", fmt.Errorf("%s reported an empty version", command)
	}
	return &adapterFactory{name: values.adapter, model: values.model, new: construct}, command, version, nil
}

func writeReport(path string, value report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create evidence directory: %w", err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evidence: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write evidence: %w", err)
	}
	return nil
}

func scenarioWorkspace(factory *adapterFactory, workdir string) error {
	a := factory.create()
	nonce := token()
	input := "source-" + nonce
	if err := os.WriteFile(filepath.Join(workdir, "input.txt"), []byte(input+"\n"), 0o644); err != nil {
		return err
	}
	property := "result_" + nonce
	nested := "nested_" + nonce
	first, second := "first_"+nonce, "second_"+nonce
	schema := objectSchema(map[string]any{
		property: map[string]any{
			"type": "object",
			"properties": map[string]any{
				nested: map[string]any{"type": "string"},
				"order": map[string]any{
					"type":     "array",
					"items":    map[string]any{"enum": []string{first, second}},
					"minItems": 2,
					"maxItems": 2,
				},
			},
			"required":             []string{nested, "order"},
			"additionalProperties": false,
		},
	}, []string{property})
	parts := []harness.ContentPart{{Type: harness.ContentPartText, Text: fmt.Sprintf("Read input.txt using a workspace tool. Write its exact trimmed contents to output.txt using a workspace tool. Return the structured result with %s.%s equal to that content and %s.order containing the schema's enum choices in their displayed order.", property, nested, property)}}
	sessionID, createErr := a.CreateSession(factory.model, workdir)
	if createErr != nil {
		return fmt.Errorf("create session: %w", createErr)
	}
	log := &eventLog{}
	result, runErr := a.RunTurn(turn(sessionID, factory.model, workdir, parts, schema, turnTimeout), log.callback)
	if runErr != nil {
		return fmt.Errorf("run turn: %w", runErr)
	}
	outer, ok := result[property].(map[string]any)
	if !ok || outer[nested] != input || !reflect.DeepEqual(outer["order"], []any{first, second}) {
		return fmt.Errorf("unexpected structured result: %#v", result)
	}
	written, err := os.ReadFile(filepath.Join(workdir, "output.txt"))
	if err != nil || strings.TrimSpace(string(written)) != input {
		return fmt.Errorf("workspace result mismatch: content=%q err=%v", strings.TrimSpace(string(written)), err)
	}
	events := log.snapshot()
	if err := verifyWorkspaceEvents(events, parts); err != nil {
		return err
	}
	before := len(events)
	time.Sleep(postReturnWatch)
	if after := log.count(); after != before {
		return fmt.Errorf("callbacks continued after RunTurn returned: before=%d after=%d", before, after)
	}
	return nil
}

func scenarioContinuation(factory *adapterFactory, workdir string) error {
	first := factory.create()
	secret := "remember-" + token()
	sessionID, createErr := first.CreateSession(factory.model, workdir)
	if createErr != nil {
		return fmt.Errorf("create session: %w", createErr)
	}
	ack := "ack_" + token()
	_, runErr := first.RunTurn(turn(sessionID, factory.model, workdir,
		textParts("Remember this private value for a later turn: "+secret+". Do not write it to the workspace. Return the requested acknowledgement."),
		objectSchema(map[string]any{"ack": map[string]any{"enum": []string{ack}}}, []string{"ack"}), turnTimeout), func(harness.Event) {})
	if runErr != nil {
		return fmt.Errorf("establish memory: %w", runErr)
	}
	second := factory.create()
	key := "recalled_" + token()
	result, revisitErr := second.RunTurn(turn(sessionID, factory.model, workdir,
		textParts("Return the private value I asked you to remember in the previous turn under the schema's only property."),
		objectSchema(map[string]any{key: map[string]any{"type": "string"}}, []string{key}), turnTimeout), func(harness.Event) {})
	if revisitErr != nil {
		return fmt.Errorf("revisit after adapter reconstruction: %w", revisitErr)
	}
	if result[key] != secret {
		return fmt.Errorf("recalled value mismatch: got %#v", result[key])
	}
	return nil
}

func scenarioIsolation(factory *adapterFactory, workdir string) error {
	a := factory.create()
	dirA, dirB := filepath.Join(workdir, "a"), filepath.Join(workdir, "b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		return err
	}
	idA, errA := a.CreateSession(factory.model, dirA)
	if errA != nil {
		return errA
	}
	idB, errB := a.CreateSession(factory.model, dirB)
	if errB != nil {
		return errB
	}
	nonceA, nonceB := token(), token()
	type outcome struct {
		result harness.Result
		err    *harness.Error
		log    *eventLog
		ended  time.Time
	}
	runLong := func(id, dir, nonce string, output chan<- outcome) {
		log := &eventLog{}
		result, runErr := a.RunTurn(turn(id, factory.model, dir, textParts(longTaskPrompt(nonce, "started", "finished", longTaskSeconds)), doneSchema(nonce), turnTimeout), log.callback)
		output <- outcome{result: result, err: runErr, log: log, ended: time.Now()}
	}
	firstA, firstB := make(chan outcome, 1), make(chan outcome, 1)
	go runLong(idA, dirA, nonceA, firstA)
	go runLong(idB, dirB, nonceB, firstB)
	if err := waitFiles(markerDeadline, filepath.Join(dirA, "started"), filepath.Join(dirB, "started")); err != nil {
		a.Interrupt(idA)
		a.Interrupt(idB)
		return err
	}
	secondA := make(chan outcome, 1)
	secondMarker := "second_started"
	go func() {
		log := &eventLog{}
		result, runErr := a.RunTurn(turn(idA, factory.model, dirA,
			textParts(fmt.Sprintf("Write %s to %s using a workspace tool, then return the requested result.", nonceA, secondMarker)),
			doneSchema(nonceA), turnTimeout), log.callback)
		secondA <- outcome{result: result, err: runErr, log: log, ended: time.Now()}
	}()
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dirA, secondMarker)); err == nil {
		return errors.New("second turn for session A overlapped its live first turn")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	aResult := <-firstA
	bResult := <-firstB
	secondResult := <-secondA
	if aResult.err != nil || bResult.err != nil {
		return fmt.Errorf("concurrent turns failed: A=%v B=%v", aResult.err, bResult.err)
	}
	if secondResult.err != nil && !secondResult.err.Valid() {
		return fmt.Errorf("serialized second turn returned invalid error: %v", secondResult.err)
	}
	if secondResult.err == nil && secondResult.ended.Before(aResult.ended) {
		return errors.New("second turn for session A completed before its first turn")
	}
	if aResult.result["done"] != nonceA || bResult.result["done"] != nonceB {
		return fmt.Errorf("results mixed: A=%#v B=%#v", aResult.result, bResult.result)
	}
	if err := assertFile(filepath.Join(dirA, "finished"), nonceA); err != nil {
		return err
	}
	if err := assertFile(filepath.Join(dirB, "finished"), nonceB); err != nil {
		return err
	}
	if fileExists(filepath.Join(dirA, "finished")) && fileContains(filepath.Join(dirA, "finished"), nonceB) {
		return errors.New("session B nonce appeared in session A workspace")
	}
	if eventsContain(aResult.log.snapshot(), nonceB) || eventsContain(bResult.log.snapshot(), nonceA) {
		return errors.New("concurrent event streams mixed")
	}
	return nil
}

func scenarioInvalidInputs(factory *adapterFactory, workdir string) error {
	a := factory.create()
	unknown := uuidLike()
	unknownFile := "unknown-" + token()
	log := &eventLog{}
	_, runErr := a.RunTurn(turn(unknown, factory.model, workdir,
		textParts("Write data to "+unknownFile+" and return the result."), doneSchema(token()), 45*time.Second), log.callback)
	if err := requirePublicError("unknown session", runErr); err != nil {
		return err
	}
	if hasAssistantOrTool(log.snapshot()) || fileExists(filepath.Join(workdir, unknownFile)) {
		return errors.New("unknown session produced assistant/tool events or workspace changes")
	}

	badModel := "tractor-model-does-not-exist-" + token()
	badFile := "bad-model-" + token()
	id, createErr := a.CreateSession(badModel, workdir)
	if createErr != nil {
		return requirePublicError("nonexistent model create", createErr)
	}
	badLog := &eventLog{}
	_, badRunErr := a.RunTurn(turn(id, badModel, workdir,
		textParts("Write data to "+badFile+" and return the result."), doneSchema(token()), 45*time.Second), badLog.callback)
	if err := requirePublicError("nonexistent model run", badRunErr); err != nil {
		return err
	}
	if hasAssistantOrTool(badLog.snapshot()) || fileExists(filepath.Join(workdir, badFile)) {
		return errors.New("nonexistent model produced assistant/tool events or workspace changes")
	}
	return nil
}

func scenarioSteering(factory *adapterFactory, workdir string) error {
	a := factory.create()
	activeDir, inactiveDir := filepath.Join(workdir, "active"), filepath.Join(workdir, "inactive")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(inactiveDir, 0o755); err != nil {
		return err
	}
	id, createErr := a.CreateSession(factory.model, activeDir)
	if createErr != nil {
		return createErr
	}
	nonce := token()
	log := &eventLog{}
	type outcome struct {
		result harness.Result
		err    *harness.Error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := a.RunTurn(turn(id, factory.model, activeDir,
			textParts(longTaskPrompt(nonce, "started", "finished", longTaskSeconds)), doneSchema(nonce), turnTimeout), log.callback)
		done <- outcome{result, runErr}
	}()
	if err := waitFiles(markerDeadline, filepath.Join(activeDir, "started")); err != nil {
		a.Interrupt(id)
		return err
	}
	steerValue := "steer-" + token()
	steerText := fmt.Sprintf("Before returning, use a workspace tool to write exactly %s to steered.txt.", steerValue)
	steerParts := textParts(steerText)
	a.Steer(id, steerParts)
	activeResult := <-done
	if activeResult.err != nil {
		return fmt.Errorf("steered turn: %w", activeResult.err)
	}
	if activeResult.result["done"] != nonce {
		return fmt.Errorf("steered result mismatch: %#v", activeResult.result)
	}
	if err := assertFile(filepath.Join(activeDir, "steered.txt"), steerValue); err != nil {
		return fmt.Errorf("steering workspace effect: %w", err)
	}
	userEvents := eventsOfType(log.snapshot(), harness.EventUser)
	if len(userEvents) != 2 {
		return fmt.Errorf("expected initial plus exactly one steering user event, got %d", len(userEvents))
	}
	if err := eventPartsEqual(userEvents[1], steerParts); err != nil {
		return fmt.Errorf("steering event: %w", err)
	}

	inactiveID, inactiveCreateErr := a.CreateSession(factory.model, inactiveDir)
	if inactiveCreateErr != nil {
		return inactiveCreateErr
	}
	inactiveValue := "inactive-" + token()
	inactiveParts := textParts("Write exactly " + inactiveValue + " to should-not-exist.txt")
	a.Steer(inactiveID, inactiveParts)
	inactiveLog := &eventLog{}
	firstParts := textParts("Return the requested result without changing the workspace.")
	_, inactiveRunErr := a.RunTurn(turn(inactiveID, factory.model, inactiveDir, firstParts, doneSchema(inactiveValue), turnTimeout), inactiveLog.callback)
	if inactiveRunErr != nil {
		return fmt.Errorf("first inactive-session turn: %w", inactiveRunErr)
	}
	inactiveUsers := eventsOfType(inactiveLog.snapshot(), harness.EventUser)
	if len(inactiveUsers) != 1 {
		return fmt.Errorf("inactive steering queued a user event: count=%d", len(inactiveUsers))
	}
	if err := eventPartsEqual(inactiveUsers[0], firstParts); err != nil {
		return err
	}
	if fileExists(filepath.Join(inactiveDir, "should-not-exist.txt")) {
		return errors.New("inactive steering produced a workspace effect")
	}
	return nil
}

func scenarioInterruption(factory *adapterFactory, workdir string) error {
	a := factory.create()
	interruptDir, timeoutDir := filepath.Join(workdir, "interrupt"), filepath.Join(workdir, "timeout")
	if err := os.MkdirAll(interruptDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(timeoutDir, 0o755); err != nil {
		return err
	}
	interruptID, createErr := a.CreateSession(factory.model, interruptDir)
	if createErr != nil {
		return createErr
	}
	nonce := token()
	log := &eventLog{}
	type turnResult struct {
		err *harness.Error
		at  time.Time
	}
	done := make(chan turnResult, 1)
	startedAt := time.Now()
	go func() {
		_, runErr := a.RunTurn(turn(interruptID, factory.model, interruptDir,
			textParts(longTaskPrompt(nonce, "started", "finished", 20)), doneSchema(nonce), turnTimeout), log.callback)
		done <- turnResult{runErr, time.Now()}
	}()
	if err := waitFiles(markerDeadline, filepath.Join(interruptDir, "started")); err != nil {
		a.Interrupt(interruptID)
		return err
	}
	interruptCalled := time.Now()
	a.Interrupt(interruptID)
	var interruptedResult turnResult
	select {
	case interruptedResult = <-done:
	case <-time.After(10 * time.Second):
		return errors.New("interrupted RunTurn did not return promptly")
	}
	if interruptedResult.err == nil || interruptedResult.err.Category != harness.ErrorInterrupted || !interruptedResult.err.Valid() {
		return fmt.Errorf("interrupt returned wrong error: %v", interruptedResult.err)
	}
	if interruptedResult.at.Sub(interruptCalled) >= 10*time.Second || interruptedResult.at.Sub(startedAt) >= 20*time.Second {
		return fmt.Errorf("interrupt was not materially shorter than normal duration: elapsed=%s", interruptedResult.at.Sub(startedAt))
	}
	callbackCount := log.count()
	time.Sleep(postReturnWatch)
	if log.count() != callbackCount {
		return errors.New("callback arrived after interrupted RunTurn returned")
	}
	if err := reuseSession(a, factory.model, interruptID, interruptDir); err != nil {
		return fmt.Errorf("reuse after interrupt: %w", err)
	}

	timeoutID, timeoutCreateErr := a.CreateSession(factory.model, timeoutDir)
	if timeoutCreateErr != nil {
		return timeoutCreateErr
	}
	timeoutNonce := token()
	timeoutStart := time.Now()
	_, timeoutErr := a.RunTurn(turn(timeoutID, factory.model, timeoutDir,
		textParts(longTaskPrompt(timeoutNonce, "started", "finished", 20)), doneSchema(timeoutNonce), 6*time.Second), func(harness.Event) {})
	if timeoutErr == nil || timeoutErr.Category != harness.ErrorInterrupted || !timeoutErr.Valid() {
		return fmt.Errorf("timeout returned wrong error: %v", timeoutErr)
	}
	if time.Since(timeoutStart) >= 15*time.Second {
		return fmt.Errorf("timeout did not end before normal completion: %s", time.Since(timeoutStart))
	}
	if err := reuseSession(a, factory.model, timeoutID, timeoutDir); err != nil {
		return fmt.Errorf("reuse after timeout: %w", err)
	}
	return nil
}

func scenarioCompaction(factory *adapterFactory, workdir string) error {
	a := factory.create()
	memoryDir, activeDir := filepath.Join(workdir, "memory"), filepath.Join(workdir, "active")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return err
	}
	id, createErr := a.CreateSession(factory.model, memoryDir)
	if createErr != nil {
		return createErr
	}
	secret := "compact-memory-" + token()
	ack := "ack_" + token()
	_, establishErr := a.RunTurn(turn(id, factory.model, memoryDir,
		textParts("Remember this private value for a later turn: "+secret+". Do not write it to the workspace. Return the requested acknowledgement."),
		objectSchema(map[string]any{"ack": map[string]any{"enum": []string{ack}}}, []string{"ack"}), turnTimeout), func(harness.Event) {})
	if establishErr != nil {
		return fmt.Errorf("establish compaction memory: %w", establishErr)
	}
	if compactErr := a.Compact(id, memoryDir); compactErr != nil {
		return fmt.Errorf("compact established session: %w", compactErr)
	}
	key := "recalled_" + token()
	result, revisitErr := a.RunTurn(turn(id, factory.model, memoryDir,
		textParts("Return the private value I asked you to remember under the schema's only property."),
		objectSchema(map[string]any{key: map[string]any{"type": "string"}}, []string{key}), turnTimeout), func(harness.Event) {})
	if revisitErr != nil {
		return fmt.Errorf("revisit compacted session: %w", revisitErr)
	}
	if result[key] != secret {
		return fmt.Errorf("compacted session recalled %#v", result[key])
	}

	activeID, activeCreateErr := a.CreateSession(factory.model, activeDir)
	if activeCreateErr != nil {
		return activeCreateErr
	}
	nonce := token()
	type outcome struct {
		result harness.Result
		err    *harness.Error
	}
	done := make(chan outcome, 1)
	go func() {
		result, runErr := a.RunTurn(turn(activeID, factory.model, activeDir,
			textParts(longTaskPrompt(nonce, "started", "finished", 10)), doneSchema(nonce), turnTimeout), func(harness.Event) {})
		done <- outcome{result, runErr}
	}()
	if err := waitFiles(markerDeadline, filepath.Join(activeDir, "started")); err != nil {
		a.Interrupt(activeID)
		return err
	}
	compactStart := time.Now()
	activeCompactErr := a.Compact(activeID, activeDir)
	if err := requirePublicError("active compact", activeCompactErr); err != nil {
		return err
	}
	if time.Since(compactStart) > 5*time.Second {
		return fmt.Errorf("active compact did not return promptly: %s", time.Since(compactStart))
	}
	activeResult := <-done
	if activeResult.err != nil || activeResult.result["done"] != nonce {
		return fmt.Errorf("active compact disrupted turn: result=%#v err=%v", activeResult.result, activeResult.err)
	}
	if err := assertFile(filepath.Join(activeDir, "finished"), nonce); err != nil {
		return fmt.Errorf("active turn finished marker: %w", err)
	}
	unknownErr := a.Compact(uuidLike(), workdir)
	if err := requirePublicError("unknown compact", unknownErr); err != nil {
		return err
	}
	return nil
}

func turn(sessionID, model, workdir string, parts []harness.ContentPart, schema json.RawMessage, timeout time.Duration) harness.RunTurnInput {
	return harness.RunTurnInput{
		SessionID:       sessionID,
		Model:           model,
		ReasoningEffort: "medium",
		OutputSchema:    schema,
		Workdir:         workdir,
		Parts:           parts,
		Timeout:         timeout,
	}
}

func objectSchema(properties map[string]any, required []string) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func doneSchema(nonce string) json.RawMessage {
	return objectSchema(map[string]any{"done": map[string]any{"enum": []string{nonce}}}, []string{"done"})
}

func textParts(text string) []harness.ContentPart {
	return []harness.ContentPart{{Type: harness.ContentPartText, Text: text}}
}

func longTaskPrompt(nonce, started, finished string, seconds int) string {
	return fmt.Sprintf("Use a workspace shell tool to run one foreground shell command that writes exactly %s to %s, sleeps for %d seconds, then writes exactly %s to %s. Wait for the command. Then return the requested structured result.", nonce, started, seconds, nonce, finished)
}

func reuseSession(a harness.HarnessAdapter, model, sessionID, workdir string) error {
	nonce := token()
	file := "reused-" + nonce
	result, runErr := a.RunTurn(turn(sessionID, model, workdir,
		textParts(fmt.Sprintf("Use a workspace tool to write exactly %s to %s, then return the requested result.", nonce, file)),
		doneSchema(nonce), turnTimeout), func(harness.Event) {})
	if runErr != nil {
		return runErr
	}
	if result["done"] != nonce {
		return fmt.Errorf("unexpected result: %#v", result)
	}
	return assertFile(filepath.Join(workdir, file), nonce)
}

func verifyWorkspaceEvents(events []harness.Event, parts []harness.ContentPart) error {
	if len(events) == 0 {
		return errors.New("event stream was empty")
	}
	if eventType(events[0]) != harness.EventUser {
		return fmt.Errorf("first event type was %q, want user", eventType(events[0]))
	}
	if err := eventPartsEqual(events[0], parts); err != nil {
		return fmt.Errorf("initial user event: %w", err)
	}
	if len(eventsOfType(events, harness.EventAssistant)) == 0 {
		return errors.New("event stream contained no complete assistant content")
	}
	calls := make(map[string]struct{})
	results := make(map[string]struct{})
	for _, event := range events {
		switch eventType(event) {
		case harness.EventToolCall:
			if id, ok := event["call_id"].(string); ok && id != "" {
				calls[id] = struct{}{}
			}
		case harness.EventToolResult:
			if id, ok := event["call_id"].(string); ok && id != "" {
				results[id] = struct{}{}
			}
		}
	}
	if len(calls) == 0 {
		return errors.New("event stream contained no tool call")
	}
	for id := range calls {
		if _, ok := results[id]; ok {
			return nil
		}
	}
	callIDs, resultIDs := mapKeys(calls), mapKeys(results)
	return fmt.Errorf("tool calls and results were not paired: calls=%v results=%v", callIDs, resultIDs)
}

func eventPartsEqual(event harness.Event, want []harness.ContentPart) error {
	raw, err := json.Marshal(event["parts"])
	if err != nil {
		return err
	}
	var got []harness.ContentPart
	if err := json.Unmarshal(raw, &got); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("parts mismatch: got=%#v want=%#v", got, want)
	}
	return nil
}

func eventType(event harness.Event) string {
	value, _ := event["type"].(string)
	return value
}

func eventsOfType(events []harness.Event, kind string) []harness.Event {
	var selected []harness.Event
	for _, event := range events {
		if eventType(event) == kind {
			selected = append(selected, event)
		}
	}
	return selected
}

func eventsContain(events []harness.Event, needle string) bool {
	raw, _ := json.Marshal(events)
	return strings.Contains(string(raw), needle)
}

func hasAssistantOrTool(events []harness.Event) bool {
	for _, event := range events {
		switch eventType(event) {
		case harness.EventAssistant, harness.EventToolCall, harness.EventToolResult:
			return true
		}
	}
	return false
}

func requirePublicError(label string, err *harness.Error) error {
	if err == nil {
		return fmt.Errorf("%s returned success, want Error", label)
	}
	if !err.Valid() {
		return fmt.Errorf("%s returned invalid Error: %#v", label, err)
	}
	return nil
}

func waitFiles(timeout time.Duration, paths ...string) error {
	deadline := time.Now().Add(timeout)
	for {
		all := true
		for _, path := range paths {
			if !fileExists(path) {
				all = false
				break
			}
		}
		if all {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for markers: %v", paths)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertFile(path, want string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if got := strings.TrimSpace(string(raw)); got != want {
		return fmt.Errorf("%s content=%q want=%q", path, got, want)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileContains(path, value string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(raw), value)
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func token() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func uuidLike() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexValue[:8], hexValue[8:12], hexValue[12:16], hexValue[16:20], hexValue[20:])
}
