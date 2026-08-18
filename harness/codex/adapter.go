// Package codex implements Tractor's HarnessAdapter using Codex app-server.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/codex/schema"
)

const (
	requestTimeout = 30 * time.Second
	controlTimeout = 5 * time.Second
)

// Adapter translates the neutral harness contract to Codex app-server's v2
// stdio protocol.
type Adapter struct {
	config processConfig
	mu     sync.Mutex
	closed bool
	states map[string]*sessionState
}

type sessionState struct {
	opMu    sync.Mutex
	mu      sync.Mutex
	workdir string
	fresh   *connection
	active  *activeTurn
}

type activeTurn struct {
	connection  *connection
	turnID      string
	projector   *eventProjector
	interrupted atomic.Bool
	controls    sync.WaitGroup
}

// New constructs an adapter that launches `codex app-server --stdio`.
func New() *Adapter {
	return newAdapter(defaultProcessConfig())
}

func newAdapter(config processConfig) *Adapter {
	return &Adapter{config: config, states: make(map[string]*sessionState)}
}

// SetStderr forwards app-server stderr while retaining it for diagnostics.
// It must be called before the adapter is used.
func (a *Adapter) SetStderr(stderr io.Writer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.stderr = stderr
}

// CreateSession starts a durable Codex thread. Its app-server process is kept
// through the first turn because a thread with no turn is not yet resumable.
func (a *Adapter) CreateSession(model, workdir string) (string, *harness.Error) {
	if err := harness.ValidateCreateSessionInput(model, workdir); err != nil {
		return "", err
	}
	connection, err := a.start()
	if err != nil {
		return "", categorize(err, false)
	}
	keep := false
	defer func() {
		if !keep {
			connection.close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := connection.initialize(ctx); err != nil {
		return "", categorize(err, false)
	}
	result, err := connection.call(ctx, "thread/start", map[string]any{
		"model":              model,
		"cwd":                workdir,
		"approvalPolicy":     "never",
		"sandbox":            "danger-full-access",
		"serviceName":        "tractor",
		"sessionStartSource": "startup",
	})
	if err != nil {
		return "", categorize(err, false)
	}
	threadID, err := decodeThreadID(result)
	if err != nil {
		return "", categorize(err, false)
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return "", terminal("Codex adapter is closed")
	}
	a.states[threadID] = &sessionState{workdir: workdir, fresh: connection}
	a.mu.Unlock()
	keep = true
	return threadID, nil
}

// RunTurn resumes or uses the retained fresh process, runs one structured
// turn, emits completed logical events, and validates the exact result schema.
func (a *Adapter) RunTurn(input harness.RunTurnInput, onEvent harness.OnEvent) (harness.Result, *harness.Error) {
	if err := harness.ValidateRunTurnInput(input, onEvent); err != nil {
		return nil, err
	}
	validator, validationErr := harness.NewResultValidator(input.OutputSchema)
	if validationErr != nil {
		return nil, validationErr
	}
	state, stateErr := a.state(input.SessionID)
	if stateErr != nil {
		return nil, stateErr
	}

	state.opMu.Lock()
	defer state.opMu.Unlock()
	connection, fresh, openErr := a.openSession(state, input.SessionID, input.Workdir)
	if openErr != nil {
		return nil, openErr
	}
	defer func() {
		state.mu.Lock()
		if state.fresh == connection {
			state.fresh = nil
		}
		state.mu.Unlock()
		connection.close()
	}()

	params, optionalProperties, err := turnStartParams(input)
	if err != nil {
		return nil, terminal(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	result, err := connection.call(ctx, "turn/start", params)
	cancel()
	if err != nil {
		return nil, categorize(err, false)
	}
	turnID, err := decodeTurnID(result)
	if err != nil {
		return nil, terminal(err.Error())
	}
	projector := newEventProjector(onEvent)
	active := &activeTurn{connection: connection, turnID: turnID, projector: projector}
	state.mu.Lock()
	state.active = active
	if fresh && state.fresh == connection {
		state.fresh = nil
	}
	if state.workdir == "" {
		state.workdir = input.Workdir
	}
	state.mu.Unlock()
	projector.user(input.Parts)
	defer finishActive(state, active)

	raw, runErr := waitTurn(connection, input, turnID, active)
	if runErr != nil {
		return nil, runErr
	}
	raw, err = omitOptionalNulls(raw, optionalProperties)
	if err != nil {
		return nil, terminal(fmt.Sprintf("normalize Codex result: %v", err))
	}
	return validator.Validate(raw)
}

// Steer best-effort delivers ordered text parts to a currently active turn.
func (a *Adapter) Steer(sessionID string, parts []harness.ContentPart) {
	if harness.ValidateContentParts(parts) != nil {
		return
	}
	state := a.lookup(sessionID)
	if state == nil {
		return
	}
	state.mu.Lock()
	active := state.active
	if active != nil {
		active.controls.Add(1)
	}
	state.mu.Unlock()
	if active == nil {
		return
	}
	defer active.controls.Done()

	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	_, err := active.connection.call(ctx, "turn/steer", map[string]any{
		"threadId":       sessionID,
		"expectedTurnId": active.turnID,
		"input":          nativeInput(parts),
	})
	if err != nil {
		return
	}
	state.mu.Lock()
	stillActive := state.active == active
	state.mu.Unlock()
	if stillActive {
		active.projector.user(parts)
	}
}

// Interrupt requests the native turn stop and returns immediately.
func (a *Adapter) Interrupt(sessionID string) {
	state := a.lookup(sessionID)
	if state == nil {
		return
	}
	state.mu.Lock()
	active := state.active
	if active != nil {
		active.controls.Add(1)
	}
	state.mu.Unlock()
	if active == nil {
		return
	}
	active.interrupted.Store(true)
	go func() {
		defer active.controls.Done()
		ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
		defer cancel()
		_, _ = active.connection.call(ctx, "turn/interrupt", map[string]any{
			"threadId": sessionID,
			"turnId":   active.turnID,
		})
	}()
}

// Compact blocks until Codex reports completion of the matching
// contextCompaction item.
func (a *Adapter) Compact(sessionID, workdir string) *harness.Error {
	if err := harness.ValidateSessionInput(sessionID, workdir); err != nil {
		return err
	}
	state, stateErr := a.state(sessionID)
	if stateErr != nil {
		return stateErr
	}
	if !state.opMu.TryLock() {
		return terminal("cannot compact a session with an active operation")
	}
	defer state.opMu.Unlock()
	state.mu.Lock()
	if state.active != nil {
		state.mu.Unlock()
		return terminal("cannot compact an active session")
	}
	state.mu.Unlock()

	connection, fresh, openErr := a.openSession(state, sessionID, workdir)
	if openErr != nil {
		return openErr
	}
	if !fresh {
		defer connection.close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	_, err := connection.call(ctx, "thread/compact/start", map[string]any{"threadId": sessionID})
	cancel()
	if err != nil {
		if fresh && !isRPCError(err) {
			a.dropFresh(state, connection)
			connection.close()
		}
		return categorize(err, false)
	}
	if err := waitCompaction(connection, sessionID); err != nil {
		if fresh && err.Category == harness.ErrorRetryable {
			a.dropFresh(state, connection)
			connection.close()
		}
		return err
	}
	return nil
}

// Close releases retained fresh-session and live app-server processes.
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

	connections := make(map[*connection]struct{})
	for _, state := range states {
		state.mu.Lock()
		if state.fresh != nil {
			connections[state.fresh] = struct{}{}
		}
		if state.active != nil {
			connections[state.active.connection] = struct{}{}
		}
		state.mu.Unlock()
	}
	for connection := range connections {
		connection.close()
	}
}

func (a *Adapter) start() (*connection, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("codex adapter is closed")
	}
	config := a.config
	config.args = append([]string(nil), config.args...)
	config.env = append([]string(nil), config.env...)
	return startConnection(config)
}

func (a *Adapter) state(sessionID string) (*sessionState, *harness.Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, terminal("Codex adapter is closed")
	}
	state := a.states[sessionID]
	if state == nil {
		state = &sessionState{}
		a.states[sessionID] = state
	}
	return state, nil
}

func (a *Adapter) lookup(sessionID string) *sessionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.states[sessionID]
}

func (a *Adapter) openSession(state *sessionState, sessionID, workdir string) (*connection, bool, *harness.Error) {
	state.mu.Lock()
	if state.workdir != "" && state.workdir != workdir {
		state.mu.Unlock()
		return nil, false, terminal("session working directory cannot change")
	}
	connection := state.fresh
	state.mu.Unlock()
	if connection != nil {
		return connection, true, nil
	}

	connection, err := a.start()
	if err != nil {
		return nil, false, categorize(err, false)
	}
	keep := false
	defer func() {
		if !keep {
			connection.close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := connection.initialize(ctx); err != nil {
		return nil, false, categorize(err, false)
	}
	result, err := connection.call(ctx, "thread/resume", map[string]any{
		"threadId":       sessionID,
		"cwd":            workdir,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
	})
	if err != nil {
		return nil, false, categorize(err, false)
	}
	resumedID, err := decodeThreadID(result)
	if err != nil {
		return nil, false, terminal(err.Error())
	}
	if resumedID != sessionID {
		return nil, false, terminal("thread/resume returned a different session ID")
	}
	state.mu.Lock()
	if state.workdir == "" {
		state.workdir = workdir
	}
	state.mu.Unlock()
	keep = true
	return connection, false, nil
}

func (a *Adapter) dropFresh(state *sessionState, connection *connection) {
	state.mu.Lock()
	if state.fresh == connection {
		state.fresh = nil
	}
	state.mu.Unlock()
}

func finishActive(state *sessionState, active *activeTurn) {
	state.mu.Lock()
	if state.active == active {
		state.active = nil
	}
	state.mu.Unlock()
	active.controls.Wait()
}

func turnStartParams(input harness.RunTurnInput) (schema.TurnStartParams, map[string]struct{}, error) {
	outputSchema, optionalProperties, err := codexCompatibleOutputSchema(input.OutputSchema)
	if err != nil {
		return schema.TurnStartParams{}, nil, err
	}
	model := input.Model
	workdir := input.Workdir
	return schema.TurnStartParams{
		ThreadID:       input.SessionID,
		Input:          nativeInput(input.Parts),
		Model:          schema.TurnStartParamsModel(&model),
		Cwd:            schema.TurnStartParamsCwd(&workdir),
		Effort:         schema.ReasoningEffort(input.ReasoningEffort),
		OutputSchema:   outputSchema,
		ApprovalPolicy: "never",
		SandboxPolicy:  map[string]any{"type": "dangerFullAccess"},
	}, optionalProperties, nil
}

func nativeInput(parts []harness.ContentPart) []schema.UserInput {
	result := make([]schema.UserInput, 0, len(parts))
	for _, part := range parts {
		result = append(result, map[string]any{
			"type":          "text",
			"text":          part.Text,
			"text_elements": []any{},
		})
	}
	return result
}

func waitTurn(connection *connection, input harness.RunTurnInput, turnID string, active *activeTurn) ([]byte, *harness.Error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if input.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, input.Timeout)
		defer cancel()
	}
	result, err := readTurn(ctx, connection, input.SessionID, turnID, active)
	if !errors.Is(err, context.DeadlineExceeded) {
		return result, categorizeTurnError(err, active.interrupted.Load())
	}

	active.interrupted.Store(true)
	interruptCtx, interruptCancel := context.WithTimeout(context.Background(), controlTimeout)
	_, _ = connection.call(interruptCtx, "turn/interrupt", map[string]any{
		"threadId": input.SessionID,
		"turnId":   turnID,
	})
	interruptCancel()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), controlTimeout)
	_, _ = readTurn(drainCtx, connection, input.SessionID, turnID, active)
	drainCancel()
	return nil, interrupted("turn timed out and was interrupted")
}

