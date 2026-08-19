// Package agy implements Tractor's HarnessAdapter using Google Antigravity's
// `agy` print-mode CLI.
package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/agy/schema"
)

const (
	controlTimeout = 5 * time.Second
	createTimeout  = 2 * time.Minute
	defaultTimeout = 24 * time.Hour
)

type runnerConfig struct {
	binary   string
	baseArgs []string
	env      []string
}

// Adapter translates the neutral harness contract to agy's stream-json CLI.
type Adapter struct {
	mu     sync.Mutex
	closed bool
	stderr io.Writer
	states map[string]*sessionState
	config runnerConfig
}

type sessionState struct {
	opMu         sync.Mutex
	mu           sync.Mutex
	workdir      string
	active       *activeTurn
	pendingSteer [][]harness.ContentPart
}

type activeTurn struct {
	command     *exec.Cmd
	done        chan struct{}
	interrupted atomic.Bool
	steered     atomic.Bool
}

type runRequest struct {
	prompt          string
	model           string
	effort          string
	workdir         string
	sessionID       string
	newProject      bool
	outputSchema    string
	timeout         time.Duration
	projector       *eventProjector
	requireResultID bool
}

type nativeResult struct {
	conversationID string
	status         string
	errorMessage   string
	structured     any
	echoedSchema   any
}

// New constructs an adapter that launches `agy` from PATH.
func New() *Adapter {
	return newAdapter(runnerConfig{binary: "agy", env: os.Environ()})
}

func newAdapter(config runnerConfig) *Adapter {
	return &Adapter{config: config, states: make(map[string]*sessionState)}
}

// SetStderr forwards agy stderr while retaining a copy for error reporting.
func (a *Adapter) SetStderr(stderr io.Writer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stderr = stderr
}

// CreateSession performs a short native turn so agy mints and persists a real
// conversation bound to workdir.
func (a *Adapter) CreateSession(model, workdir string) (string, *harness.Error) {
	if err := harness.ValidateCreateSessionInput(model, workdir); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(workdir)
	if err != nil {
		return "", terminal(fmt.Sprintf("resolve working directory: %v", err))
	}
	result, _, runErr := a.runOnce(runRequest{
		prompt:          "Reply with the single word OK. Do not use any tools.",
		model:           model,
		workdir:         absolute,
		newProject:      true,
		timeout:         createTimeout,
		requireResultID: true,
	})
	if runErr != nil {
		return "", runErr
	}
	if strings.TrimSpace(result.conversationID) == "" {
		return "", terminal("agy create result omitted conversation_id")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", terminal("agy adapter is closed")
	}
	a.states[result.conversationID] = &sessionState{workdir: absolute}
	return result.conversationID, nil
}

