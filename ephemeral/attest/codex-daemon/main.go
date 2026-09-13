// Run with go run ./ephemeral/attest/codex-daemon. It proves the codex
// package attaches to the machine's one shared `codex app-server` daemon
// instead of launching a process per thread or per turn: the daemon's pid
// and the machine's app-server process set are identical before and after
// a run that creates two Codex sessions, runs a first and a second turn on
// one of them (the reuse path that replaced the old per-turn process),
// forks that session, and runs a turn on the fork. It also confirms,
// straight from the daemon over its own control socket, that the threads
// the run created are loaded there.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/tylergannon/gimble"
	"github.com/tylergannon/gimble/codex"
)

const model = "gpt-5.6-luna"

// recordingAdapter wraps the real codex adapter and records every thread id
// CreateSession and Fork hand back, so the proof can check afterward that
// the shared daemon has them loaded.
type recordingAdapter struct {
	inner gimble.HarnessAdapter
	mu    sync.Mutex
	ids   []string
}

func (a *recordingAdapter) record(id string) string {
	a.mu.Lock()
	a.ids = append(a.ids, id)
	a.mu.Unlock()
	return id
}

func (a *recordingAdapter) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	id, err := a.inner.CreateSession(ctx, model, workdir)
	if err != nil {
		return "", err
	}
	return a.record(id), nil
}

func (a *recordingAdapter) Fork(ctx context.Context, sessionID string) (string, error) {
	id, err := a.inner.Fork(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return a.record(id), nil
}

func (a *recordingAdapter) RunTurn(ctx context.Context, sessionID, prompt string, schema json.RawMessage, onEvent func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	return a.inner.RunTurn(ctx, sessionID, prompt, schema, onEvent)
}

func (a *recordingAdapter) Steer(ctx context.Context, sessionID, message string) error {
	return a.inner.Steer(ctx, sessionID, message)
}

func (a *recordingAdapter) Close(ctx context.Context, sessionID string) error {
	return a.inner.Close(ctx, sessionID)
}

func main() {
	dir, err := os.MkdirTemp("", "gimble-codex-daemon-")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Evidence directory:", dir)
	fmt.Println("Model: gpt-5.6-luna (Codex)")

	beforePID, beforePIDErr := daemonPID()
	beforeProcs, beforeProcsErr := appServerProcesses()
	fmt.Printf("Before: daemon_pid=%s (err=%v) app_server_processes=%v (err=%v)\n", beforePID, beforePIDErr, beforeProcs, beforeProcsErr)

	adapter := &recordingAdapter{inner: codex.New()}
	var turn1, turn1Again, turn2, forkTurn string

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	runErr := gimble.Run(gimble.Project(ctx, dir), "codex-daemon", func(ctx context.Context) error {
		first := gimble.NewSession(ctx, "first", adapter, model, dir)
		result, err := first.Generate[gimble.Text](ctx, "Reply with exactly one short sentence naming a prime number. No tools.")
		if err != nil {
			return fmt.Errorf("first turn: %w", err)
		}
		turn1 = string(result)

		// A second turn on the same session is the reuse path that
		// replaced the old per-turn process: it must run on the one
		// shared connection without a fresh thread/start.
		result, err = first.Generate[gimble.Text](ctx, "Reply with exactly one short sentence naming a different prime number. No tools.")
		if err != nil {
			return fmt.Errorf("second turn on first session: %w", err)
		}
		turn1Again = string(result)

		group := gimble.Group(ctx, "concurrent")
		group.Go("fork", func(ctx context.Context) error {
			fork, err := first.Fork(ctx, "forked")
			if err != nil {
				return err
			}
			// A turn on the fork, not just the fork itself, proves
			// thread/fork's subscription covers running a real turn,
			// not only creating the thread.
			result, err := fork.Generate[gimble.Text](ctx, "Reply with exactly one short sentence naming a fruit. No tools.")
			if err != nil {
				return fmt.Errorf("turn on fork: %w", err)
			}
			forkTurn = string(result)
			return nil
		})
		group.Go("second", func(ctx context.Context) error {
			second := gimble.NewSession(ctx, "second", adapter, model, dir)
			result, err := second.Generate[gimble.Text](ctx, "Reply with exactly one short sentence naming a color. No tools.")
			if err != nil {
				return fmt.Errorf("second turn: %w", err)
			}
			turn2 = string(result)
			return nil
		})
		return group.Wait()
	})

	fmt.Printf("Turn 1 answer: %q\n", turn1)
	fmt.Printf("Turn 1 (second turn, same session) answer: %q\n", turn1Again)
	fmt.Printf("Turn 2 answer: %q\n", turn2)
	fmt.Printf("Fork turn answer: %q\n", forkTurn)
	fmt.Printf("Run error: %v\n", runErr)

	afterPID, afterPIDErr := daemonPID()
	afterProcs, afterProcsErr := appServerProcesses()
	fmt.Printf("After: daemon_pid=%s (err=%v) app_server_processes=%v (err=%v)\n", afterPID, afterPIDErr, afterProcs, afterProcsErr)

	adapter.mu.Lock()
	createdThreads := slices.Clone(adapter.ids)
	adapter.mu.Unlock()
	fmt.Printf("Threads created by this run: %v\n", createdThreads)

	loaded, loadedErr := loadedThreadIDs(ctx)
	fmt.Printf("Daemon thread/loaded/list: %v (err=%v)\n", loaded, loadedErr)
	allLoaded := loadedErr == nil
	for _, id := range createdThreads {
		if !slices.Contains(loaded, id) {
			allLoaded = false
			fmt.Printf("  MISSING from thread/loaded/list: %s\n", id)
		}
	}
	fmt.Printf("All created threads loaded in the shared daemon: %v\n", allLoaded)

	samePID := beforePIDErr == nil && afterPIDErr == nil && beforePID != "" && beforePID == afterPID
	sameProcs := beforeProcsErr == nil && afterProcsErr == nil && slices.Equal(beforeProcs, afterProcs)
	ok := runErr == nil && turn1 != "" && turn1Again != "" && turn2 != "" && forkTurn != "" && samePID && sameProcs && allLoaded && len(createdThreads) == 3
	fmt.Printf("Success: %v (same_daemon_pid=%v same_app_server_processes=%v)\n", ok, samePID, sameProcs)
	if !ok {
		os.Exit(1)
	}
}

// daemonPID returns the pid of the machine's managed app-server daemon: the
// process started by `codex app-server daemon start`, listening on its
// control socket. `pgrep -f 'app-server --listen unix'` also matches the
// shell wrapper that launched it (its command line embeds the same text),
// so this keeps only the pid whose command actually starts with `codex `.
func daemonPID() (string, error) {
	out, err := exec.Command("pgrep", "-fl", "app-server --listen unix").Output()
	if err != nil {
		return "", fmt.Errorf("pgrep: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(fields) == 2 && strings.HasPrefix(fields[1], "codex ") {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no managed daemon process found in:\n%s", out)
}

// appServerProcesses returns the sorted pids of every process on the
// machine whose command line mentions `app-server` (the managed daemon,
// its launch wrapper, and any other client's app-server process, such as
// the ChatGPT desktop app's). Comparing this set before and after the run
// is how the proof shows no new app-server process was launched.
func appServerProcesses() ([]string, error) {
	out, err := exec.Command("pgrep", "-f", "app-server").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil // pgrep: no matches
		}
		return nil, fmt.Errorf("pgrep: %w", err)
	}
	pids := strings.Fields(strings.TrimSpace(string(out)))
	slices.Sort(pids)
	return pids, nil
}

