// Package claude implements Tractor's HarnessAdapter using Claude Code.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	claudeagent "github.com/roasbeef/claude-agent-sdk-go"
	"github.com/tylergannon/tractor/harness"
)

const controlTimeout = 5 * time.Second

type nativeConfig struct {
	sessionID       string
	model           string
	reasoningEffort string
	workdir         string
	outputSchema    any
	fresh           bool
	stderr          io.Writer
}

type nativeSession interface {
	Send(context.Context, string) error
	Messages() iter.Seq[claudeagent.Message]
	Errors() <-chan error
	InterruptWithReceipt(context.Context) (*claudeagent.InterruptReceipt, error)
	Close() error
}

type sessionFactory func(context.Context, nativeConfig) (nativeSession, error)

// Adapter translates the neutral harness contract to Claude Code's SDK
// protocol. Native conversation history remains Claude Code's responsibility.
type Adapter struct {
	mu      sync.Mutex
	closed  bool
	stderr  io.Writer
	states  map[string]*sessionState
	factory sessionFactory
}

type sessionState struct {
	opMu    sync.Mutex
	mu      sync.Mutex
	workdir string
	fresh   bool
	active  *activeTurn
}

type activeTurn struct {
	session     nativeSession
	projector   *eventProjector
	interrupted atomic.Bool
	controls    sync.WaitGroup
}

// New constructs an adapter that launches Claude Code through the pinned SDK.
func New() *Adapter {
	return newAdapter(openNativeSession)
}

func newAdapter(factory sessionFactory) *Adapter {
	return &Adapter{states: make(map[string]*sessionState), factory: factory}
}

// SetStderr forwards Claude Code stderr. It must be called before use.
func (a *Adapter) SetStderr(stderr io.Writer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stderr = stderr
}

// CreateSession mints the native ID that the first Claude Code invocation
// receives through --session-id.
func (a *Adapter) CreateSession(model, workdir string) (string, *harness.Error) {
	if err := harness.ValidateCreateSessionInput(model, workdir); err != nil {
		return "", err
	}
	id, err := newUUID()
	if err != nil {
		return "", retryable(fmt.Sprintf("mint Claude session ID: %v", err))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return "", terminal("Claude adapter is closed")
	}
	a.states[id] = &sessionState{workdir: workdir, fresh: true}
	return id, nil
}

// RunTurn runs one native structured-output turn and validates its result
// against the exact schema supplied by the caller.
func (a *Adapter) RunTurn(input harness.RunTurnInput, onEvent harness.OnEvent) (harness.Result, *harness.Error) {
	if err := harness.ValidateRunTurnInput(input, onEvent); err != nil {
		return nil, err
	}
	validator, validationErr := harness.NewResultValidator(input.OutputSchema)
	if validationErr != nil {
		return nil, validationErr
	}
	outputSchema, err := decodeSchema(input.OutputSchema)
	if err != nil {
		return nil, terminal(err.Error())
	}
	state, stateErr := a.state(input.SessionID)
	if stateErr != nil {
		return nil, stateErr
	}

	state.opMu.Lock()
	defer state.opMu.Unlock()
	fresh, workdirErr := prepareState(state, input.Workdir)
	if workdirErr != nil {
		return nil, workdirErr
	}
	sessionCtx := context.Background()
	var cancelSession context.CancelFunc
	var deadline time.Time
	if input.Timeout > 0 {
		deadline = time.Now().Add(input.Timeout)
		sessionCtx, cancelSession = context.WithDeadline(sessionCtx, deadline)
	} else {
		sessionCtx, cancelSession = context.WithCancel(sessionCtx)
	}
	defer cancelSession()
	session, openErr := a.open(sessionCtx, nativeConfig{
		sessionID:       input.SessionID,
		model:           input.Model,
		reasoningEffort: input.ReasoningEffort,
		workdir:         input.Workdir,
		outputSchema:    outputSchema,
		fresh:           fresh,
	})
	if openErr != nil {
		return nil, categorize(openErr, false)
	}
	defer func() { _ = session.Close() }()

	projector := newEventProjector(onEvent)
	active := &activeTurn{session: session, projector: projector}
	state.mu.Lock()
	state.active = active
	state.mu.Unlock()
	defer finishActive(state, active)

	projector.user(input.Parts)
	if err := session.Send(sessionCtx, joinParts(input.Parts)); err != nil {
		return nil, categorize(err, false)
	}
	if !deadline.IsZero() {
		input.Timeout = time.Until(deadline)
		if input.Timeout <= 0 {
			return nil, interrupted("turn timed out and was interrupted")
		}
	}
	result, runErr := waitTurn(session, state, input, active)
	if runErr != nil {
		return nil, runErr
	}
	raw, err := json.Marshal(result.StructuredOutput)
	if err != nil {
		return nil, terminal(fmt.Sprintf("encode Claude structured output: %v", err))
	}
	return validator.Validate(raw)
}