// RunTurn runs one resumed structured-output turn and validates the exact
// caller schema. One bounded repair turn is attempted for invalid output.
func (a *Adapter) RunTurn(input harness.RunTurnInput, onEvent harness.OnEvent) (harness.Result, *harness.Error) {
	if err := harness.ValidateRunTurnInput(input, onEvent); err != nil {
		return nil, err
	}
	validator, validationErr := harness.NewResultValidator(input.OutputSchema)
	if validationErr != nil {
		return nil, validationErr
	}
	absolute, pathErr := filepath.Abs(input.Workdir)
	if pathErr != nil {
		return nil, terminal(fmt.Sprintf("resolve working directory: %v", pathErr))
	}
	state, stateErr := a.state(input.SessionID)
	if stateErr != nil {
		return nil, stateErr
	}
	state.opMu.Lock()
	defer state.opMu.Unlock()
	if err := bindWorkdir(state, absolute); err != nil {
		return nil, err
	}

	schemaFile, err := writeSchema(input.OutputSchema)
	if err != nil {
		return nil, terminal(fmt.Sprintf("write agy output schema: %v", err))
	}
	defer func() { _ = os.Remove(schemaFile) }()
	projector := newEventProjector(onEvent)
	projector.user(input.Parts)
	deadline := time.Time{}
	if input.Timeout > 0 {
		deadline = time.Now().Add(input.Timeout)
	}
	request := runRequest{
		prompt:          joinParts(input.Parts),
		model:           input.Model,
		effort:          input.ReasoningEffort,
		workdir:         absolute,
		sessionID:       input.SessionID,
		outputSchema:    schemaFile,
		timeout:         remaining(deadline, input.Timeout),
		projector:       projector,
		requireResultID: true,
	}
	if request.timeout <= 0 && input.Timeout > 0 {
		return nil, interrupted("turn timed out and was interrupted")
	}
	result, active, runErr := a.runWithSteering(state, request, deadline, input.Timeout)
	if runErr != nil {
		if !isArtifactPathError(runErr) {
			return nil, runErr
		}
		repairPrompt := artifactWriteRepairPrompt(runErr.Message)
		repairParts := []harness.ContentPart{{Type: harness.ContentPartText, Text: repairPrompt}}
		projector.user(repairParts)
		request.prompt = repairPrompt
		request.timeout = remaining(deadline, input.Timeout)
		if request.timeout <= 0 && input.Timeout > 0 {
			return nil, interrupted("turn timed out and was interrupted")
		}
		result, active, runErr = a.runWithSteering(state, request, deadline, input.Timeout)
		if runErr != nil {
			return nil, runErr
		}
	}
	validated, invalid := validateStructured(validator, result.structured)
	if mismatch := compareEchoedSchema(input.OutputSchema, result.echoedSchema); mismatch != nil {
		return nil, mismatch
	}
	if invalid == nil {
		return validated, nil
	}
	repairPrompt := fmt.Sprintf("Your previous structured output did not conform to the required JSON schema: %s. Respond again. Output only a JSON object conforming exactly to the schema. Do not run any tools.", invalid.Message)
	repairParts := []harness.ContentPart{{Type: harness.ContentPartText, Text: repairPrompt}}
	projector.user(repairParts)
	request.prompt = repairPrompt
	request.timeout = remaining(deadline, input.Timeout)
	if request.timeout <= 0 && input.Timeout > 0 {
		active.interrupted.Store(true)
		return nil, interrupted("turn timed out and was interrupted")
	}
	result, _, runErr = a.runWithSteering(state, request, deadline, input.Timeout)
	if runErr != nil {
		return nil, runErr
	}
	validated, invalid = validateStructured(validator, result.structured)
	if mismatch := compareEchoedSchema(input.OutputSchema, result.echoedSchema); mismatch != nil {
		return nil, mismatch
	}
	if invalid != nil {
		return nil, terminal("agy result does not conform to the supplied schema: " + invalid.Message)
	}
	return validated, nil
}

// Steer interrupts the active print process and resumes its native
// conversation with the steering message inside the same logical RunTurn.
func (a *Adapter) Steer(sessionID string, parts []harness.ContentPart) {
	if strings.TrimSpace(sessionID) == "" || harness.ValidateContentParts(parts) != nil {
		return
	}
	state := a.lookup(sessionID)
	if state == nil {
		return
	}
	copied := append([]harness.ContentPart(nil), parts...)
	state.mu.Lock()
	active := state.active
	if active != nil {
		state.pendingSteer = append(state.pendingSteer, copied)
		active.steered.Store(true)
	}
	state.mu.Unlock()
	if active != nil {
		interruptProcess(active)
	}
}

// Interrupt signals the active native process and returns immediately.
func (a *Adapter) Interrupt(sessionID string) {
	state := a.lookup(sessionID)
	if state == nil {
		return
	}
	state.mu.Lock()
	active := state.active
	state.mu.Unlock()
	if active != nil {
		interruptProcess(active)
	}
}

// Compact asks agy to compact the native conversation with its /compact
// command. The operation is rejected while another turn is active.
func (a *Adapter) Compact(sessionID, workdir string) *harness.Error {
	if err := harness.ValidateSessionInput(sessionID, workdir); err != nil {
		return err
	}
	absolute, err := filepath.Abs(workdir)
	if err != nil {
		return terminal(fmt.Sprintf("resolve working directory: %v", err))
	}
	state, _, stateErr := a.stateWithExistence(sessionID)
	if stateErr != nil {
		return stateErr
	}
	if !state.opMu.TryLock() {
		return terminal("cannot compact a session with an active operation")
	}
	defer state.opMu.Unlock()
	if err := bindWorkdir(state, absolute); err != nil {
		return err
	}
	_, _, runErr := a.runForState(state, runRequest{
		prompt:          "/compact",
		workdir:         absolute,
		sessionID:       sessionID,
		timeout:         createTimeout,
		requireResultID: true,
	})
	if runErr != nil {
		a.removeState(sessionID, state)
	}
	return runErr
}

