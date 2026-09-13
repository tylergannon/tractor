// Package codex is Gimble's HarnessAdapter for Codex, through `codex
// app-server`. The adapter attaches to the one shared app-server daemon
// already running on the machine: it never launches its own app-server
// process, and it never stops or restarts the daemon, because other
// clients (Codex Desktop included) share it. A thread lives in the daemon
// until the session's scope ends, when Close archives it: an archived
// thread is unloaded, and its MCP child processes and file descriptors
// are released from the shared daemon.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tylergannon/gimble"
)

const (
	requestTimeout = 30 * time.Second
	controlTimeout = 5 * time.Second
)

// adapter runs Codex sessions against the machine's shared app-server
// daemon over one shared connection, dialed lazily on first use.
type adapter struct {
	connMu     sync.Mutex
	sharedConn *connection

	mu       sync.Mutex
	sessions map[string]*session

	// onArchive, when set, is called after thread/archive succeeds for a
	// thread. It exists only so a live test can observe which threads Close
	// archived; production adapters never set it.
	onArchive func(threadID string)
}

type session struct {
	model   string
	workdir string

	mu     sync.Mutex
	active *activeTurn
}

type activeTurn struct {
	conn   *connection
	turnID string
	emit   *projector
}

// New returns Gimble's Codex harness. It attaches to the machine's shared
// `codex app-server` daemon on first use, starting it (idempotently) if it
// is not already running.
func New() gimble.HarnessAdapter {
	return &adapter{sessions: make(map[string]*session)}
}

// conn returns the shared connection, dialing it if there is none yet or
// the last one's reader has failed (the daemon restarted). A freshly
// dialed connection resumes every thread this adapter already knows about
// before it is handed to any caller: `turn/start` does not subscribe the
// calling connection to a thread's notifications, only `thread/start`,
// `thread/fork`, and `thread/resume` do, so a redialed connection that
// skipped this would run turns that hang forever in readTurn.
func (a *adapter) conn(ctx context.Context) (*connection, error) {
	return a.connection(ctx, true)
}

// connection is conn with a choice about a stopped daemon: turns start it,
// Close does not.
func (a *adapter) connection(ctx context.Context, startDaemon bool) (*connection, error) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.sharedConn != nil && !a.sharedConn.dead() {
		return a.sharedConn, nil
	}
	conn, err := connect(ctx, startDaemon)
	if err != nil {
		return nil, err
	}
	if err := a.resumeThreads(ctx, conn); err != nil {
		// The connection is initialized and its reader is running; drop
		// it so a failed redial does not leave a subscribed client and a
		// goroutine behind. The daemon itself is untouched.
		_ = conn.ws.CloseNow()
		return nil, err
	}
	a.sharedConn = conn
	return conn, nil
}

// resumeThreads re-subscribes conn to every session this adapter has
// created, so a connection that replaces a dead one keeps receiving turn
// notifications for threads that already exist in the daemon. It is only
// needed on redial: thread/start, thread/fork, and thread/resume already
// subscribe the connection that made the call.
func (a *adapter) resumeThreads(ctx context.Context, conn *connection) error {
	a.mu.Lock()
	sessions := make(map[string]*session, len(a.sessions))
	for id, s := range a.sessions {
		sessions[id] = s
	}
	a.mu.Unlock()
	for id, s := range sessions {
		result, err := callThread(ctx, conn, "thread/resume", map[string]any{
			"threadId":       id,
			"cwd":            s.workdir,
			"approvalPolicy": "never",
			"sandbox":        "danger-full-access",
		})
		if err != nil {
			return fmt.Errorf("codex: resume thread %s: %w", id, err)
		}
		resumed, err := threadID(result)
		if err != nil {
			return fmt.Errorf("codex: resume thread %s: %w", id, err)
		}
		if resumed != id {
			return fmt.Errorf("codex: resume thread %s: daemon returned a different thread id %s", id, resumed)
		}
		conn.registerThread(id)
	}
	return nil
}

// callThread makes a request that operates on params["threadId"]. The
// daemon refuses thread/resume and thread/fork on an archived thread, and
// Close archives every thread whose scope has ended, so a thread being used
// again (a fork of it, or a resume on redial after someone archived it from
// Codex Desktop) is unarchived first and the request retried once.
func callThread(ctx context.Context, conn *connection, method string, params map[string]any) (json.RawMessage, error) {
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	result, err := conn.call(callCtx, method, params)
	if err == nil || !strings.Contains(err.Error(), "is archived") {
		return result, err
	}
	threadID, _ := params["threadId"].(string)
	if _, err := conn.call(callCtx, "thread/unarchive", map[string]any{"threadId": threadID}); err != nil {
		return nil, fmt.Errorf("codex: unarchive thread %s: %w", threadID, err)
	}
	return conn.call(callCtx, method, params)
}

