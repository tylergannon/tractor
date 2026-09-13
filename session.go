package gimble

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Session is one agent conversation on one harness, in one workdir. It
// belongs to the scope that created it and is closed when that scope ends.
type Session struct {
	adapter HarnessAdapter
	name    string
	model   string
	workdir string
	id      string // the creating scope's key, then name.ordinal, as in lap.3/coder.1

	mu      sync.Mutex
	native  string // the harness's session id, made on the first turn
	turns   int
	turnID  string
	running bool
	closed  bool

	usage            Usage // the session's running total across its turns
	canonicalSession string
	eventSeq         uint64
	messageIDs       map[string]string
	activeEmit       func(AgentEvent) error
}

// NewSession creates a session in the scope the ctx is in, named for the
// graph. It cannot fail: the agent process starts on the first turn, and
// the adapter carries the harness-specific config. A session created outside
// Run cannot generate turns or be forked.
func NewSession(ctx context.Context, name string, adapter HarnessAdapter, model, workdir string) *Session {
	s := &Session{adapter: adapter, name: name, model: model, workdir: workdir}
	if scope, err := current(ctx); err == nil {
		scope.adopt(s)
		scope.run.event(scope.key, s.id, "", SessionCreated{Name: name, Adapter: fmt.Sprintf("%T", adapter), Model: model, Workdir: workdir})
	}
	return s
}

// Text is the output of a prose turn: no schema is sent, and the result is
// the agent's final message.
type Text string

// Schema is empty: a prose turn sends no schema.
func (Text) Schema() json.RawMessage { return nil }

// ValidateJSON accepts the final message, a JSON string.
func (Text) ValidateJSON(raw []byte) error {
	var text string
	return json.Unmarshal(raw, &text)
}

// Generate runs one turn and blocks until it ends. T's schema is sent with
// the prompt, and the result is validated once, here, and decoded into T.
// A failed validation is an error. For Text no schema is sent and the
// result is the final message. The options attach supervisors.
func (s *Session) Generate[T Output](ctx context.Context, prompt string, opts ...AgentOption) (T, error) {
	if o := apply(opts); len(o.supervisors) > 0 {
		return supervise[T](ctx, s, prompt, o.supervisors)
	}
	var out T
	return generate[T](ctx, s, prompt, nil, fmt.Sprintf("%T", out))
}

func generate[T Output](ctx context.Context, s *Session, prompt string, onEvent func(AgentEvent) error, outputType string) (T, error) {
	var out T
	raw, err := s.turn(ctx, prompt, out.Schema(), onEvent, outputType, out.ValidateJSON)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("gimble: %s: decode the result: %w", s.id, err)
	}
	return out, nil
}