func readTurn(ctx context.Context, connection *connection, threadID, turnID string, active *activeTurn) ([]byte, error) {
	var assistantText string
	for {
		message, err := connection.next(ctx)
		if err != nil {
			return nil, err
		}
		if len(message.ID) > 0 && message.Method != "" {
			return nil, refuseServerRequest(connection, message)
		}
		if !messageMatches(message.Params, threadID, turnID) {
			continue
		}
		switch message.Method {
		case "item/started":
			active.projector.itemStarted(message.Params)
		case "item/completed":
			if text, ok := active.projector.itemCompleted(message.Params); ok {
				assistantText = text
			}
		case "thread/tokenUsage/updated":
			active.projector.usage(message.Params)
		case "turn/completed":
			status, failure, ok := decodeCompletedTurn(message.Params, threadID, turnID)
			if !ok {
				continue
			}
			switch status {
			case "completed":
				if active.interrupted.Load() {
					return nil, context.Canceled
				}
				if strings.TrimSpace(assistantText) == "" {
					return nil, errors.New("codex completed without an assistant result")
				}
				return []byte(assistantText), nil
			case "interrupted":
				return nil, context.Canceled
			case "failed":
				if failure == "" {
					failure = "Codex turn failed"
				}
				return nil, errors.New(failure)
			default:
				return nil, fmt.Errorf("codex turn ended with status %q", status)
			}
		case "error":
			return nil, decodeErrorNotification(message.Params)
		}
	}
}

