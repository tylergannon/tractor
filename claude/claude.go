// Package claude is Gimble's HarnessAdapter for Claude Code, through the
// Claude Agent SDK. Each turn launches Claude Code against the session id;
// the conversation is Claude Code's to keep.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	claudeagent "github.com/roasbeef/claude-agent-sdk-go"
	"github.com/tylergannon/gimble"
)

const controlTimeout = 5 * time.Second

// adapter runs Claude Code sessions.
type adapter struct {
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	model   string
	workdir string

	mu     sync.Mutex
	fresh  bool   // no native conversation yet
	parent string // for a fork, the session to fork from on the first turn
	active *activeTurn
}

type activeTurn struct {
	stream *claudeagent.Stream
	emit   *projector
}

// New returns Gimble's Claude Code harness. It launches Claude Code when a
// session first needs it.
func New() gimble.HarnessAdapter {
	return &adapter{sessions: make(map[string]*session)}
}

// CreateSession mints the id the first turn passes as --session-id.
func (a *adapter) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	return a.add(&session{model: model, workdir: workdir, fresh: true})
}

// Fork mints an id for a new session that forks from sessionID on its
// first turn, with --resume and --fork-session.
func (a *adapter) Fork(ctx context.Context, sessionID string) (string, error) {
	parent, err := a.session(sessionID)
	if err != nil {
		return "", err
	}
	return a.add(&session{model: parent.model, workdir: parent.workdir, parent: sessionID})
}

func (a *adapter) add(s *session) (string, error) {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return "", fmt.Errorf("claude: mint a session id: %w", err)
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	return id, nil
}

// RunTurn runs one turn and blocks until it ends.
func (a *adapter) RunTurn(ctx context.Context, sessionID, prompt string, schema json.RawMessage, onEvent func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	s, err := a.session(sessionID)
	if err != nil {
		return gimble.TurnResult{}, err
	}
	s.mu.Lock()
	fresh, parent := s.fresh, s.parent
	s.mu.Unlock()

	nativeErrors := make(chan error, 1)
	project := newProjector(sessionID, s.model, onEvent)
	options := []claudeagent.Option{
		claudeagent.WithCwd(s.workdir),
		claudeagent.WithModel(s.model),
		claudeagent.WithIncludePartialMessages(true),
		claudeagent.WithPermissionMode(claudeagent.PermissionModeBypassAll),
		claudeagent.WithAllowDangerouslySkipPermissions(true),
		claudeagent.WithRawMessageObserver(func(raw json.RawMessage) error {
			err := project.raw(raw)
			if err != nil {
				select {
				case nativeErrors <- err:
				default:
				}
			}
			return err
		}),
		claudeagent.WithStderr(func(data string) {
			if err := fatalStderr(data); err != nil {
				select {
				case nativeErrors <- err:
				default:
				}
			}
		}),
	}
	extra := map[string]*string{}
	if len(schema) > 0 {
		text := string(schema)
		extra["json-schema"] = &text
	}
	switch {
	case parent != "":
		options = append(options, claudeagent.WithForkSession(parent))
		extra["session-id"] = &sessionID
	case fresh:
		extra["session-id"] = &sessionID
	default:
		options = append(options, claudeagent.WithResume(sessionID))
	}
	options = append(options, claudeagent.WithExtraArgs(extra))

	// The process is not tied to ctx: on cancellation the turn is
	// interrupted first so Claude Code can wind down, then closed.
	processCtx, stop := context.WithCancel(context.Background())
	defer stop()
	client, err := claudeagent.NewClient(options...)
	if err != nil {
		return gimble.TurnResult{}, fmt.Errorf("claude: %w", err)
	}
	defer func() { _ = client.Close() }()
	stream, err := client.Stream(processCtx)
	if err != nil {
		return gimble.TurnResult{}, fmt.Errorf("claude: %w", err)
	}
	defer func() { _ = stream.Close() }()

	active := &activeTurn{stream: stream, emit: project}
	s.setActive(active)
	defer s.setActive(nil)
	if err := stream.Send(processCtx, prompt); err != nil {
		return gimble.TurnResult{}, fmt.Errorf("claude: %w", err)
	}

	result, err := waitTurn(ctx, stream, nativeErrors, sessionID, s)
	if ctx.Err() != nil {
		return gimble.TurnResult{}, ctx.Err()
	}
	if err != nil {
		return gimble.TurnResult{}, err
	}
	usage := project.turnUsage()
	if len(schema) == 0 {
		out, err := json.Marshal(result.Result)
		return gimble.TurnResult{Output: out, Usage: usage}, err
	}
	if result.StructuredOutput == nil {
		return gimble.TurnResult{}, errors.New("claude: the turn ended without structured output")
	}
	out, err := json.Marshal(result.StructuredOutput)
	return gimble.TurnResult{Output: out, Usage: usage}, err
}

