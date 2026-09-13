// Run with go run ./ephemeral/attest/issue129. The worker and reviewer are
// real cheap-model sessions. noisyWorker injects a deterministic event burst
// ahead of the worker's native activity so the live reviewer must work across
// a retention gap; the durable record is left in the printed temporary path.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tylergannon/gimble"
	"github.com/tylergannon/gimble/claude"
	"github.com/tylergannon/gimble/codex"
)

const lookLimit = 64 << 10

type noisyWorker struct {
	inner gimble.HarnessAdapter
	once  sync.Once
	steer atomic.Bool
}

func (a *noisyWorker) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	return a.inner.CreateSession(ctx, model, workdir)
}

func (a *noisyWorker) RunTurn(ctx context.Context, sessionID, prompt string, schema json.RawMessage, emit func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	return a.inner.RunTurn(ctx, sessionID, prompt, schema, func(event gimble.AgentEvent) error {
		if err := emit(event); err != nil {
			return err
		}
		var injectErr error
		a.once.Do(func() {
			for i := range 400 {
				injectErr = emit(noiseEvent(sessionID, "session.tool.success", map[string]any{
					"assistantMessageID": "noise-message", "id": fmt.Sprintf("noise-%03d", i),
					"content":  []any{map[string]any{"type": "text", "text": strings.Repeat("noisy tool output ", 240)}},
					"executed": true,
				}))
				if injectErr != nil {
					return
				}
			}
			injectErr = emit(noiseEvent(sessionID, "session.tool.called", map[string]any{
				"assistantMessageID": "noise-message", "id": "proposed-change",
				"input": map[string]any{"action": "add workflow-level supervisor buffer controls"}, "executed": true,
			}))
		})
		return injectErr
	})
}

func noiseEvent(sessionID, eventType string, data map[string]any) gimble.AgentEvent {
	data["sessionID"] = sessionID
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	ref, err := json.Marshal(map[string]any{
		"provider": "issue129", "sessionID": sessionID, "messageID": "noise-message",
	})
	if err != nil {
		panic(err)
	}
	return gimble.AgentEvent{Type: eventType, Data: raw, NativeRef: ref}
}

func (a *noisyWorker) Steer(ctx context.Context, sessionID, message string) error {
	if strings.Contains(message, "inside the runtime") {
		a.steer.Store(true)
	}
	return a.inner.Steer(ctx, sessionID, message)
}

func (a *noisyWorker) Fork(ctx context.Context, sessionID string) (string, error) {
	return a.inner.Fork(ctx, sessionID)
}

func (a *noisyWorker) Close(ctx context.Context, sessionID string) error {
	return a.inner.Close(ctx, sessionID)
}

type measuredReviewer struct {
	inner     gimble.HarnessAdapter
	maxPrompt atomic.Int64
	sawGap    atomic.Bool
	sawTarget atomic.Bool
}

func (a *measuredReviewer) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	return a.inner.CreateSession(ctx, model, workdir)
}

func (a *measuredReviewer) RunTurn(ctx context.Context, sessionID, prompt string, schema json.RawMessage, emit func(gimble.AgentEvent) error) (gimble.TurnResult, error) {
	for old := a.maxPrompt.Load(); int64(len(prompt)) > old && !a.maxPrompt.CompareAndSwap(old, int64(len(prompt))); old = a.maxPrompt.Load() {
	}
	if strings.Contains(prompt, "[gap] Supervisor activity items") {
		a.sawGap.Store(true)
	}
	if strings.Contains(prompt, "call_id=proposed-change") {
		a.sawTarget.Store(true)
	}
	return a.inner.RunTurn(ctx, sessionID, prompt, schema, emit)
}

func (a *measuredReviewer) Steer(ctx context.Context, sessionID, message string) error {
	return a.inner.Steer(ctx, sessionID, message)
}

func (a *measuredReviewer) Fork(ctx context.Context, sessionID string) (string, error) {
	return a.inner.Fork(ctx, sessionID)
}

func (a *measuredReviewer) Close(ctx context.Context, sessionID string) error {
	return a.inner.Close(ctx, sessionID)
}

func main() {
	dir, err := os.MkdirTemp("", "gimble-issue129-")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Evidence directory:", dir)
	fmt.Println("Models: worker=gpt-5.6-luna reviewer=haiku")

	workerAdapter := &noisyWorker{inner: codex.New()}
	reviewerAdapter := &measuredReviewer{inner: claude.New()}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err = gimble.Run(gimble.Project(ctx, dir), "supervisor-bounds", func(ctx context.Context) error {
		worker := gimble.NewSession(ctx, "worker", workerAdapter, "gpt-5.6-luna", dir)
		reviewer := gimble.NewSession(ctx, "reviewer", reviewerAdapter, "haiku", dir)
		result, err := worker.Generate[gimble.Text](ctx,
			"Run the shell command `sleep 12`. Incorporate any supervisor instruction, then explain in one sentence what boundary the implementation should preserve.",
			gimble.WithSupervisor(reviewer,
				"If the recent activity proposes workflow-level supervisor buffer controls, object with exactly: keep supervisor buffer controls inside the runtime. Otherwise return no objections.",
				gimble.WithInterval(time.Second)))
		fmt.Printf("Worker result: %q\n", result)
		return err
	})

	maxPrompt := reviewerAdapter.maxPrompt.Load()
	fmt.Printf("Observed: retained_limit_bytes=%d max_prompt_bytes=%d gap=%t target=%t useful_steer=%t run_error=%v parent_error=%v\n",
		256<<10, maxPrompt, reviewerAdapter.sawGap.Load(), reviewerAdapter.sawTarget.Load(), workerAdapter.steer.Load(), err, ctx.Err())
	if err != nil || ctx.Err() != nil || maxPrompt > lookLimit || !reviewerAdapter.sawGap.Load() || !reviewerAdapter.sawTarget.Load() || !workerAdapter.steer.Load() {
		os.Exit(1)
	}
}