// CreateSession starts a Codex thread.
func (a *adapter) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	return a.thread(ctx, "thread/start", map[string]any{
		"model":                 model,
		"cwd":                   workdir,
		"approvalPolicy":        "never",
		"sandbox":               "danger-full-access",
		"serviceName":           "gimble",
		"experimentalRawEvents": true,
	}, &session{model: model, workdir: workdir})
}

// Fork starts a new thread with the conversation of sessionID so far.
func (a *adapter) Fork(ctx context.Context, sessionID string) (string, error) {
	parent, err := a.session(sessionID)
	if err != nil {
		return "", err
	}
	return a.thread(ctx, "thread/fork", map[string]any{
		"threadId":       sessionID,
		"cwd":            parent.workdir,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
	}, &session{model: parent.model, workdir: parent.workdir})
}

// thread calls a method that makes a thread and registers the returned
// thread id for notification routing before returning it.
func (a *adapter) thread(ctx context.Context, method string, params map[string]any, s *session) (string, error) {
	conn, err := a.conn(ctx)
	if err != nil {
		return "", err
	}
	result, err := callThread(ctx, conn, method, params)
	if err != nil {
		return "", err
	}
	id, err := threadID(result)
	if err != nil {
		return "", err
	}
	conn.registerThread(id)
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	return id, nil
}

