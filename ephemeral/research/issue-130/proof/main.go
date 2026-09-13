package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/tylergannon/gimble"
	"github.com/tylergannon/gimble/claude"
	"github.com/tylergannon/gimble/codex"
	"github.com/tylergannon/gimble/web"
)

type deterministicAdapter struct {
	project string
	next    atomic.Int64
}

func (a *deterministicAdapter) CreateSession(context.Context, string, string) (string, error) {
	return "proof-native-" + strconv.FormatInt(a.next.Add(1), 10), nil
}

func (a *deterministicAdapter) RunTurn(ctx context.Context, sessionID, _ string, _ json.RawMessage, emit func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	if err := waitFile(ctx, filepath.Join(a.project, "start")); err != nil {
		return gimble.TurnResult{}, err
	}
	// Both concurrent native sessions deliberately reuse provider-local IDs;
	// Gimble must still produce distinct canonical message identities.
	messageID, toolID := "assistant-shared", "tool-shared"
	events := []struct {
		kind string
		data map[string]any
	}{
		{"session.step.started", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "agent": "proof", "model": map[string]any{"id": "deterministic", "providerID": "proof"}}},
		{"session.text.started", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "ordinal": 0}},
		{"session.text.delta", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "ordinal": 0, "delta": "draft-" + sessionID}},
		{"session.reasoning.started", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "ordinal": 0, "state": map[string]any{"phase": "proof"}}},
		{"session.reasoning.delta", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "ordinal": 0, "delta": "thinking-" + sessionID}},
		{"session.tool.input.started", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "id": toolID, "name": "shell"}},
		{"session.tool.input.delta", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "id": toolID, "delta": "{\"command\":\"printf proof\"}"}},
		{"session.tool.input.ended", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "id": toolID, "text": "{\"command\":\"printf proof\"}"}},
		{"session.tool.called", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "id": toolID, "input": map[string]any{"command": "printf proof"}, "executed": true}},
		{"session.tool.progress", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "id": toolID, "metadata": map[string]any{"output": "PROOF_TOOL_PROGRESS_" + sessionID}}},
	}
	for _, event := range events {
		if err := emit(native(event.kind, event.data, messageID)); err != nil {
			return gimble.TurnResult{}, err
		}
	}
	if err := waitFile(ctx, filepath.Join(a.project, "finish")); err != nil {
		return gimble.TurnResult{}, err
	}
	final := []struct {
		kind string
		data map[string]any
	}{
		{"session.text.ended", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "ordinal": 0, "text": "FINAL_TEXT_" + sessionID}},
		{"session.reasoning.ended", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "ordinal": 0, "text": "FINAL_REASONING_" + sessionID, "state": map[string]any{"phase": "done"}}},
		{"session.tool.success", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "id": toolID, "content": []any{map[string]any{"type": "text", "text": "FINAL_TOOL_" + sessionID}}, "metadata": map[string]any{"output": "FINAL_TOOL_" + sessionID}, "executed": true}},
		{"session.step.ended", map[string]any{"sessionID": sessionID, "assistantMessageID": messageID, "finish": "stop", "cost": 0, "tokens": map[string]any{"input": 0, "output": 0, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}}}},
	}
	for _, event := range final {
		if err := emit(native(event.kind, event.data, messageID)); err != nil {
			return gimble.TurnResult{}, err
		}
	}
	out, err := json.Marshal("FINAL_RESULT_" + sessionID)
	return gimble.TurnResult{Output: out}, err
}

func native(kind string, data map[string]any, messageID string) gimble.AgentEvent {
	raw, _ := json.Marshal(data)
	ref, _ := json.Marshal(map[string]any{"provider": "proof", "messageID": messageID, "accounting": map[string]any{"costAvailable": true, "tokensAvailable": true, "costSource": "provider"}})
	return gimble.AgentEvent{Type: kind, Data: raw, NativeRef: ref}
}
func (*deterministicAdapter) Steer(context.Context, string, string) error { return nil }
func (*deterministicAdapter) Fork(_ context.Context, id string) (string, error) {
	return id + "-fork", nil
}
func (*deterministicAdapter) Close(context.Context, string) error { return nil }

func waitFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func main() {
	mode := flag.String("mode", "deterministic", "deterministic, codex, claude, or interrupt")
	project := flag.String("project", "", "isolated proof project directory")
	port := flag.Int("port", 18081, "loopback port")
	flag.Parse()
	if *project == "" {
		fatal(errors.New("-project is required"))
	}
	if err := os.MkdirAll(filepath.Join(*project, "work"), 0755); err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := web.NewRuntime(ctx, *project, web.WithPort(*port))
	if err != nil {
		fatal(err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- runtime.Run(ctx, "observation-proof", func(ctx context.Context) error {
			switch *mode {
			case "deterministic":
				adapter := &deterministicAdapter{project: *project}
				group := gimble.Group(ctx, "concurrent")
				for _, name := range []string{"alpha", "beta"} {
					name := name
					group.Go(name, func(ctx context.Context) error {
						_, err := gimble.NewSession(ctx, name, adapter, "deterministic", filepath.Join(*project, "work")).Generate[gimble.Text](ctx, "deterministic observation proof")
						return err
					})
				}
				return group.Wait()
			case "codex":
				return live(ctx, codex.New(), "gpt-5.6-luna", *project, false)
			case "claude":
				return live(ctx, claude.New(), "haiku", *project, false)
			case "interrupt":
				return live(ctx, codex.New(), "gpt-5.6-luna", *project, true)
			default:
				return fmt.Errorf("unknown mode %q", *mode)
			}
		})
	}()
	runID, err := waitRun(ctx, *project)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("PROOF_READY=http://127.0.0.1:%d|%s\n", *port, runID)
	if err := <-runDone; err != nil && *mode != "interrupt" {
		fatal(err)
	}
	fmt.Println("PROOF_COMPLETE")
	_ = waitFile(ctx, filepath.Join(*project, "stop"))
}

func live(ctx context.Context, adapter gimble.HarnessAdapter, model, project string, interrupt bool) error {
	session := gimble.NewSession(ctx, "live", adapter, model, filepath.Join(project, "work"))
	if interrupt {
		go func() { time.Sleep(2 * time.Second); _ = session.Interrupt(ctx) }()
	}
	_, err := session.Generate[gimble.Text](ctx, "Use exactly one shell tool to run `printf GIMBLE_LIVE_TOOL_MARKER`, then answer exactly GIMBLE_LIVE_FINAL_MARKER.")
	return err
}

func waitRun(ctx context.Context, project string) (string, error) {
	for {
		entries, _ := os.ReadDir(filepath.Join(project, "runs"))
		if len(entries) > 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			if len(names) > 0 {
				return names[len(names)-1], nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "proof:", err); os.Exit(1) }