// Steer sends message into the session's running turn, if there is one.
func (a *adapter) Steer(ctx context.Context, sessionID, message string) error {
	s, err := a.session(sessionID)
	if err != nil {
		return err
	}
	active := s.getActive()
	if active == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	if err := active.stream.Send(ctx, message); err != nil {
		if s.getActive() != active {
			return nil // the turn ended first: the message is dropped
		}
		return fmt.Errorf("claude: %w", err)
	}
	return nil
}

func (a *adapter) Interrupt(ctx context.Context, sessionID string) error {
	s, err := a.session(sessionID)
	if err != nil {
		return err
	}
	active := s.getActive()
	if active == nil {
		return nil
	}
	_, err = active.stream.InterruptWithReceipt(ctx)
	return err
}

// Close forgets sessionID. Claude Code's own process is already gone by
// the time Close runs: RunTurn's process is scoped to one turn, not the
// session. Idempotent: an unknown id returns nil.
func (a *adapter) Close(ctx context.Context, sessionID string) error {
	a.mu.Lock()
	delete(a.sessions, sessionID)
	a.mu.Unlock()
	return nil
}

func (a *adapter) session(id string) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[id]; s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("claude: no session %q", id)
}

func (s *session) setActive(active *activeTurn) {
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
}

func (s *session) getActive() *activeTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// waitTurn reads the turn to its result. When ctx ends first it interrupts
// the turn and waits briefly for Claude Code to wind down.
func waitTurn(ctx context.Context, stream *claudeagent.Stream, nativeErrors <-chan error, sessionID string, s *session) (claudeagent.ResultMessage, error) {
	messages := make(chan claudeagent.Message)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer close(messages)
		for message := range stream.Messages() {
			select {
			case messages <- message:
			case <-stop:
				return
			}
		}
	}()

	done := ctx.Done()
	var grace <-chan time.Time
	for {
		select {
		case err := <-nativeErrors:
			if err != nil {
				return claudeagent.ResultMessage{}, err
			}
		case <-done:
			done = nil
			interruptCtx, cancel := context.WithTimeout(context.Background(), controlTimeout)
			_, _ = stream.InterruptWithReceipt(interruptCtx)
			cancel()
			grace = time.After(controlTimeout)
		case <-grace:
			return claudeagent.ResultMessage{}, ctx.Err()
		case message, ok := <-messages:
			if !ok {
				return claudeagent.ResultMessage{}, errors.New("claude: the stream ended without a result")
			}
			envelope := decodeEnvelope(message)
			if envelope.SessionID != "" && envelope.SessionID != sessionID {
				return claudeagent.ResultMessage{}, fmt.Errorf("claude: got session %s, want %s", envelope.SessionID, sessionID)
			}
			if envelope.SessionID != "" && !(envelope.Type == "system" && envelope.Subtype == "init") {
				s.mu.Lock()
				s.fresh, s.parent = false, "" // the native conversation exists now
				s.mu.Unlock()
			}
			if err := assistantError(message); err != nil {
				return claudeagent.ResultMessage{}, err
			}
			if result, ok := asResult(message); ok {
				if done == nil {
					return claudeagent.ResultMessage{}, ctx.Err()
				}
				if result.Subtype != "success" && result.Status != "success" {
					return claudeagent.ResultMessage{}, errors.New(resultFailure(result))
				}
				return result, nil
			}
		}
	}
}

type envelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
}

func decodeEnvelope(message claudeagent.Message) envelope {
	var e envelope
	if raw, err := json.Marshal(message); err == nil {
		_ = json.Unmarshal(raw, &e)
	}
	return e
}

func assistantError(message claudeagent.Message) error {
	var code claudeagent.AssistantMessageError
	switch assistant := message.(type) {
	case claudeagent.AssistantMessage:
		code = assistant.Error
	case *claudeagent.AssistantMessage:
		code = assistant.Error
	}
	if code == "" {
		return nil
	}
	return errors.New("claude: assistant error: " + string(code))
}

func asResult(message claudeagent.Message) (claudeagent.ResultMessage, bool) {
	switch result := message.(type) {
	case claudeagent.ResultMessage:
		return result, true
	case *claudeagent.ResultMessage:
		return *result, true
	}
	return claudeagent.ResultMessage{}, false
}

func resultFailure(result claudeagent.ResultMessage) string {
	parts := append([]string(nil), result.Errors...)
	if result.TerminalReason != nil {
		parts = append(parts, string(*result.TerminalReason))
	}
	if len(parts) == 0 {
		parts = append(parts, result.Subtype)
	}
	return "claude: the turn failed: " + strings.Join(parts, "; ")
}

func fatalStderr(data string) error {
	lower := strings.ToLower(data)
	for _, marker := range []string{"no conversation found", "invalid model", "unknown model"} {
		if strings.Contains(lower, marker) {
			return errors.New("claude: " + strings.TrimSpace(data))
		}
	}
	return nil
}

var _ gimble.HarnessAdapter = (*adapter)(nil)