// turn runs one agent turn and records its outcome. validate, if any, is the
// typed output's check: the turn is recorded as failed when the result does
// not validate, because that is what the turn produced.
func (s *Session) turn(ctx context.Context, prompt string, schema json.RawMessage, onEvent func(AgentEvent) error, outputType string, validate func([]byte) error) (json.RawMessage, error) {
	s.mu.Lock()
	if err := s.usable(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.running = true
	s.turns++
	turnID := fmt.Sprintf("%s/turn.%d", s.id, s.turns)
	s.turnID = turnID
	native := s.native
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.turnID = ""
		s.activeEmit = nil
		s.mu.Unlock()
	}()

	if native == "" {
		id, err := s.adapter.CreateSession(ctx, s.model, s.workdir)
		if err != nil {
			return nil, fmt.Errorf("gimble: %s: %w", s.id, err)
		}
		s.mu.Lock()
		s.native, native = id, id
		s.mu.Unlock()
	}
	if onEvent == nil {
		onEvent = func(AgentEvent) error { return nil }
	}
	logf("%s: turn started (%s)", s.id, s.model)
	start := time.Now()
	scope, _ := current(ctx)
	if scope != nil {
		scope.run.event(scope.key, s.id, turnID, TurnStarted{Prompt: prompt, OutputType: outputType})
	}
	var turnUsage Usage
	var wrapped func(AgentEvent) error
	wrapped = func(e AgentEvent) error {
		stamped, err := s.stampAgentEvent(e)
		if err != nil {
			return err
		}
		if scope != nil {
			if err := scope.run.sessionEvent(scope.key, s.id, turnID, stamped); err != nil {
				return err
			}
		}
		if err := onEvent(stamped); err != nil {
			return err
		}
		if step, carried := stepUsage(stamped); carried {
			turnUsage.add(step)
			return s.recordUsage(step, native, turnID, wrapped)
		}
		return nil
	}
	s.mu.Lock()
	s.activeEmit = wrapped
	s.mu.Unlock()
	inboxKey := turnID + "/prompt"
	if err := wrapped(nativeEvent("session.inbox.enqueued", map[string]any{
		"sessionID": native, "inboxID": inboxKey,
		"item": map[string]any{"type": "user", "payload": map[string]any{"text": prompt}, "delivery": "queue"},
	}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID, "messageID": inboxKey})); err != nil {
		return nil, fmt.Errorf("gimble: %s: record prompt: %w", s.id, err)
	}
	if err := wrapped(nativeEvent("session.inbox.delivered", map[string]any{"sessionID": native, "inboxID": inboxKey}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID, "messageID": inboxKey})); err != nil {
		return nil, fmt.Errorf("gimble: %s: deliver prompt: %w", s.id, err)
	}
	if err := wrapped(nativeEvent("session.execution.started", map[string]any{"sessionID": native}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID})); err != nil {
		return nil, fmt.Errorf("gimble: %s: start execution: %w", s.id, err)
	}
	result, err := s.adapter.RunTurn(ctx, native, prompt, schema, wrapped)
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if len(result.Usage) > 0 {
		// The steps already accounted for the turn's tokens. The harness's
		// own report adds only the cost it stated, so nothing is counted
		// twice, and the session republishes its total.
		var stated Usage
		for _, model := range result.Usage {
			stated.Cost += model.Cost
		}
		if usageErr := s.recordUsage(stated, native, turnID, wrapped); usageErr != nil {
			err = errors.Join(err, usageErr)
		}
	}
	// The turn's usage is what the harness reported, and otherwise what its
	// steps spent, under the model the session runs.
	report := modelUsage(map[string]Usage{s.model: turnUsage})
	if result.Usage != nil {
		report = modelUsage(result.Usage)
	}
	logf("%s: turn ended after %s: %v", s.id, time.Since(start).Round(time.Second), orNone(err))
	if ctx.Err() != nil {
		reason := "shutdown"
		if steerSource(ctx) != "" {
			reason = "user"
		}
		terminalErr := wrapped(nativeEvent("session.execution.interrupted", map[string]any{"sessionID": native, "reason": reason}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID}))
		err = errors.Join(ctx.Err(), terminalErr)
		if scope != nil {
			scope.run.event(scope.key, s.id, turnID, TurnEnded{Error: ctx.Err().Error(), Usage: report, Duration: time.Since(start), Interrupted: true})
		}
		return nil, err
	}
	if err != nil {
		terminalErr := wrapped(nativeEvent("session.execution.failed", map[string]any{"sessionID": native, "error": map[string]any{"type": "provider", "message": err.Error()}}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID}))
		err = errors.Join(err, terminalErr)
		if scope != nil {
			scope.run.event(scope.key, s.id, turnID, TurnEnded{Error: err.Error(), Usage: report, Duration: time.Since(start)})
		}
		return nil, fmt.Errorf("gimble: %s: %w", s.id, err)
	}
	if terminalErr := wrapped(nativeEvent("session.execution.succeeded", map[string]any{"sessionID": native}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID})); terminalErr != nil {
		return nil, fmt.Errorf("gimble: %s: complete execution: %w", s.id, terminalErr)
	}
	// The harness succeeded, so its native events stand; only the turn's own
	// recorded outcome carries the validation failure.
	if validate != nil {
		if err := validate(result.Output); err != nil {
			if scope != nil {
				scope.run.event(scope.key, s.id, turnID, TurnEnded{Result: JSONText(result.Output), Error: err.Error(), Usage: report, Duration: time.Since(start)})
			}
			return nil, fmt.Errorf("gimble: %s: the result does not validate: %w", s.id, err)
		}
	}
	if scope != nil {
		scope.run.event(scope.key, s.id, turnID, TurnEnded{Result: JSONText(result.Output), Usage: report, Duration: time.Since(start)})
	}
	return result.Output, nil
}

