// Package agy is Gimble's HarnessAdapter for Google Antigravity through the
// agy print-mode CLI.
package agy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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

	"github.com/tylergannon/gimble"
)

const (
	controlTimeout = 5 * time.Second
	printBackstop  = 24 * time.Hour
)

var errInterrupted = errors.New("agy: turn was interrupted")

type config struct {
	binary   string
	baseArgs []string
	env      []string
}

type adapter struct {
	mu       sync.Mutex
	sessions map[string]*session
	config   config
}

type session struct {
	model          string
	workdir        string
	ops            sync.Mutex
	mu             sync.Mutex
	conversationID string
	active         *activeTurn
	pending        []string
}

type activeTurn struct {
	command     *exec.Cmd
	done        chan struct{}
	interrupted atomic.Bool
	steered     atomic.Bool
}

type runRequest struct {
	prompt, model, workdir, sessionID string
	schema                            json.RawMessage
	newProject                        bool
	projector                         *projector
}

type nativeResult struct {
	conversationID string
	status         string
	errorMessage   string
	response       string
	structured     any
}

// New returns Gimble's Antigravity harness. It launches agy from PATH when a
// session first needs it.
func New() gimble.HarnessAdapter {
	return newAdapter(config{binary: "agy"})
}

func newAdapter(cfg config) *adapter {
	return &adapter{sessions: make(map[string]*session), config: cfg}
}

// CreateSession reserves an adapter session. The first visible user turn
// starts the native Antigravity conversation, so no hidden model call escapes
// Gimble's event and accounting record.
func (a *adapter) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", errors.New("agy: model is blank")
	}
	absolute, err := existingDir(workdir)
	if err != nil {
		return "", err
	}
	id, err := newSessionID()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.sessions[id] = &session{model: model, workdir: absolute}
	a.mu.Unlock()
	return id, nil
}

// RunTurn runs one resumed agy print process and translates its NDJSON stream.
// Antigravity states no cost and no per-model turn report, so TurnResult carries
// no usage: the session accounts for the turn from its step events alone.
func (a *adapter) RunTurn(ctx context.Context, sessionID, prompt string, schema json.RawMessage, onEvent func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	s, err := a.session(sessionID)
	if err != nil {
		return gimble.TurnResult{}, err
	}
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	conversationID := s.conversationID
	s.mu.Unlock()

	request := runRequest{
		prompt: prompt, model: s.model, workdir: s.workdir, sessionID: conversationID,
		newProject: conversationID == "",
		schema:     schema, projector: newProjector(sessionID, s.model, onEvent),
	}
	result, err := a.run(ctx, s, request)
	if ctx.Err() != nil {
		return gimble.TurnResult{}, ctx.Err()
	}
	if err != nil {
		return gimble.TurnResult{}, err
	}
	if len(schema) == 0 {
		out, err := json.Marshal(result.response)
		return gimble.TurnResult{Output: out}, err
	}
	if result.structured == nil {
		return gimble.TurnResult{}, errors.New("agy: the turn ended without structured_output")
	}
	out, err := json.Marshal(result.structured)
	return gimble.TurnResult{Output: out}, err
}

// Steer interrupts the active print process. RunTurn resumes the same native
// conversation with the message before it returns.
func (a *adapter) Steer(ctx context.Context, sessionID, message string) error {
	s, err := a.session(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	active := s.active
	if active != nil {
		s.pending = append(s.pending, message)
		active.steered.Store(true)
	}
	s.mu.Unlock()
	if active != nil {
		interruptProcess(active)
	}
	return nil
}

// Interrupt stops the active agy process. It is the optional interrupt method
// discovered by gimble.Session.
func (a *adapter) Interrupt(ctx context.Context, sessionID string) error {
	s, err := a.session(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		interruptProcess(active)
	}
	return nil
}

// Fork reports the native limitation instead of aliasing two Gimble sessions
// to one mutable Antigravity conversation. agy does not expose /fork in print
// mode or provide another headless fork transport.
func (a *adapter) Fork(ctx context.Context, sessionID string) (string, error) {
	if _, err := a.session(sessionID); err != nil {
		return "", err
	}
	return "", errors.New("agy: fork is unavailable in print mode")
}

// Close forgets sessionID. Idempotent: an unknown id returns nil.
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
	return nil, fmt.Errorf("agy: no session %q", id)
}

func (a *adapter) run(ctx context.Context, s *session, request runRequest) (nativeResult, error) {
	for {
		result, active, err := a.runOnce(ctx, s, request)
		if request.newProject && result.conversationID != "" {
			request.sessionID = result.conversationID
			request.newProject = false
			s.mu.Lock()
			s.conversationID = result.conversationID
			s.mu.Unlock()
			request.projector.setConversation(result.conversationID)
		}
		message := s.takeSteer()
		if message == "" {
			return result, err
		}
		if err != nil && !(active != nil && active.steered.Load() && errors.Is(err, errInterrupted)) {
			return nativeResult{}, err
		}
		request.prompt = message
	}
}

func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("agy: mint session id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func (s *session) takeSteer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return ""
	}
	message := s.pending[0]
	s.pending = s.pending[1:]
	return message
}

