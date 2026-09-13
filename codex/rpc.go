package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// readLimit is generous because a turn can stream large frames (command
// output, big diffs).
const readLimit = 64 << 20

// connection is one WebSocket connection to the machine's shared `codex
// app-server` daemon, spoken to in JSON-RPC. Every session and turn shares
// it: the daemon, not this process, keeps the threads loaded.
type connection struct {
	ws     *websocket.Conn
	nextID atomic.Int64

	writeMu sync.Mutex

	pendMu  sync.Mutex
	pending map[int64]chan rpcMessage

	threadsMu sync.Mutex
	threads   map[string]chan rpcMessage

	readDone chan struct{}
	readOnce sync.Once
	readErr  error
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

// daemonVersion is `codex app-server daemon version`'s JSON.
type daemonVersion struct {
	Status     string `json:"status"`
	SocketPath string `json:"socketPath"`
}

// errDaemonNotRunning is connect's answer when the daemon is down and the
// caller did not ask to start it.
var errDaemonNotRunning = errors.New("codex: app-server daemon is not running")

// connect finds the machine's shared app-server daemon, dials its socket,
// and completes the JSON-RPC handshake. With startDaemon it starts the
// daemon if none is running; without it, a stopped daemon is
// errDaemonNotRunning. It never stops or restarts the daemon: other clients
// (Codex Desktop included) share it.
func connect(ctx context.Context, startDaemon bool) (*connection, error) {
	socketPath, running, err := daemonStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !running {
		if !startDaemon {
			return nil, errDaemonNotRunning
		}
		if _, err := runCodex(ctx, "app-server", "daemon", "start"); err != nil {
			return nil, fmt.Errorf("codex: start app-server daemon: %w", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			socketPath, running, err = daemonStatus(ctx)
			if err != nil {
				return nil, err
			}
			if running {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("codex: app-server daemon did not come up within 10s")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}}
	ws, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("codex: dial app-server daemon at %s: %w", socketPath, err)
	}
	ws.SetReadLimit(readLimit)

	c := &connection{
		ws:       ws,
		pending:  make(map[int64]chan rpcMessage),
		threads:  make(map[string]chan rpcMessage),
		readDone: make(chan struct{}),
	}
	go c.read()

	initCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err = c.call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "gimble", "title": "Gimble", "version": "dev"},
		"capabilities": map[string]any{"experimentalApi": true},
	})
	if err == nil {
		err = c.send(map[string]any{"method": "initialized", "params": map[string]any{}})
	}
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	return c, nil
}

// daemonStatus runs `codex app-server daemon version` and reports the
// socket to dial and whether the daemon is up.
func daemonStatus(ctx context.Context) (socketPath string, running bool, err error) {
	out, err := runCodex(ctx, "app-server", "daemon", "version")
	if err != nil {
		return "", false, fmt.Errorf("codex: app-server daemon version: %w", err)
	}
	var v daemonVersion
	if err := json.Unmarshal(out, &v); err != nil {
		return "", false, fmt.Errorf("codex: decode daemon version: %w", err)
	}
	if v.SocketPath == "" {
		return "", false, fmt.Errorf("codex: daemon version has no socketPath: %s", strings.TrimSpace(string(out)))
	}
	return v.SocketPath, v.Status == "running" || v.Status == "alreadyRunning", nil
}

func runCodex(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "codex", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// registerThread opens routing for threadID's notifications and server
// requests. Call it once the thread is known, before waiting on its turns.
func (c *connection) registerThread(threadID string) chan rpcMessage {
	c.threadsMu.Lock()
	defer c.threadsMu.Unlock()
	if ch, ok := c.threads[threadID]; ok {
		return ch
	}
	ch := make(chan rpcMessage, 512)
	c.threads[threadID] = ch
	return ch
}

// unregisterThread stops routing threadID's notifications and server
// requests to this connection. It is the counterpart to registerThread,
// called from Close; it does not itself tell the daemon anything, that is
// thread/archive's job.
func (c *connection) unregisterThread(threadID string) {
	c.threadsMu.Lock()
	defer c.threadsMu.Unlock()
	delete(c.threads, threadID)
}

func (c *connection) read() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.finishRead(err)
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(data, &message); err != nil {
			c.finishRead(fmt.Errorf("codex: decode app-server message: %w", err))
			return
		}
		c.dispatch(message)
	}
}

func (c *connection) finishRead(err error) {
	c.readOnce.Do(func() {
		c.readErr = err
		close(c.readDone)
	})
}

// dispatch routes a response to its caller by id. Everything else carries a
// threadId and is routed to that thread's channel; a message for a thread
// this connection has not registered (another client's thread, sharing the
// same daemon) is dropped.
func (c *connection) dispatch(message rpcMessage) {
	if len(message.ID) > 0 && message.Method == "" {
		if id, ok := parseID(message.ID); ok {
			c.pendMu.Lock()
			response := c.pending[id]
			c.pendMu.Unlock()
			if response != nil {
				response <- message
				return
			}
		}
	}
	threadID := messageThreadID(message.Params)
	if threadID == "" {
		return
	}
	c.threadsMu.Lock()
	ch := c.threads[threadID]
	c.threadsMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- message:
	case <-c.readDone:
	}
}

func messageThreadID(raw json.RawMessage) string {
	var envelope struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.ThreadID
}

func (c *connection) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	response := make(chan rpcMessage, 1)
	c.pendMu.Lock()
	c.pending[id] = response
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case message := <-response:
		if message.Error != nil {
			return nil, fmt.Errorf("%s: %w", method, message.Error)
		}
		return message.Result, nil
	case <-c.readDone:
		return nil, c.exitError()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *connection) respond(message rpcMessage, result any) error {
	return c.send(map[string]any{"id": message.ID, "result": result})
}

func (c *connection) send(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(context.Background(), websocket.MessageText, encoded)
}

// next returns the next notification or server request for threadID.
func (c *connection) next(ctx context.Context, ch chan rpcMessage) (rpcMessage, error) {
	select {
	case message := <-ch:
		return message, nil
	case <-c.readDone:
		return rpcMessage{}, c.exitError()
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	}
}

func (c *connection) exitError() error {
	return fmt.Errorf("codex: app-server connection closed: %w", c.readErr)
}

// dead reports whether the connection's reader has already failed: the
// daemon socket is gone, so a fresh dial is needed.
func (c *connection) dead() bool {
	select {
	case <-c.readDone:
		return true
	default:
		return false
	}
}

func parseID(raw json.RawMessage) (int64, bool) {
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	number, err := strconv.ParseInt(text, 10, 64)
	return number, err == nil
}