// Steer best-effort sends ordered text parts to an active Claude stream.
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
	if err := active.session.Send(ctx, joinParts(parts)); err != nil {
		return
	}
	state.mu.Lock()
	stillActive := state.active == active
	state.mu.Unlock()
	if stillActive {
		active.projector.user(parts)
	}
}

// Interrupt requests the native stop and returns immediately.
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
		_, _ = active.session.InterruptWithReceipt(ctx)
	}()
}

// Compact sends Claude Code's native /compact command and waits for its
// terminal compact status.
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
	if state.workdir != "" && state.workdir != workdir {
		state.mu.Unlock()
		return terminal("session working directory cannot change")
	}
	state.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	session, err := a.open(ctx, nativeConfig{sessionID: sessionID, workdir: workdir})
	if err != nil {
		return categorize(err, false)
	}
	if err := session.Send(ctx, "/compact"); err != nil {
		return categorize(err, false)
	}
	messages, stop := readMessages(session)
	defer stop()
	for {
		var message claudeagent.Message
		select {
		case <-ctx.Done():
			return retryable("Claude compaction timed out")
		case nativeErr := <-session.Errors():
			return categorize(nativeErr, false)
		case next, ok := <-messages:
			if !ok {
				return retryable("Claude stream ended without a compaction result")
			}
			message = next
		}
		if mismatch := validateMessageSession(message, sessionID); mismatch != nil {
			return mismatch
		}
		status, ok := asStatus(message)
		if ok && status.Status == nil {
			switch status.CompactResult {
			case claudeagent.CompactResultSuccess:
				return nil
			case claudeagent.CompactResultFailed:
				message := strings.TrimSpace(status.CompactError)
				if message == "" {
					message = "Claude compaction failed"
				}
				return categorize(errors.New(message), false)
			}
		}
		if result, ok := asResult(message); ok && result.Subtype != "success" && result.Status != "success" {
			return categorize(errors.New(resultFailure(result)), false)
		}
	}
}

// Close interrupts active streams. Each turn otherwise owns and closes its
// native SDK process.
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
			active.interrupted.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
			_, _ = active.session.InterruptWithReceipt(ctx)
			cancel()
		}
	}
}

func (a *Adapter) open(ctx context.Context, config nativeConfig) (nativeSession, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, errors.New("claude adapter is closed")
	}
	config.stderr = a.stderr
	factory := a.factory
	a.mu.Unlock()
	return factory(ctx, config)
}