// loadedThreadIDs asks the daemon directly, over its own control socket,
// which threads it has loaded. It is independent of the codex package (that
// package's connection type is unexported) so it proves the daemon's state
// from the outside, the same way another client would see it.
func loadedThreadIDs(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "codex", "app-server", "daemon", "version").Output()
	if err != nil {
		return nil, fmt.Errorf("daemon version: %w", err)
	}
	var version struct {
		SocketPath string `json:"socketPath"`
	}
	if err := json.Unmarshal(out, &version); err != nil {
		return nil, fmt.Errorf("decode daemon version: %w", err)
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", version.SocketPath)
		},
	}}
	ws, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", version.SocketPath, err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(64 << 20)

	id := 0
	call := func(method string, params any) (map[string]any, error) {
		id++
		wantID := id
		body, err := json.Marshal(map[string]any{"id": wantID, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if err := ws.Write(ctx, websocket.MessageText, body); err != nil {
			return nil, err
		}
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return nil, err
			}
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if idValue, ok := m["id"].(float64); ok && int(idValue) == wantID && m["method"] == nil {
				if errValue := m["error"]; errValue != nil {
					return nil, fmt.Errorf("%s: %v", method, errValue)
				}
				return m, nil
			}
		}
	}
	if _, err := call("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "gimble-attest", "title": "gimble-attest", "version": "dev"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	notice, err := json.Marshal(map[string]any{"method": "initialized", "params": map[string]any{}})
	if err != nil {
		return nil, err
	}
	if err := ws.Write(ctx, websocket.MessageText, notice); err != nil {
		return nil, err
	}
	resp, err := call("thread/loaded/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("thread/loaded/list: %w", err)
	}
	result, _ := resp["result"].(map[string]any)
	data, _ := result["data"].([]any)
	var ids []string
	for _, entry := range data {
		if id, ok := threadIDOf(entry); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func threadIDOf(entry any) (string, bool) {
	switch v := entry.(type) {
	case string:
		return v, true
	case map[string]any:
		if id, ok := v["id"].(string); ok {
			return id, true
		}
		if thread, ok := v["thread"].(map[string]any); ok {
			if id, ok := thread["id"].(string); ok {
				return id, true
			}
		}
	}
	return "", false
}