// Close interrupts all active agy processes.
func (a *Adapter) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	states := make([]*sessionState, 0, len(a.states))
	for _, state := range a.states {
		states = append(states, state)
	}
	a.mu.Unlock()
	for _, state := range states {
		state.mu.Lock()
		active := state.active
		state.mu.Unlock()
		if active != nil {
			interruptProcess(active)
		}
	}
}

func (a *Adapter) runForState(state *sessionState, request runRequest) (nativeResult, *activeTurn, *harness.Error) {
	result, active, err := a.runOnceWithActive(request, func(active *activeTurn) {
		state.mu.Lock()
		state.active = active
		state.mu.Unlock()
	})
	state.mu.Lock()
	if state.active == active {
		state.active = nil
	}
	state.mu.Unlock()
	return result, active, err
}

func (a *Adapter) runWithSteering(
	state *sessionState,
	request runRequest,
	deadline time.Time,
	originalTimeout time.Duration,
) (nativeResult, *activeTurn, *harness.Error) {
	for {
		result, active, err := a.runForState(state, request)
		parts := takeSteer(state)
		if len(parts) == 0 {
			return result, active, err
		}
		if err != nil && (active == nil || !active.steered.Load() || err.Category != harness.ErrorInterrupted) {
			return nativeResult{}, active, err
		}
		request.projector.user(parts)
		request.prompt = joinParts(parts)
		request.timeout = remaining(deadline, originalTimeout)
		if request.timeout <= 0 && originalTimeout > 0 {
			return nativeResult{}, active, interrupted("turn timed out and was interrupted")
		}
	}
}

func takeSteer(state *sessionState) []harness.ContentPart {
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.pendingSteer) == 0 {
		return nil
	}
	parts := state.pendingSteer[0]
	state.pendingSteer = state.pendingSteer[1:]
	return parts
}

func (a *Adapter) runOnce(request runRequest) (nativeResult, *activeTurn, *harness.Error) {
	return a.runOnceWithActive(request, nil)
}