func (a *Adapter) state(sessionID string) (*sessionState, *harness.Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, terminal("Claude adapter is closed")
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

func prepareState(state *sessionState, workdir string) (bool, *harness.Error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.workdir != "" && state.workdir != workdir {
		return false, terminal("session working directory cannot change")
	}
	if state.workdir == "" {
		state.workdir = workdir
	}
	return state.fresh, nil
}

func finishActive(state *sessionState, active *activeTurn) {
	state.mu.Lock()
	if state.active == active {
		state.active = nil
	}
	state.mu.Unlock()
	active.controls.Wait()
}

func waitTurn(session nativeSession, state *sessionState, input harness.RunTurnInput, active *activeTurn) (claudeagent.ResultMessage, *harness.Error) {
	messages, stop := readMessages(session)
	defer stop()

	var timer <-chan time.Time
	var timeout *time.Timer
	if input.Timeout > 0 {
		timeout = time.NewTimer(input.Timeout)
		timer = timeout.C
		defer timeout.Stop()
	}
	timedOut := false
	for {
		select {
		case nativeErr := <-session.Errors():
			return claudeagent.ResultMessage{}, categorize(nativeErr, active.interrupted.Load())
		case <-timer:
			if timedOut {
				return claudeagent.ResultMessage{}, interrupted("turn timed out and was interrupted")
			}
			timedOut = true
			active.interrupted.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
			_, _ = session.InterruptWithReceipt(ctx)
			cancel()
			timer = time.After(controlTimeout)
		case message, ok := <-messages:
			if !ok {
				if timedOut || active.interrupted.Load() {
					return claudeagent.ResultMessage{}, interrupted("turn was interrupted")
				}
				return claudeagent.ResultMessage{}, retryable("Claude stream ended without a result")
			}
			if mismatch := validateMessageSession(message, input.SessionID); mismatch != nil {
				return claudeagent.ResultMessage{}, mismatch
			}
			promoteMaterialized(state, message, input.SessionID)
			active.projector.message(message)
			if result, ok := asResult(message); ok {
				if timedOut || active.interrupted.Load() {
					return claudeagent.ResultMessage{}, interrupted("turn was interrupted")
				}
				if result.Subtype != "success" && result.Status != "success" {
					return claudeagent.ResultMessage{}, categorize(errors.New(resultFailure(result)), false)
				}
				if result.StructuredOutput == nil {
					return claudeagent.ResultMessage{}, terminal("Claude completed without structured output")
				}
				return result, nil
			}
		}
	}
}

func readMessages(session nativeSession) (<-chan claudeagent.Message, func()) {
	messages := make(chan claudeagent.Message)
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		defer close(messages)
		for message := range session.Messages() {
			select {
			case messages <- message:
			case <-stop:
				return
			}
		}
	}()
	return messages, func() {
		close(stop)
		_ = session.Close()
		<-done
	}
}

func promoteMaterialized(state *sessionState, message claudeagent.Message, sessionID string) {
	envelope, err := nativeEnvelope(message)
	if err != nil || envelope.SessionID != sessionID || (envelope.Type == "system" && envelope.Subtype == "init") {
		return
	}
	state.mu.Lock()
	state.fresh = false
	state.mu.Unlock()
}

func validateMessageSession(message claudeagent.Message, sessionID string) *harness.Error {
	envelope, err := nativeEnvelope(message)
	if err != nil {
		return terminal(fmt.Sprintf("decode Claude message: %v", err))
	}
	if envelope.SessionID != "" && envelope.SessionID != sessionID {
		return terminal("Claude returned a different session ID")
	}
	if envelope.Type == "system" && envelope.Subtype == "init" && envelope.SessionID == "" {
		return terminal("Claude init omitted its session ID")
	}
	return nil
}

type messageEnvelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
}

func nativeEnvelope(message claudeagent.Message) (messageEnvelope, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return messageEnvelope{}, err
	}
	var envelope messageEnvelope
	err = json.Unmarshal(raw, &envelope)
	return envelope, err
}

func decodeSchema(raw json.RawMessage) (any, error) {
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("decode output schema: %w", err)
	}
	return schema, nil
}

func joinParts(parts []harness.ContentPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n\n")
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	var encoded [32]byte
	hex.Encode(encoded[:], value[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), nil
}

func resultFailure(result claudeagent.ResultMessage) string {
	parts := append([]string(nil), result.Errors...)
	if result.TerminalReason != nil {
		parts = append(parts, string(*result.TerminalReason))
	}
	if len(parts) == 0 {
		parts = append(parts, result.Subtype)
	}
	return "Claude turn failed: " + strings.Join(parts, "; ")
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
		message = "Claude operation failed"
	}
	lower := strings.ToLower(message)
	for _, marker := range []string{"rate limit", "temporarily unavailable", "service unavailable", "overloaded", "connection reset", "broken pipe", "unexpected eof"} {
		if strings.Contains(lower, marker) {
			return retryable(message)
		}
	}
	for _, marker := range []string{"no conversation found", "not found", "invalid", "model", "structured output", "permission"} {
		if strings.Contains(lower, marker) {
			return terminal(message)
		}
	}
	return retryable(message)
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
