package gimble

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestCloseFailureNeverEntersScopeErrorButRunAggregatesEvery covers
// acceptance item 1: a fake adapter whose Close fails leaves the scope's
// own error untouched, records SessionClosed{Error} for each failing
// session, and Run's returned error carries every failure as a *CloseError,
// which RunEnded's Error also reflects.
func TestCloseFailureNeverEntersScopeErrorButRunAggregatesEvery(t *testing.T) {
	boomOne := errors.New("adapter refused to release session one")
	boomTwo := errors.New("adapter refused to release session two")
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		return "ok", nil
	}}
	f.closeErr = func(session string) error {
		switch session {
		case "native-1":
			return boomOne
		case "native-2":
			return boomTwo
		default:
			return nil
		}
	}
	project := t.TempDir()
	var scopeErr error
	err := Run(Project(t.Context(), project), "close-fail", func(ctx context.Context) error {
		scopeErr = Scope(ctx, "lap", func(ctx context.Context) error {
			if _, err := NewSession(ctx, "one", f, "m", "/w").Generate[Text](ctx, "hi"); err != nil {
				return err
			}
			if _, err := NewSession(ctx, "two", f, "m", "/w").Generate[Text](ctx, "hi"); err != nil {
				return err
			}
			return nil
		})
		return scopeErr
	})

	if scopeErr != nil {
		t.Fatalf("Scope's own error = %v, want nil: a body that returned nil must not be turned into a failure by cleanup", scopeErr)
	}

	var closeErr *CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Run error = %v, want it to match *CloseError with errors.As", err)
	}
	if !errors.Is(err, boomOne) || !errors.Is(err, boomTwo) {
		t.Fatalf("Run error = %v, want it to carry both close failures, not just the first", err)
	}

	runs, globErr := filepath.Glob(filepath.Join(project, "runs", "*", "run.jsonl"))
	if globErr != nil || len(runs) != 1 {
		t.Fatalf("run logs = %v, %v", runs, globErr)
	}
	records := readRecords[LifecycleRecord](t, runs[0])
	var closedErrors []string
	var runEnded RunEnded
	for _, record := range records {
		switch event := record.Event.(type) {
		case SessionClosed:
			if event.Error != "" {
				closedErrors = append(closedErrors, event.Error)
			}
		case RunEnded:
			runEnded = event
		}
	}
	if len(closedErrors) != 2 {
		t.Fatalf("SessionClosed{Error} records = %v, want one per failing session", closedErrors)
	}
	if !strings.Contains(runEnded.Error, boomOne.Error()) || !strings.Contains(runEnded.Error, boomTwo.Error()) {
		t.Fatalf("RunEnded.Error = %q, want it to mention both close failures", runEnded.Error)
	}
}

// TestCancelledRunStillClosesEverySessionOnAFreshContext covers acceptance
// item 2: cancelling the run's ctx from inside the body still closes every
// adopted session that has a native id, and does so with a ctx that is not
// already cancelled.
func TestCancelledRunStillClosesEverySessionOnAFreshContext(t *testing.T) {
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		return "ok", nil
	}}
	var mu sync.Mutex
	var closed []string
	var ctxErrs []error
	f.onClose = func(ctx context.Context, session string) {
		mu.Lock()
		defer mu.Unlock()
		closed = append(closed, session)
		ctxErrs = append(ctxErrs, ctx.Err())
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := Run(Project(ctx, t.TempDir()), "cancelled", func(ctx context.Context) error {
		if _, err := NewSession(ctx, "one", f, "m", "/w").Generate[Text](ctx, "hi"); err != nil {
			return err
		}
		if _, err := NewSession(ctx, "two", f, "m", "/w").Generate[Text](ctx, "hi"); err != nil {
			return err
		}
		cancel() // cancel the run's own ctx from inside the body
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(closed) != 2 {
		t.Fatalf("closed sessions = %v, want both", closed)
	}
	for i, err := range ctxErrs {
		if err != nil {
			t.Errorf("Close for %s ran with an already-cancelled ctx: %v", closed[i], err)
		}
	}
}

// TestUnusedSessionClosesWithoutAnAdapterCall covers acceptance item 3: a
// session that never ran a turn has no native id, so it produces a plain
// SessionClosed{} and no Close call at all.
func TestUnusedSessionClosesWithoutAnAdapterCall(t *testing.T) {
	f := &fake{}
	f.onClose = func(ctx context.Context, session string) {
		t.Errorf("Close was called for a session with no native id: %s", session)
	}
	project := t.TempDir()
	err := Run(Project(t.Context(), project), "unused", func(ctx context.Context) error {
		NewSession(ctx, "idle", f, "m", "/w")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, globErr := filepath.Glob(filepath.Join(project, "runs", "*", "run.jsonl"))
	if globErr != nil || len(runs) != 1 {
		t.Fatalf("run logs = %v, %v", runs, globErr)
	}
	records := readRecords[LifecycleRecord](t, runs[0])
	found := false
	for _, record := range records {
		if closed, ok := record.Event.(SessionClosed); ok {
			found = true
			if closed.Error != "" {
				t.Errorf("SessionClosed.Error = %q, want empty", closed.Error)
			}
		}
	}
	if !found {
		t.Fatal("no SessionClosed event was recorded for the unused session")
	}
}

// TestCloseFailureDoesNotAffectRecordingError covers acceptance item 5:
// Complete.RecordingError, which reports a persistence failure, is
// unaffected by a HarnessAdapter.Close failure.
func TestCloseFailureDoesNotAffectRecordingError(t *testing.T) {
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		return "ok", nil
	}}
	f.closeErr = func(string) error { return errors.New("boom") }
	project := t.TempDir()
	err := Run(Project(t.Context(), project), "recording", func(ctx context.Context) error {
		_, err := NewSession(ctx, "one", f, "m", "/w").Generate[Text](ctx, "hi")
		return err
	})
	var closeErr *CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Run error = %v, want *CloseError", err)
	}

	runs, globErr := filepath.Glob(filepath.Join(project, "runs", "*", "run.jsonl"))
	if globErr != nil || len(runs) != 1 {
		t.Fatalf("run logs = %v, %v", runs, globErr)
	}
	records := readRecords[LifecycleRecord](t, runs[0])
	var complete Complete
	for _, record := range records {
		if event, ok := record.Event.(Complete); ok {
			complete = event
		}
	}
	if complete.RecordingError != "" {
		t.Fatalf("Complete.RecordingError = %q, want empty: a Close failure is not a recording error", complete.RecordingError)
	}
}
