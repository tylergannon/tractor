package gimble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tylergannon/gimble/internal/runlog"
)

type issue131Adapter struct {
	mu   sync.Mutex
	next int
}

func (a *issue131Adapter) CreateSession(context.Context, string, string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	return fmt.Sprintf("issue131-native-%d", a.next), nil
}

func (*issue131Adapter) RunTurn(_ context.Context, _ string, prompt string, _ json.RawMessage, emit func(AgentEvent) error) (TurnResult, error) {
	out, err := json.Marshal("ok")
	return TurnResult{Output: out}, err
}

func (*issue131Adapter) Steer(context.Context, string, string) error { return nil }

func (*issue131Adapter) Fork(_ context.Context, session string) (string, error) {
	return session + "-fork", nil
}

func (*issue131Adapter) Close(context.Context, string) error { return nil }

func issue131Run(t *testing.T, project, name string, body func(context.Context, *run) error) (*run, error) {
	t.Helper()
	var got *run
	err := Run(Project(t.Context(), project), name, func(ctx context.Context) error {
		scope, err := current(ctx)
		if err != nil {
			return err
		}
		got = scope.run
		return body(ctx, got)
	})
	return got, err
}

func TestIssue131RunClosesOwnedSessionLogs(t *testing.T) {
	adapter := &issue131Adapter{}
	r, err := issue131Run(t, t.TempDir(), "session-close", func(ctx context.Context, _ *run) error {
		for i := 0; i < 4; i++ {
			s := NewSession(ctx, "worker", adapter, "fake", t.TempDir())
			if _, err := s.Generate[Text](ctx, fmt.Sprintf("turn-%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions) != 4 {
		t.Fatalf("session writers = %d, want 4", len(r.sessions))
	}
	for session, writer := range r.sessions {
		if _, err := writer.file.Stat(); err == nil {
			t.Errorf("session writer %s remains open after Run returned", session)
		}
	}
}

func TestIssue131RunSurfacesRecordingFailures(t *testing.T) {
	t.Run("project open", func(t *testing.T) {
		project := t.TempDir()
		if err := os.Mkdir(filepath.Join(project, "project.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		r, err := issue131Run(t, project, "project-open", func(context.Context, *run) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "open project log") {
			t.Fatalf("Run error = %v, want project-log open failure", err)
		}
		var complete Complete
		if readErr := runlog.Read[LifecycleRecord](t.Context(), r.dir, func(record LifecycleRecord) error {
			if event, ok := record.Event.(Complete); ok {
				complete = event
			}
			return nil
		}); readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(complete.RecordingError, "open project log") {
			t.Fatalf("complete recording error = %q", complete.RecordingError)
		}
	})

	t.Run("session open", func(t *testing.T) {
		adapter := &issue131Adapter{}
		_, err := issue131Run(t, t.TempDir(), "session-open", func(ctx context.Context, r *run) error {
			if err := os.WriteFile(filepath.Join(r.dir, "sessions"), []byte("blocked"), 0o644); err != nil {
				return err
			}
			_, generateErr := NewSession(ctx, "worker", adapter, "fake", t.TempDir()).Generate[Text](ctx, "turn")
			return generateErr
		})
		if err == nil || !strings.Contains(err.Error(), "open session log") {
			t.Fatalf("Run error = %v, want session-log open failure", err)
		}
	})

	t.Run("session write", func(t *testing.T) {
		adapter := &issue131Adapter{}
		_, err := issue131Run(t, t.TempDir(), "session-write", func(ctx context.Context, r *run) error {
			s := NewSession(ctx, "worker", adapter, "fake", t.TempDir())
			if _, err := s.Generate[Text](ctx, "first"); err != nil {
				return err
			}
			r.mu.Lock()
			writer := r.sessions[s.id]
			r.mu.Unlock()
			if err := writer.file.Close(); err != nil {
				return err
			}
			_, generateErr := s.Generate[Text](ctx, "second")
			return generateErr
		})
		if err == nil || !strings.Contains(err.Error(), "write session log") {
			t.Fatalf("Run error = %v, want session-log write failure", err)
		}
	})

	t.Run("session close", func(t *testing.T) {
		adapter := &issue131Adapter{}
		_, err := issue131Run(t, t.TempDir(), "session-close-error", func(ctx context.Context, r *run) error {
			s := NewSession(ctx, "worker", adapter, "fake", t.TempDir())
			if _, err := s.Generate[Text](ctx, "turn"); err != nil {
				return err
			}
			r.mu.Lock()
			writer := r.sessions[s.id]
			r.mu.Unlock()
			return writer.file.Close()
		})
		if err == nil || !strings.Contains(err.Error(), "close session log") {
			t.Fatalf("Run error = %v, want session-log close failure", err)
		}
	})

	t.Run("run write and workflow error", func(t *testing.T) {
		workflowErr := errors.New("workflow failed")
		_, err := issue131Run(t, t.TempDir(), "combined", func(_ context.Context, r *run) error {
			if err := r.writer.file.Close(); err != nil {
				return err
			}
			return workflowErr
		})
		if !errors.Is(err, workflowErr) {
			t.Fatalf("Run error = %v, want original workflow error", err)
		}
		if !strings.Contains(err.Error(), "write run log") {
			t.Fatalf("Run error = %v, want recording failure too", err)
		}
	})
}