func (a *Adapter) runOnceWithActive(request runRequest, started func(*activeTurn)) (nativeResult, *activeTurn, *harness.Error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nativeResult{}, nil, terminal("agy adapter is closed")
	}
	config := a.config
	stderrWriter := a.stderr
	a.mu.Unlock()

	args := append([]string(nil), config.baseArgs...)
	args = append(args, "-p", request.prompt, "--output-format", "stream-json", "--dangerously-skip-permissions", "--add-dir", request.workdir)
	if request.newProject {
		args = append(args, "--new-project")
	} else {
		args = append(args, "--conversation", request.sessionID)
	}
	if request.model != "" {
		args = append(args, "--model", request.model)
	}
	if request.effort != "" && !modelIncludesEffort(request.model) {
		args = append(args, "--effort", request.effort)
	}
	if request.outputSchema != "" {
		args = append(args, "--json-schema", request.outputSchema)
	}
	backstop := defaultTimeout
	if request.timeout > 0 {
		backstop = request.timeout + 30*time.Second
	}
	args = append(args, "--print-timeout", backstop.String())

	command := exec.Command(config.binary, args...)
	command.Dir = request.workdir
	command.Env = config.env
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nativeResult{}, nil, categorize(err, false)
	}
	var stderr bytes.Buffer
	if stderrWriter != nil {
		command.Stderr = io.MultiWriter(stderrWriter, &stderr)
	} else {
		command.Stderr = &stderr
	}
	if err := command.Start(); err != nil {
		return nativeResult{}, nil, categorize(err, false)
	}
	active := &activeTurn{command: command, done: make(chan struct{})}
	if started != nil {
		started(active)
	}
	envelopes := make(chan schema.Envelope)
	go scanStream(stdout, envelopes)
	waited := make(chan error, 1)
	go func() {
		waited <- command.Wait()
		close(active.done)
	}()

	var timer <-chan time.Time
	var timeout *time.Timer
	if request.timeout > 0 {
		timeout = time.NewTimer(request.timeout)
		timer = timeout.C
		defer timeout.Stop()
	}
	var result *nativeResult
	initSeen := false
	initID := ""
	var processErr error
	var protocolErr *harness.Error
	for envelopes != nil || waited != nil {
		select {
		case envelope, ok := <-envelopes:
			if !ok {
				envelopes = nil
				continue
			}
			if request.projector != nil {
				request.projector.envelope(envelope)
			}
			if envelope.Event == "init" {
				initSeen = true
				id := envelopeConversationID(envelope)
				initID = id
				if request.sessionID != "" && id != request.sessionID {
					interruptProcess(active)
					protocolErr = terminal("agy init returned a different conversation ID")
				}
			}
			if envelope.Event == "result" && envelope.Result != nil {
				parsed := nativeResult{
					conversationID: envelopeConversationID(envelope),
					status:         stringValue(envelope.Result.Status),
					errorMessage:   stringValue(envelope.Result.Error),
					structured:     envelope.Result.StructuredOutput,
					echoedSchema:   envelope.Result.JsonSchema,
				}
				if parsed.status == "SUCCESS" && request.sessionID != "" && parsed.conversationID != request.sessionID {
					interruptProcess(active)
					protocolErr = terminal("agy result returned a different conversation ID")
				}
				result = &parsed
			}
		case waitErr := <-waited:
			waited = nil
			processErr = waitErr
		case <-timer:
			interruptProcess(active)
			timer = nil
		}
	}
	if protocolErr != nil {
		return nativeResult{}, active, protocolErr
	}
	if active.interrupted.Load() {
		return nativeResult{}, active, interrupted("agy turn was interrupted")
	}
	if result == nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" && processErr != nil {
			message = processErr.Error()
		}
		if message == "" {
			message = "agy stream ended without a result"
		}
		return nativeResult{}, active, categorize(errors.New(message), false)
	}
	if result.status != "SUCCESS" {
		message := result.errorMessage
		if message == "" {
			message = fmt.Sprintf("agy turn ended with status %q", result.status)
		}
		if result.status == "CANCELED" || result.status == "INTERRUPTED" {
			return nativeResult{}, active, interrupted(message)
		}
		return nativeResult{}, active, categorize(errors.New(message), false)
	}
	if request.requireResultID && (!initSeen || result.conversationID == "") {
		return nativeResult{}, active, terminal("agy stream omitted the required conversation ID")
	}
	if request.requireResultID && initID != result.conversationID {
		return nativeResult{}, active, terminal("agy init and result returned different conversation IDs")
	}
	return *result, active, nil
}

func scanStream(reader io.Reader, output chan<- schema.Envelope) {
	defer close(output)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var envelope schema.Envelope
		if json.Unmarshal(scanner.Bytes(), &envelope) == nil && envelope.Event != "" {
			output <- envelope
		}
	}
}

func interruptProcess(active *activeTurn) {
	if active == nil || active.command == nil || active.command.Process == nil {
		return
	}
	active.interrupted.Store(true)
	_ = active.command.Process.Signal(os.Interrupt)
	go func() {
		select {
		case <-active.done:
		case <-time.After(controlTimeout):
			_ = active.command.Process.Kill()
		}
	}()
}

func (a *Adapter) state(sessionID string) (*sessionState, *harness.Error) {
	state, _, err := a.stateWithExistence(sessionID)
	return state, err
}

func (a *Adapter) stateWithExistence(sessionID string) (*sessionState, bool, *harness.Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, false, terminal("agy adapter is closed")
	}
	state, existed := a.states[sessionID]
	if !existed {
		state = &sessionState{}
		a.states[sessionID] = state
	}
	return state, existed, nil
}

func (a *Adapter) lookup(sessionID string) *sessionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.states[sessionID]
}

func (a *Adapter) removeState(sessionID string, state *sessionState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.states[sessionID] == state {
		delete(a.states, sessionID)
	}
}

func bindWorkdir(state *sessionState, workdir string) *harness.Error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.workdir != "" && state.workdir != workdir {
		return terminal("session working directory cannot change")
	}
	if state.workdir == "" {
		state.workdir = workdir
	}
	return nil
}

func writeSchema(raw json.RawMessage) (string, error) {
	file, err := os.CreateTemp("", "tractor-agy-schema-*.json")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(name)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(name)
		return "", closeErr
	}
	return name, nil
}

func validateStructured(validator *harness.ResultValidator, value any) (harness.Result, *harness.Error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, terminal(fmt.Sprintf("encode agy structured output: %v", err))
	}
	return validator.Validate(raw)
}