func waitCompaction(connection *connection, threadID string) *harness.Error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var compactTurnID string
	for {
		message, err := connection.next(ctx)
		if err != nil {
			return categorize(err, false)
		}
		if len(message.ID) > 0 && message.Method != "" {
			return categorize(refuseServerRequest(connection, message), false)
		}
		if !messageMatchesThread(message.Params, threadID) {
			continue
		}
		switch message.Method {
		case "turn/started":
			compactTurnID = notificationTurnID(message.Params)
		case "item/completed":
			item, ok := decodeItem(message.Params)
			if ok && item.Type == "contextCompaction" {
				turnID := notificationTurnID(message.Params)
				if compactTurnID == "" || turnID == compactTurnID {
					return nil
				}
			}
		case "turn/completed":
			status, failure, ok := decodeCompletedTurn(message.Params, threadID, compactTurnID)
			if ok && status != "completed" {
				if failure == "" {
					failure = "Codex compaction failed"
				}
				return categorize(errors.New(failure), false)
			}
		case "error":
			return categorize(decodeErrorNotification(message.Params), false)
		}
	}
}

func refuseServerRequest(connection *connection, message rpcMessage) error {
	result := map[string]any{"decision": "decline"}
	switch message.Method {
	case "execCommandApproval", "applyPatchApproval":
		result["decision"] = "denied"
	case "item/tool/requestUserInput":
		result = map[string]any{"answers": map[string]any{}}
	case "mcpServer/elicitation/request":
		result = map[string]any{"action": "decline"}
	case "item/permissions/requestApproval":
		result = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	}
	if err := connection.respond(message, result, nil); err != nil {
		return err
	}
	return fmt.Errorf("unexpected interactive Codex request %q", message.Method)
}