// stepUsage reads the usage a step event carries. A step that ended always
// accounts for itself; a step that failed does so only when it reached the
// model, which is when its data carries both cost and tokens.
func stepUsage(event AgentEvent) (Usage, bool) {
	ended := event.Type == "session.step.ended"
	if !ended && event.Type != "session.step.failed" {
		return Usage{}, false
	}
	var data struct {
		Cost   *float64 `json:"cost"`
		Tokens *Tokens  `json:"tokens"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return Usage{}, false
	}
	if !ended && (data.Cost == nil || data.Tokens == nil) {
		return Usage{}, false
	}
	var usage Usage
	if data.Cost != nil {
		usage.Cost = *data.Cost
	}
	if data.Tokens != nil {
		usage.Tokens = *data.Tokens
	}
	return usage, true
}

// recordUsage adds one usage to the session's running total and emits that
// total as session.usage.updated, through the same path the harness's own
// events take: the session's total is republished after every step and
// after every harness turn report.
func (s *Session) recordUsage(add Usage, native, turnID string, emit func(AgentEvent) error) error {
	s.mu.Lock()
	s.usage.add(add)
	total := s.usage
	s.mu.Unlock()
	return emit(nativeEvent("session.usage.updated",
		map[string]any{"sessionID": native, "cost": total.Cost, "tokens": total.Tokens},
		map[string]any{"provider": "gimble", "sessionID": native, "turnID": turnID}))
}

func nativeEvent(eventType string, data any, nativeRef any) AgentEvent {
	event := AgentEvent{Type: eventType, Data: mustJSON(data)}
	if nativeRef != nil {
		event.NativeRef = mustJSON(nativeRef)
	}
	return event
}

func (s *Session) stampAgentEvent(event AgentEvent) (AgentEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var data map[string]any
	if err := json.Unmarshal(event.Data, &data); err != nil || data == nil {
		return AgentEvent{}, fmt.Errorf("gimble: %s data is not an object", event.Type)
	}
	if s.canonicalSession == "" {
		s.canonicalSession = "ses_" + base64.RawURLEncoding.EncodeToString([]byte(s.id))
	}
	nativeSession, _ := data["sessionID"].(string)
	if nativeSession == "" {
		return AgentEvent{}, fmt.Errorf("gimble: %s has no native sessionID", event.Type)
	}
	data["sessionID"] = s.canonicalSession
	ref := make(map[string]any)
	if len(event.NativeRef) > 0 {
		if err := json.Unmarshal(event.NativeRef, &ref); err != nil {
			return AgentEvent{}, fmt.Errorf("gimble: %s NativeRef is not a JSON object: %w", event.Type, err)
		}
	}
	if ref == nil {
		return AgentEvent{}, fmt.Errorf("gimble: %s NativeRef is not a JSON object", event.Type)
	}
	allowedRef := map[string]bool{
		"provider": true, "sessionID": true, "turnID": true, "messageID": true,
		"responseID": true, "itemID": true, "parentToolUseID": true,
		"normalizedMessageID": true, "normalizedSessionID": true,
	}
	for key := range ref {
		if !allowedRef[key] {
			return AgentEvent{}, fmt.Errorf("gimble: %s NativeRef has unsupported field %q", event.Type, key)
		}
	}
	if provider, _ := ref["provider"].(string); provider == "" {
		return AgentEvent{}, fmt.Errorf("gimble: %s NativeRef has no provider", event.Type)
	}
	if _, exists := ref["sessionID"]; !exists {
		ref["sessionID"] = nativeSession
	} else if ref["sessionID"] != nativeSession {
		return AgentEvent{}, fmt.Errorf("gimble: %s NativeRef sessionID does not match data", event.Type)
	}
	nextEventSeq := s.eventSeq + 1
	for _, key := range []string{"assistantMessageID", "inboxID"} {
		if nativeID, present := data[key].(string); present {
			if nativeID == "" {
				return AgentEvent{}, fmt.Errorf("gimble: %s has an empty %s", event.Type, key)
			}
			canonical := s.messageIDLocked(nativeID, nextEventSeq)
			data[key] = canonical
			if err := bindMessageRef(event.Type, ref, nativeID, canonical); err != nil {
				return AgentEvent{}, err
			}
		}
	}
	if event.Type == "session.step.started" {
		data["agent"] = s.name
	}
	event.ID = fmt.Sprintf("evt_%s_%020d", base64.RawURLEncoding.EncodeToString([]byte(s.id)), nextEventSeq)
	event.Created = time.Now().UnixMilli()
	event.Data = mustJSON(data)
	event.NativeRef = mustJSON(ref)
	s.eventSeq = nextEventSeq
	return event, nil
}

func (s *Session) messageIDLocked(native string, eventSeq uint64) string {
	if s.messageIDs == nil {
		s.messageIDs = make(map[string]string)
	}
	if id := s.messageIDs[native]; id != "" {
		return id
	}
	id := fmt.Sprintf("msg_%s_%020d", base64.RawURLEncoding.EncodeToString([]byte(s.id)), eventSeq)
	s.messageIDs[native] = id
	return id
}

func bindMessageRef(eventType string, ref map[string]any, native, canonical string) error {
	if _, exists := ref["messageID"]; !exists {
		ref["messageID"] = native
	} else if ref["messageID"] != native {
		return fmt.Errorf("gimble: %s NativeRef messageID does not match data", eventType)
	}
	ref["normalizedMessageID"] = canonical
	return nil
}

// usable reports why s cannot start a turn. The caller holds s.mu.
func (s *Session) usable() error {
	switch {
	case s.id == "":
		return fmt.Errorf("gimble: session %q was not created in a run", s.name)
	case s.closed:
		return fmt.Errorf("gimble: session %s was used after its scope ended", s.id)
	case s.running:
		return fmt.Errorf("gimble: session %s is already running a turn", s.id)
	}
	return nil
}

// Steer injects a message into the turn that is running on this session.
// It is called from another goroutine while Generate blocks. The message
// lands at the worker's next model call. If no turn is running the message
// is dropped, and Steer returns nil either way. The error is for a harness
// that could not be reached.
func (s *Session) Steer(ctx context.Context, message string) error {
	s.mu.Lock()
	native, running, turn, emit := s.native, s.running, s.turnID, s.activeEmit
	s.mu.Unlock()
	if !running || native == "" {
		logf("%s: steer dropped: %s", s.id, oneLine(message))
		if scope, err := current(ctx); err == nil {
			scope.run.event(scope.key, s.id, turn, Steer{Target: s.id, Source: steerSource(ctx), Message: message})
		}
		return nil
	}
	logf("%s: steer: %s", s.id, oneLine(message))
	if scope, err := current(ctx); err == nil {
		scope.run.event(scope.key, s.id, turn, Steer{Target: s.id, Source: steerSource(ctx), Message: message, Landed: true})
	}
	inboxKey := turn + fmt.Sprintf("/steer.%d", time.Now().UnixNano())
	if emit != nil {
		if err := emit(nativeEvent("session.inbox.enqueued", map[string]any{
			"sessionID": native, "inboxID": inboxKey,
			"item": map[string]any{"type": "user", "payload": map[string]any{"text": message}, "delivery": "steer"},
		}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turn, "messageID": inboxKey})); err != nil {
			return err
		}
	}
	err := s.adapter.Steer(ctx, native, message)
	if emit != nil {
		eventType := "session.inbox.delivered"
		if err != nil {
			eventType = "session.inbox.cancelled"
		}
		emitErr := emit(nativeEvent(eventType, map[string]any{"sessionID": native, "inboxID": inboxKey}, map[string]any{"provider": "gimble", "sessionID": native, "turnID": turn, "messageID": inboxKey}))
		return errors.Join(err, emitErr)
	}
	return err
}

func (s *Session) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	native, running, turn := s.native, s.running, s.turnID
	s.mu.Unlock()
	if !running || native == "" {
		return nil
	}
	if scope, err := current(ctx); err == nil {
		scope.run.event(scope.key, s.id, turn, Interrupt{Target: s.id, Source: steerSource(ctx)})
	}
	interruptor, ok := s.adapter.(interface {
		Interrupt(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("gimble: %s: adapter does not support interrupt", s.id)
	}
	return interruptor.Interrupt(ctx, native)
}

type steerSourceKey struct{}

func withSteerSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, steerSourceKey{}, source)
}
func steerSource(ctx context.Context) string {
	source, _ := ctx.Value(steerSourceKey{}).(string)
	return source
}

// Fork returns a new session, named for the graph, in the same workdir,
// with the same conversation so far. The two sessions are independent
// after that.
func (s *Session) Fork(ctx context.Context, name string) (*Session, error) {
	scope, err := current(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	err = s.usable()
	native := s.native
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	fork := &Session{adapter: s.adapter, name: name, model: s.model, workdir: s.workdir}
	if native != "" {
		if fork.native, err = s.adapter.Fork(ctx, native); err != nil {
			return nil, fmt.Errorf("gimble: fork %s: %w", s.id, err)
		}
	}
	scope.adopt(fork)
	scope.run.event(scope.key, fork.id, "", SessionCreated{Name: name, Adapter: fmt.Sprintf("%T", fork.adapter), Model: fork.model, Workdir: fork.workdir, Parent: s.id})
	logf("%s: forked from %s", fork.id, s.id)
	return fork, nil
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 160 {
		return text[:160] + "..."
	}
	return text
}