// RunTurn runs one turn on the thread and blocks until it ends.
func (a *adapter) RunTurn(ctx context.Context, sessionID, prompt string, schema json.RawMessage, onEvent func(gimble.AgentEvent) error) (json.RawMessage, error) {
	s, err := a.session(sessionID)
	if err != nil {
		return nil, err
	}
	conn, err := a.conn(ctx)
	if err != nil {
		return nil, err
	}
	ch := conn.registerThread(sessionID)

	params := map[string]any{
		"threadId":       sessionID,
		"input":          input(prompt),
		"model":          s.model,
		"cwd":            s.workdir,
		"approvalPolicy": "never",
		"sandboxPolicy":  map[string]any{"type": "dangerFullAccess"},
	}
	if len(schema) > 0 {
		params["outputSchema"] = schema
	}
	startCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	result, err := conn.call(startCtx, "turn/start", params)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	turn, err := turnID(result)
	if err != nil {
		return nil, err
	}

	active := &activeTurn{conn: conn, turnID: turn, emit: newProjector(sessionID, turn, s.model, onEvent)}
	s.setActive(active)
	defer s.setActive(nil)

	text, err := readTurn(ctx, conn, ch, sessionID, turn, active.emit)
	if ctx.Err() != nil {
		interrupt(conn, sessionID, turn)
		drainCtx, drainCancel := context.WithTimeout(context.Background(), controlTimeout)
		_, _ = readTurn(drainCtx, conn, ch, sessionID, turn, active.emit)
		drainCancel()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if len(schema) == 0 {
		return json.Marshal(text)
	}
	return json.RawMessage(strings.TrimSpace(text)), nil
}

// Steer sends message into the thread's running turn, if there is one.
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
	_, err = active.conn.call(ctx, "turn/steer", map[string]any{
		"threadId":       sessionID,
		"expectedTurnId": active.turnID,
		"input":          input(message),
	})
	if err != nil {
		if s.getActive() != active {
			return nil // the turn ended first: the message is dropped
		}
		return err
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
	interrupt(active.conn, sessionID, active.turnID)
	return nil
}

// Close forgets sessionID locally and archives its thread in the daemon.
// Archiving unloads the thread, releasing the MCP children and descriptors
// it held in the shared daemon; unsubscribing would leave all of that
// loaded. The archive goes over a live connection: if the adapter's socket
// has died, Close redials (the daemon is usually still up) so a dead client
// socket cannot leak a thread while reporting success. It never starts,
// stops, or restarts the daemon: a daemon that is not running holds nothing
// for this thread, so that case is a successful Close with no call.
// Idempotent: an unknown or already closed id returns nil without a network
// call, and the daemon's "no rollout found" for an already-archived thread
// is success.
func (a *adapter) Close(ctx context.Context, sessionID string) error {
	a.mu.Lock()
	_, known := a.sessions[sessionID]
	delete(a.sessions, sessionID)
	a.mu.Unlock()
	if !known {
		return nil
	}
	// Local routing state goes first, whatever the connection's health, so
	// even a failed archive leaves nothing of this thread in the process.
	a.connMu.Lock()
	if a.sharedConn != nil {
		a.sharedConn.unregisterThread(sessionID)
	}
	a.connMu.Unlock()
	conn, err := a.connection(ctx, false)
	if errors.Is(err, errDaemonNotRunning) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("codex: archive thread %s: %w", sessionID, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if _, err := conn.call(callCtx, "thread/archive", map[string]any{"threadId": sessionID}); err != nil && !strings.Contains(err.Error(), "no rollout found") {
		return fmt.Errorf("codex: archive thread %s: %w", sessionID, err)
	}
	if a.onArchive != nil {
		a.onArchive(sessionID)
	}
	return nil
}

func (a *adapter) session(id string) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[id]; s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("codex: no session %q", id)
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

func interrupt(conn *connection, threadID, turnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	_, _ = conn.call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
}

func input(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
}

// readTurn consumes notifications from the thread's routed channel until
// the turn completes and returns the agent's final message.
func readTurn(ctx context.Context, conn *connection, ch chan rpcMessage, threadID, turnID string, emit *projector) (string, error) {
	var final string
	for {
		message, err := conn.next(ctx, ch)
		if err != nil {
			return "", err
		}
		if len(message.ID) > 0 && message.Method != "" {
			return "", refuse(conn, message)
		}
		if !matches(message.Params, threadID, turnID) {
			continue
		}
		switch message.Method {
		case "item/started":
			if err := emit.itemStarted(message.Params); err != nil {
				return "", err
			}
		case "item/agentMessage/delta":
			if err := emit.textDelta(message.Params); err != nil {
				return "", err
			}
		case "item/reasoning/summaryTextDelta":
			if err := emit.reasoningDelta(message.Params); err != nil {
				return "", err
			}
		case "item/commandExecution/outputDelta":
			if err := emit.toolOutputDelta(message.Params); err != nil {
				return "", err
			}
		case "item/completed":
			text, ok, err := emit.itemCompleted(message.Params)
			if err != nil {
				return "", err
			}
			if ok {
				final = text
			}
		case "rawResponse/completed":
			if err := emit.rawResponseCompleted(message.Params); err != nil {
				return "", err
			}
		case "turn/completed":
			if err := emit.turnCompleted(message.Params); err != nil {
				return "", err
			}
			status, failure := completedTurn(message.Params)
			switch status {
			case "completed":
				if strings.TrimSpace(final) == "" {
					return "", errors.New("codex: the turn completed without a final message")
				}
				return final, nil
			case "interrupted":
				return "", errors.New("codex: the turn was interrupted")
			case "failed":
				if failure == "" {
					failure = "the turn failed"
				}
				return "", errors.New("codex: " + failure)
			default:
				return "", fmt.Errorf("codex: the turn ended with status %q", status)
			}
		case "error":
			return "", errorNotification(message.Params)
		}
	}
}

// refuse declines an interactive request; Gimble turns run with approvals
// off and never answer prompts.
func refuse(conn *connection, message rpcMessage) error {
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
	if err := conn.respond(message, result); err != nil {
		return err
	}
	return fmt.Errorf("codex: unexpected interactive request %q", message.Method)
}

func threadID(raw json.RawMessage) (string, error) {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("codex: decode thread: %w", err)
	}
	if response.Thread.ID == "" {
		return "", errors.New("codex: the response has no thread.id")
	}
	return response.Thread.ID, nil
}

func turnID(raw json.RawMessage) (string, error) {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("codex: decode turn: %w", err)
	}
	if response.Turn.ID == "" {
		return "", errors.New("codex: the response has no turn.id")
	}
	return response.Turn.ID, nil
}

func completedTurn(raw json.RawMessage) (status, failure string) {
	var notification struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &notification)
	if notification.Turn.Error != nil {
		failure = notification.Turn.Error.Message
	}
	return notification.Turn.Status, failure
}

func errorNotification(raw json.RawMessage) error {
	var notification struct {
		Message string `json:"message"`
		Error   *struct {
			Message           string `json:"message"`
			AdditionalDetails string `json:"additionalDetails"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &notification) != nil {
		return fmt.Errorf("codex: error notification: %s", strings.TrimSpace(string(raw)))
	}
	if notification.Message != "" {
		return errors.New("codex: " + notification.Message)
	}
	if e := notification.Error; e != nil && e.Message != "" {
		if e.AdditionalDetails != "" {
			return fmt.Errorf("codex: %s: %s", e.Message, e.AdditionalDetails)
		}
		return errors.New("codex: " + e.Message)
	}
	return errors.New("codex: an empty error notification")
}

// matches reports whether a notification belongs to the turn.
func matches(raw json.RawMessage, threadID, turnID string) bool {
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

var _ gimble.HarnessAdapter = (*adapter)(nil)