func decodeThreadID(raw json.RawMessage) (string, error) {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("decode Codex thread response: %w", err)
	}
	if response.Thread.ID == "" {
		return "", errors.New("codex thread response omitted thread.id")
	}
	return response.Thread.ID, nil
}

func decodeTurnID(raw json.RawMessage) (string, error) {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("decode Codex turn response: %w", err)
	}
	if response.Turn.ID == "" {
		return "", errors.New("codex turn response omitted turn.id")
	}
	return response.Turn.ID, nil
}

func decodeCompletedTurn(raw json.RawMessage, threadID, turnID string) (string, string, bool) {
	var notification struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &notification) != nil || notification.ThreadID != threadID || notification.Turn.ID != turnID {
		return "", "", false
	}
	failure := ""
	if notification.Turn.Error != nil {
		failure = notification.Turn.Error.Message
	}
	return notification.Turn.Status, failure, true
}

func decodeErrorNotification(raw json.RawMessage) error {
	var notification struct {
		Message string `json:"message"`
		Error   *struct {
			Message           string `json:"message"`
			AdditionalDetails string `json:"additionalDetails"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &notification) != nil {
		return fmt.Errorf("codex error notification: %s", strings.TrimSpace(string(raw)))
	}
	if notification.Message != "" {
		return errors.New(notification.Message)
	}
	if notification.Error != nil && notification.Error.Message != "" {
		if notification.Error.AdditionalDetails != "" {
			return fmt.Errorf("%s: %s", notification.Error.Message, notification.Error.AdditionalDetails)
		}
		return errors.New(notification.Error.Message)
	}
	return errors.New("codex emitted an empty error notification")
}

func messageMatches(raw json.RawMessage, threadID, turnID string) bool {
	var envelope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ThreadID != threadID {
		return false
	}
	if envelope.TurnID != "" {
		return envelope.TurnID == turnID
	}
	return envelope.Turn.ID == turnID
}

func messageMatchesThread(raw json.RawMessage, threadID string) bool {
	var envelope struct {
		ThreadID string `json:"threadId"`
	}
	return json.Unmarshal(raw, &envelope) == nil && envelope.ThreadID == threadID
}

func notificationTurnID(raw json.RawMessage) string {
	var envelope struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.TurnID != "" {
		return envelope.TurnID
	}
	return envelope.Turn.ID
}

func categorizeTurnError(err error, wasInterrupted bool) *harness.Error {
	if err == nil {
		return nil
	}
	if wasInterrupted || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return interrupted("turn was interrupted")
	}
	return categorize(err, false)
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
		message = "Codex operation failed"
	}
	lower := strings.ToLower(message)
	for _, marker := range []string{"rate limit", "temporarily unavailable", "service unavailable", "overloaded", "connection reset", "broken pipe", "unexpected eof"} {
		if strings.Contains(lower, marker) {
			return &harness.Error{Category: harness.ErrorRetryable, Message: message}
		}
	}
	for _, marker := range []string{"invalid_request", "invalid json schema", "invalid_json_schema", "not found", "unknown thread", "model", "interactive"} {
		if strings.Contains(lower, marker) {
			return terminal(message)
		}
	}
	if isRPCError(err) {
		return terminal(message)
	}
	return &harness.Error{Category: harness.ErrorRetryable, Message: message}
}

func terminal(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorTerminal, Message: message}
}

func interrupted(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorInterrupted, Message: message}
}

var _ harness.HarnessAdapter = (*Adapter)(nil)
