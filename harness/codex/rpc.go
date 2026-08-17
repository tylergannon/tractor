package codex

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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type processConfig struct {
	command string
	args    []string
	env     []string
	stderr  io.Writer
}

type connection struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stderr    *lockedBuffer
	nextID    atomic.Int64
	writeMu   sync.Mutex
	pending   map[int64]chan rpcMessage
	pendMu    sync.Mutex
	messages  chan rpcMessage
	readDone  chan struct{}
	readOnce  sync.Once
	readMu    sync.Mutex
	readErr   error
	waitDone  chan error
	closeOnce sync.Once
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("codex app-server JSON-RPC error %d", e.Code)
	}
	return fmt.Sprintf("codex app-server JSON-RPC error %d: %s", e.Code, e.Message)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startConnection(config processConfig) (*connection, error) {
	cmd := exec.Command(config.command, config.args...)
	if config.env != nil {
		cmd.Env = config.env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &lockedBuffer{}
	if config.stderr == nil {
		cmd.Stderr = stderr
	} else {
		cmd.Stderr = io.MultiWriter(config.stderr, stderr)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	conn := &connection{
		cmd:      cmd,
		stdin:    stdin,
		stderr:   stderr,
		pending:  make(map[int64]chan rpcMessage),
		messages: make(chan rpcMessage, 512),
		readDone: make(chan struct{}),
		waitDone: make(chan error, 1),
	}
	go conn.read(stdout)
	go func() {
		err := cmd.Wait()
		conn.waitDone <- err
		conn.finishRead(err)
	}()
	return conn, nil
}

func (c *connection) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message rpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			c.finishRead(fmt.Errorf("decode Codex app-server message: %w", err))
			return
		}
		c.dispatch(message)
	}
	if err := scanner.Err(); err != nil {
		c.finishRead(err)
		return
	}
	c.finishRead(io.EOF)
}

func (c *connection) finishRead(err error) {
	c.readOnce.Do(func() {
		c.readMu.Lock()
		c.readErr = err
		c.readMu.Unlock()
		close(c.readDone)
	})
}

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
	select {
	case c.messages <- message:
	case <-c.readDone:
	}
}

func (c *connection) initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    "tractor",
			"title":   "Tractor agent",
			"version": "dev",
		},
		"capabilities": map[string]any{},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
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
	if err := c.send(rpcRequest{ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	message, err := c.nextResponse(ctx, response)
	if err != nil {
		return nil, err
	}
	if message.Error != nil {
		return nil, message.Error
	}
	return message.Result, nil
}

func (c *connection) notify(method string, params any) error {
	return c.send(rpcNotification{Method: method, Params: params})
}

func (c *connection) respond(message rpcMessage, result any, responseErr *rpcError) error {
	return c.send(rpcResponse{ID: message.ID, Result: result, Error: responseErr})
}

func (c *connection) send(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = c.stdin.Write(encoded)
	return err
}

func (c *connection) next(ctx context.Context) (rpcMessage, error) {
	select {
	case message := <-c.messages:
		return message, nil
	default:
	}
	select {
	case message := <-c.messages:
		return message, nil
	case <-c.readDone:
		return rpcMessage{}, c.terminalReadError()
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	}
}

func (c *connection) nextResponse(ctx context.Context, response <-chan rpcMessage) (rpcMessage, error) {
	select {
	case message := <-response:
		return message, nil
	case <-c.readDone:
		return rpcMessage{}, c.terminalReadError()
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	}
}

func (c *connection) terminalReadError() error {
	c.readMu.Lock()
	err := c.readErr
	c.readMu.Unlock()
	stderr := strings.TrimSpace(c.stderr.String())
	if stderr != "" {
		return fmt.Errorf("codex app-server exited: %s: %w", stderr, err)
	}
	if err == nil {
		return io.EOF
	}
	return err
}

func (c *connection) close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.waitDone:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.waitDone
		}
	})
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

func isRPCError(err error) bool {
	var rpcErr *rpcError
	return errors.As(err, &rpcErr)
}

type rpcRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcNotification struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

func defaultProcessConfig() processConfig {
	return processConfig{
		command: "codex",
		args:    []string{"app-server", "--stdio"},
		env:     os.Environ(),
	}
}