func (a *adapter) runOnce(ctx context.Context, s *session, request runRequest) (nativeResult, *activeTurn, error) {
	args := append([]string(nil), a.config.baseArgs...)
	args = append(args, "-p", request.prompt, "--output-format", "stream-json", "--dangerously-skip-permissions", "--add-dir", request.workdir)
	if request.newProject {
		args = append(args, "--new-project")
	} else {
		args = append(args, "--conversation", request.sessionID)
	}
	if request.model != "" {
		args = append(args, "--model", request.model)
	}
	if len(request.schema) > 0 {
		args = append(args, "--json-schema", string(request.schema))
	}
	backstop := printBackstop
	if deadline, ok := ctx.Deadline(); ok {
		backstop = max(time.Until(deadline)+30*time.Second, time.Second)
	}
	args = append(args, "--print-timeout", backstop.String())

	command := exec.Command(a.config.binary, args...)
	command.Dir = request.workdir
	if a.config.env != nil {
		command.Env = a.config.env
	} else {
		command.Env = os.Environ()
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nativeResult{}, nil, fmt.Errorf("agy: stdout: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nativeResult{}, nil, fmt.Errorf("agy: start: %w", err)
	}
	active := &activeTurn{command: command, done: make(chan struct{})}
	if s != nil {
		s.mu.Lock()
		s.active = active
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			if s.active == active {
				s.active = nil
			}
			s.mu.Unlock()
		}()
	}

	stream := make(chan streamItem)
	go scanStream(stdout, stream)

	var result *nativeResult
	var initID string
	var protocolErr, processErr error
	ctxDone := ctx.Done()
	for stream != nil {
		select {
		case item, ok := <-stream:
			if !ok {
				stream = nil
				continue
			}
			if item.err != nil {
				if protocolErr == nil {
					protocolErr = item.err
					interruptProcess(active)
				}
				continue
			}
			envelope := item.envelope
			switch envelope.Event {
			case "init":
				initID = envelope.conversationID()
				if request.projector != nil {
					request.projector.setConversation(initID)
				}
				if request.sessionID != "" && initID != request.sessionID && protocolErr == nil {
					protocolErr = fmt.Errorf("agy: init returned conversation %q, want %q", initID, request.sessionID)
					interruptProcess(active)
				}
			case "result":
				if envelope.Result != nil {
					result = &nativeResult{
						conversationID: envelope.conversationID(), status: envelope.Result.Status,
						errorMessage: envelope.Result.Error, response: envelope.Result.Response,
						structured: envelope.Result.StructuredOutput,
					}
				}
			}
			if request.projector != nil && protocolErr == nil {
				if err := request.projector.envelope(envelope); err != nil {
					protocolErr = err
					interruptProcess(active)
				}
			}
		case <-ctxDone:
			ctxDone = nil
			interruptProcess(active)
		}
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case processErr = <-waited:
	case <-ctx.Done():
		interruptProcess(active)
		processErr = <-waited
	}
	close(active.done)
	if ctx.Err() != nil {
		return nativeResult{}, active, ctx.Err()
	}
	if protocolErr != nil {
		return nativeResult{}, active, protocolErr
	}
	if active.interrupted.Load() {
		return nativeResult{conversationID: initID}, active, errInterrupted
	}
	if result == nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" && processErr != nil {
			message = processErr.Error()
		}
		if message == "" {
			message = "agy: stream ended without a result"
		}
		return nativeResult{}, active, errors.New(message)
	}
	if processErr != nil {
		return nativeResult{}, active, fmt.Errorf("agy: process: %w: %s", processErr, strings.TrimSpace(stderr.String()))
	}
	if result.status != "SUCCESS" {
		message := result.errorMessage
		if message == "" {
			message = fmt.Sprintf("turn ended with status %q", result.status)
		}
		return nativeResult{}, active, errors.New("agy: " + message)
	}
	if initID == "" || result.conversationID == "" {
		return nativeResult{}, active, errors.New("agy: stream omitted the required conversation_id")
	}
	if initID != result.conversationID {
		return nativeResult{}, active, errors.New("agy: init and result returned different conversation IDs")
	}
	if request.sessionID != "" && result.conversationID != request.sessionID {
		return nativeResult{}, active, fmt.Errorf("agy: result returned conversation %q, want %q", result.conversationID, request.sessionID)
	}
	return *result, active, nil
}

type streamItem struct {
	envelope envelope
	err      error
}

func scanStream(reader io.Reader, output chan<- streamItem) {
	defer close(output)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var value envelope
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			output <- streamItem{err: fmt.Errorf("agy: decode stream event: %w", err)}
			return
		}
		if value.Event == "" {
			output <- streamItem{err: errors.New("agy: stream event omitted event type")}
			return
		}
		output <- streamItem{envelope: value}
	}
	if err := scanner.Err(); err != nil {
		output <- streamItem{err: fmt.Errorf("agy: scan stream: %w", err)}
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

func existingDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("agy: workdir is blank")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("agy: workdir: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("agy: workdir %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("agy: workdir %s is not a directory", absolute)
	}
	return absolute, nil
}

var _ gimble.HarnessAdapter = (*adapter)(nil)