func compareEchoedSchema(requested json.RawMessage, echoed any) *harness.Error {
	if echoed == nil {
		return terminal("agy result omitted json_schema")
	}
	var expected any
	if err := json.Unmarshal(requested, &expected); err != nil {
		return terminal(fmt.Sprintf("decode requested output schema: %v", err))
	}
	expectedRaw, err := json.Marshal(expected)
	if err != nil {
		return terminal(fmt.Sprintf("normalize requested output schema: %v", err))
	}
	echoedRaw, err := json.Marshal(echoed)
	if err != nil {
		return terminal(fmt.Sprintf("normalize agy output schema: %v", err))
	}
	if !bytes.Equal(expectedRaw, echoedRaw) {
		return terminal("agy result echoed a different JSON schema")
	}
	return nil
}

func envelopeConversationID(envelope schema.Envelope) string {
	if envelope.ConversationID != nil {
		return *envelope.ConversationID
	}
	if envelope.Init != nil && envelope.Init.ConversationID != nil {
		return *envelope.Init.ConversationID
	}
	if envelope.Result != nil && envelope.Result.ConversationID != nil {
		return *envelope.Result.ConversationID
	}
	return ""
}

func remaining(deadline time.Time, original time.Duration) time.Duration {
	if deadline.IsZero() {
		return original
	}
	return time.Until(deadline)
}

func joinParts(parts []harness.ContentPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n\n")
}

func modelIncludesEffort(model string) bool {
	for _, suffix := range []string{"-low", "-medium", "-high"} {
		if strings.HasSuffix(model, suffix) {
			return true
		}
	}
	return false
}

func categorize(err error, wasInterrupted bool) *harness.Error {
	if err == nil {
		return nil
	}
	if wasInterrupted || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return interrupted(err.Error())
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "agy operation failed"
	}
	lower := strings.ToLower(message)
	for _, marker := range []string{"rate limit", "quota", "resource exhausted", "overloaded", "unavailable", "temporarily", "connection reset", "broken pipe", "unexpected eof", "timeout waiting for response", "internal (code 500)", "can't connect"} {
		if strings.Contains(lower, marker) {
			return retryable(message)
		}
	}
	for _, marker := range []string{"unknown model", "invalid", "not found", "permission", "unauthorized", "unauthenticated", "schema", "different conversation id"} {
		if strings.Contains(lower, marker) {
			return terminal(message)
		}
	}
	return retryable(message)
}

// artifactPathErrorMarker matches agy's own error text when the model's
// file-write tool call is routed through agy's native artifact tool, which
// only accepts paths inside its private per-conversation "brain" directory
// and rejects any path inside the workspace agy was given via --add-dir.
// Tractor cannot suppress or reroute that native tool from the CLI (agy
// exposes no per-tool allow/deny flag), so a single corrective turn is the
// only lever available to recover a branch that hits it.
const artifactPathErrorMarker = "is not a valid artifact path"

// isArtifactPathError reports whether err is agy's "declaring permissions"
// failure for a file-write tool call targeting a path outside its brain
// directory. This is agy CLI's own error text (see errorMessage propagated
// from a non-SUCCESS result envelope through categorize), not a message
// Tractor constructs.
func isArtifactPathError(err *harness.Error) bool {
	return err != nil && strings.Contains(err.Message, artifactPathErrorMarker)
}

// artifactWriteRepairPrompt asks the model to redo the turn using its
// shell/terminal tool instead of its native file-write tool, mirroring the
// bounded, single-shot repair already used for schema-invalid output above.
func artifactWriteRepairPrompt(message string) string {
	return fmt.Sprintf(
		"Your previous turn failed: %s. Your file-write/edit tool only accepts paths inside its own private artifact directory and cannot write into this workspace. Do not call that tool again in this task. Create and edit every file in the workspace exclusively with your shell/terminal tool (for example a heredoc: `cat > <path> <<'EOF' ... EOF`). Retry the original task now, writing all files that way.",
		message,
	)
}

func terminal(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorTerminal, Message: message}
}

func retryable(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorRetryable, Message: message}
}

func interrupted(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorInterrupted, Message: message}
}

var _ harness.HarnessAdapter = (*Adapter)(nil)
