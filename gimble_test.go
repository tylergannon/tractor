package gimble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylergannon/gimble/internal/observation"
	"github.com/tylergannon/gimble/internal/runlog"
	"github.com/tylergannon/polytype"
	"golang.org/x/sync/errgroup"
)

// fake is a HarnessAdapter whose turns are answered by a function.
type fake struct {
	answer func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error)
	// closeErr, when set, is called for every Close and its result returned.
	// A nil closeErr makes every Close succeed.
	closeErr func(session string) error
	// onClose, when set, is called with the ctx and session id of every
	// Close call, before closeErr; it lets a test inspect that ctx (for
	// example, that it is not already cancelled).
	onClose func(ctx context.Context, session string)
	report  map[string]Usage // the harness's own turn report, when this fake states one

	mu      sync.Mutex
	made    int
	steers  []string
	running map[string]func(AgentEvent) error
	closed  []string
}

func (f *fake) CreateSession(ctx context.Context, model, workdir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made++
	return "native-" + string(rune('0'+f.made)), nil
}

func (f *fake) RunTurn(ctx context.Context, session, prompt string, schema json.RawMessage, onEvent func(AgentEvent) error) (TurnResult, error) {
	f.mu.Lock()
	if f.running == nil {
		f.running = map[string]func(AgentEvent) error{}
	}
	f.running[session] = onEvent
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.running, session)
		f.mu.Unlock()
	}()
	out, err := f.answer(ctx, session, prompt, schema, onEvent)
	if err != nil {
		return TurnResult{}, err
	}
	result := TurnResult{Output: json.RawMessage(out), Usage: f.report}
	if len(schema) == 0 {
		result.Output, err = json.Marshal(out)
	}
	return result, err
}

func (f *fake) Steer(ctx context.Context, session, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if emit := f.running[session]; emit != nil {
		f.steers = append(f.steers, message)
	}
	return nil
}

func (f *fake) Fork(ctx context.Context, session string) (string, error) {
	return session + "-fork", nil
}

func (f *fake) Close(ctx context.Context, session string) error {
	f.mu.Lock()
	f.closed = append(f.closed, session)
	fn := f.closeErr
	hook := f.onClose
	f.mu.Unlock()
	if hook != nil {
		hook(ctx, session)
	}
	if fn != nil {
		return fn(session)
	}
	return nil
}

func runTest(t *testing.T, body func(ctx context.Context) error) error {
	t.Helper()
	return Run(Project(t.Context(), t.TempDir()), "test", body)
}

func lifecycleKind(event LifecycleEvent) string {
	switch event.(type) {
	case RunStarted:
		return "run_started"
	case RunEnded:
		return "run_ended"
	case RunCancelled:
		return "run_cancelled"
	case ScopeBegan:
		return "scope_began"
	case ScopeEnded:
		return "scope_ended"
	case PlannerDecision:
		return "planner_decision"
	case ValueSet:
		return "value_set"
	case SessionCreated:
		return "session_created"
	case SessionClosed:
		return "session_closed"
	case TurnStarted:
		return "turn_started"
	case TurnEnded:
		return "turn_ended"
	case SuperviseAttached:
		return "supervise_attached"
	case Steer:
		return "steer"
	case Interrupt:
		return "interrupt"
	case Complete:
		return "complete"
	default:
		return "unknown"
	}
}

func agentKind(event AgentEvent) string {
	return event.Type
}

func fakeAgentEvent(eventType, session, message string, fields map[string]any) AgentEvent {
	data := map[string]any{"sessionID": session, "assistantMessageID": message}
	for key, value := range fields {
		data[key] = value
	}
	return nativeEvent(eventType, data, map[string]any{"provider": "fake"})
}

func TestRunLogCanBeRead(t *testing.T) {
	project := t.TempDir()
	var dir string
	if err := Run(Project(t.Context(), project), "reader", func(ctx context.Context) error {
		dir = runDir(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("run directory was empty inside a run")
	}
	if runDir(t.Context()) != "" {
		t.Fatal("run directory was present outside a run")
	}
	var got []string
	if err := runlog.Read[LifecycleRecord](t.Context(), dir, func(e LifecycleRecord) error {
		got = append(got, lifecycleKind(e.Event))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[len(got)-1] != "complete" {
		t.Fatalf("read events = %v", got)
	}

	liveDir := t.TempDir()
	w, err := newEventWriter(filepath.Join(liveDir, "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	var readers errgroup.Group
	defer func() { cancel(); _ = readers.Wait() }()
	var seen []string
	readers.Go(func() error {
		return runlog.Read[LifecycleRecord](ctx, liveDir, func(e LifecycleRecord) error {
			seen = append(seen, lifecycleKind(e.Event))
			return nil
		})
	})
	if _, err := w.writeLifecycle("", "", "", RunStarted{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.writeLifecycle("", "", "", Complete{}); err != nil {
		t.Fatal(err)
	}
	if err := readers.Wait(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"run_started", "complete"}) {
		t.Fatalf("live events = %q", seen)
	}
}

func TestScopeData(t *testing.T) {
	err := runTest(t, func(ctx context.Context) error {
		if err := Set(ctx, "language", "go"); err != nil {
			return err
		}
		if err := Set(ctx, "language", "rust"); err == nil {
			t.Error("a second Set of a key in one scope succeeded")
		}
		if err := SetJSON(ctx, "review", review{Objections: []string{"too big"}}); err != nil {
			return err
		}
		var inner context.Context
		err := Scope(ctx, "lap", func(ctx context.Context) error {
			inner = ctx
			return Set(ctx, "language", "zig")
		})
		if err != nil {
			return err
		}
		got := ScopeText(inner)
		want := "## review\n\n{\n  \"objections\": [\n    \"too big\"\n  ]\n}\n\n## language\n\nzig"
		if got != want {
			t.Errorf("ScopeText = %q, want %q", got, want)
		}
		if err := Set(inner, "late", "x"); err == nil {
			t.Error("Set on an ended scope succeeded")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerate(t *testing.T) {
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		switch {
		case len(schema) == 0:
			return "hello", nil
		case prompt == "bad":
			return `{"objections": "not a list"}`, nil
		default:
			return `{"objections": ["one"]}`, nil
		}
	}}
	var escaped *Session
	err := runTest(t, func(ctx context.Context) error {
		s := NewSession(ctx, "coder", f, "m", "/w")
		text, err := s.Generate[Text](ctx, "hi")
		if err != nil || text != "hello" {
			t.Errorf("Generate[Text] = %q, %v", text, err)
		}
		gotReview, err := s.Generate[review](ctx, "review")
		if err != nil || len(gotReview.Objections) != 1 {
			t.Errorf("Generate[review] = %v, %v", gotReview, err)
		}
		if _, err := s.Generate[review](ctx, "bad"); err == nil {
			t.Error("a result that does not validate was accepted")
		}
		if s.id != "coder.1" || NewSession(ctx, "coder", f, "m", "/w").id != "coder.2" {
			t.Errorf("session ids: %q", s.id)
		}
		return Scope(ctx, "lap", func(ctx context.Context) error {
			escaped = NewSession(ctx, "coder", f, "m", "/w")
			if escaped.id != "lap.1/coder.1" {
				t.Errorf("id = %q", escaped.id)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Generate[Text](t.Context(), "zombie"); err == nil {
		t.Error("a session whose scope ended ran a turn")
	}
}

func TestGroupFirstErrorCancelsTheRest(t *testing.T) {
	boom := errors.New("boom")
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		if prompt == "fail" {
			return "", boom
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	var slow error
	err := runTest(t, func(ctx context.Context) error {
		g := Group(ctx, "bakeoff")
		g.Go("attempt", func(ctx context.Context) error {
			_, slow = NewSession(ctx, "candidate", f, "m", "/w").Generate[Text](ctx, "wait")
			return nil
		})
		g.Go("attempt", func(ctx context.Context) error {
			_, err := NewSession(ctx, "candidate", f, "m", "/w").Generate[Text](ctx, "fail")
			return err
		})
		return g.Wait()
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Wait = %v, want boom", err)
	}
	if !errors.Is(slow, context.Canceled) {
		t.Fatalf("the other turn ended with %v, want context.Canceled", slow)
	}
}

func TestFork(t *testing.T) {
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		return session, nil
	}}
	err := runTest(t, func(ctx context.Context) error {
		researcher := NewSession(ctx, "researcher", f, "m", "/w")
		if _, err := researcher.Generate[Text](ctx, "prime"); err != nil {
			return err
		}
		judge, err := researcher.Fork(ctx, "judge")
		if err != nil {
			return err
		}
		native, err := judge.Generate[Text](ctx, "who")
		if native != "native-1-fork" || judge.id != "judge.1" || judge.workdir != "/w" {
			t.Errorf("fork ran on %q as %q in %q", native, judge.id, judge.workdir)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSupervise(t *testing.T) {
	var workerTurns, looks, toolResults int
	f := &fake{}
	f.answer = func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		if session == "native-1" { // the worker, which runs until the supervisor's steer lands
			workerTurns++
			emit(fakeAgentEvent("session.tool.called", session, "message-1", map[string]any{"id": "1", "input": map[string]any{"command": "make plugins"}, "executed": true}))
			emit(fakeAgentEvent("session.tool.success", session, "message-1", map[string]any{"id": "1", "content": []any{map[string]any{"type": "text", "text": "built a plugin system"}}, "executed": true}))
			for range 200 {
				f.mu.Lock()
				n := len(f.steers)
				f.mu.Unlock()
				if n > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			return "done", nil
		}
		f.mu.Lock() // the supervisor
		looks++
		toolResults += strings.Count(prompt, "[tool result]")
		f.mu.Unlock()
		if strings.Contains(prompt, "no plugin systems") && strings.Contains(prompt, "built a plugin system") {
			return `{"objections": ["remove the plugin system"]}`, nil
		}
		return `{"objections": []}`, nil
	}
	err := runTest(t, func(ctx context.Context) error {
		worker := NewSession(ctx, "coder", f, "m", "/w")
		supervisor := NewSession(ctx, "taste", f, "m", "/w")
		res, err := worker.Generate[Text](ctx, "build it", WithSupervisor(supervisor, "no plugin systems", WithInterval(10*time.Millisecond)))
		if res != "done" {
			t.Errorf("result %q", res)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.steers) == 0 || !strings.Contains(f.steers[0], "remove the plugin system") {
		t.Errorf("steers: %q", f.steers)
	}
	// A steer is a new event and can cause another look before the worker exits.
	if workerTurns != 1 || toolResults != 1 {
		t.Errorf("worker turns=%d, looks=%d, tool results seen=%d; want one worker turn and its tool result seen once", workerTurns, looks, toolResults)
	}
}

func TestSuperviseASupervisor(t *testing.T) {
	f := &fake{}
	steered := func(text string) bool {
		for range 400 {
			f.mu.Lock()
			found := slices.ContainsFunc(f.steers, func(s string) bool { return strings.Contains(s, text) })
			f.mu.Unlock()
			if found {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	f.answer = func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		switch {
		case session == "native-1": // the worker, until its supervisor objects
			emit(fakeAgentEvent("session.tool.success", session, "message-1", map[string]any{"id": "1", "content": []any{map[string]any{"type": "text", "text": "built a plugin system"}}, "executed": true}))
			steered("remove the plugin system")
			return "done", nil
		case session == "native-2" && strings.Contains(prompt, "no plugin systems"): // the supervisor's first look, until its own supervisor objects
			emit(fakeAgentEvent("session.text.ended", session, "message-1", map[string]any{"ordinal": 0, "text": "I object to the variable names"}))
			if !steered("object only to plugin systems") {
				return `{"objections": []}`, nil
			}
			return `{"objections": ["remove the plugin system"]}`, nil
		case session == "native-3" && strings.Contains(prompt, "no nitpicking"): // the supervisor's supervisor
			return `{"objections": ["object only to plugin systems"]}`, nil
		}
		return `{"objections": []}`, nil
	}
	err := runTest(t, func(ctx context.Context) error {
		worker := NewSession(ctx, "coder", f, "m", "/w")
		supervisor := NewSession(ctx, "taste", f, "m", "/w")
		lead := NewSession(ctx, "lead", f, "m", "/w")
		_, err := worker.Generate[Text](ctx, "build it", WithSupervisor(supervisor, "no plugin systems",
			WithInterval(10*time.Millisecond),
			WithSupervisor(lead, "no nitpicking", WithInterval(10*time.Millisecond)),
		))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.steers) < 2 || !strings.Contains(f.steers[0], "object only to plugin systems") || !strings.Contains(f.steers[1], "remove the plugin system") {
		t.Errorf("steers: %q, want the lead's steer to the supervisor, then the supervisor's to the worker", f.steers)
	}
}

func TestSuperviseCancelsAndJoinsLook(t *testing.T) {
	workerFailure := errors.New("worker failed")
	for _, workerErr := range []error{nil, workerFailure} {
		name := "success"
		if workerErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			looking := make(chan struct{}) // the worker finishes only after a look starts
			joined := false
			f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
				if prompt == "work" {
					select {
					case <-looking:
						return "done", workerErr
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
				close(looking)
				<-ctx.Done()
				joined = true
				return "", ctx.Err()
			}}
			err := Run(Project(ctx, t.TempDir()), "join", func(ctx context.Context) error {
				worker := NewSession(ctx, "worker", f, "fake", ".")
				reviewer := NewSession(ctx, "reviewer", f, "fake", ".")
				result, err := worker.Generate[Text](ctx, "work", WithSupervisor(reviewer, "watch", WithInterval(time.Millisecond)))
				if !joined {
					t.Error("Generate returned before the supervisor exited")
				}
				if workerErr == nil && result != "done" {
					t.Errorf("worker result = %q", result)
				}
				return err
			})
			if !errors.Is(err, workerErr) {
				t.Errorf("Run error = %v, want worker error %v", err, workerErr)
			}
			if ctx.Err() != nil {
				t.Fatal("supervisor stopped only when the parent's deadline expired")
			}
		})
	}
}

func TestLoopCarriesStructuredTaskAndFeedback(t *testing.T) {
	task := Task{Name: "Repair build", Description: "Restore the package after the observed compiler failure.", DefinitionOfDone: "The package builds and the failed check passes."}
	task.Validation.Command = "go test ./..."
	var prompts []string
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			raw, err := json.Marshal(plan{Tasks: []Task{task}, Next: polytype.Nullable[int]{Present: true, Value: 0}})
			return string(raw), err
		}
		raw, err := json.Marshal(plan{Tasks: []Task{}, Next: polytype.Nullable[int]{}})
		return string(raw), err
	}}
	project := t.TempDir()
	var tasks []Task
	var taskKeys []string
	var parentText string
	err := Run(Project(t.Context(), project), "test", func(ctx context.Context) error {
		if err := Set(ctx, "constraint", "keep the public API small"); err != nil {
			return err
		}
		planner := NewSession(ctx, "planner", f, "m", t.TempDir())
		loop := Loop(ctx, "sprint", "ship", planner)
		for ctx, task := range loop.Tasks {
			tasks = append(tasks, task)
			s, _ := current(ctx)
			taskKeys = append(taskKeys, s.key)
			if err := Set(ctx, "validation result", "exit 1: package does not compile"); err != nil {
				return err
			}
		}
		parentText = ScopeText(ctx)
		return loop.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0] != task || taskKeys[0] != "sprint.1/task.1" {
		t.Fatalf("tasks %v in %v", tasks, taskKeys)
	}
	if !strings.Contains(prompts[0], "keep the public API small") || !strings.Contains(prompts[1], "Previous task record") || !strings.Contains(prompts[1], "exit 1: package does not compile") {
		t.Errorf("planner prompts:\n%s\n---\n%s", prompts[0], prompts[1])
	}
	if strings.Contains(parentText, task.Name) || strings.Contains(parentText, "package does not compile") {
		t.Fatalf("task values were promoted to the parent:\n%s", parentText)
	}

	runs, err := filepath.Glob(filepath.Join(project, "runs", "*", "run.jsonl"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("run logs = %v, %v", runs, err)
	}
	records := readRecords[LifecycleRecord](t, runs[0])
	var decided, began Task
	for _, record := range records {
		switch event := record.Event.(type) {
		case PlannerDecision:
			if event.Task.Present {
				decided = event.Task.Value
			}
		case ScopeBegan:
			if event.Task.Present {
				began = event.Task.Value
			}
		}
	}
	if decided != task || began != task {
		t.Fatalf("durable task changed: decision=%+v scope=%+v", decided, began)
	}

	backlogFile := filepath.Join(filepath.Dir(runs[0]), "scopes", "sprint.1", "backlog.md")
	raw, err := os.ReadFile(backlogFile)
	if err != nil {
		t.Fatalf("backlog.md: %v", err)
	}
	wantBacklog, err := backlogJSON("ship", []Task{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "---\n" + string(wantBacklog) + "\n---\n"; string(raw) != want {
		t.Fatalf("backlog.md = %q, want %q", raw, want)
	}
}

func TestLoopRejectsInconsistentPlannerData(t *testing.T) {
	task := Task{Name: "Build", Description: "Make it build.", DefinitionOfDone: "It builds."}
	for _, test := range []struct {
		name   string
		plan   plan
		needle string
	}{
		{
			name:   "blank description",
			plan:   plan{Tasks: []Task{{Name: "Build", DefinitionOfDone: "It builds."}}, Next: polytype.Nullable[int]{Present: true, Value: 0}},
			needle: "description is blank",
		},
		{
			name:   "next out of range",
			plan:   plan{Tasks: []Task{task}, Next: polytype.Nullable[int]{Present: true, Value: 3}},
			needle: "out of range",
		},
		{
			name:   "duplicate names",
			plan:   plan{Tasks: []Task{task, task}, Next: polytype.Nullable[int]{Present: true, Value: 0}},
			needle: "duplicate task name",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
				calls++
				raw, err := json.Marshal(test.plan)
				return string(raw), err
			}}
			err := runTest(t, func(ctx context.Context) error {
				planner := NewSession(ctx, "planner", f, "m", t.TempDir())
				loop := Loop(ctx, "sprint", "ship", planner)
				for range loop.Tasks {
					t.Fatal("invalid task was yielded")
				}
				return loop.Err()
			})
			if err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("error = %v, want %q", err, test.needle)
			}
			if calls != 1 {
				t.Fatalf("planner calls = %d, want 1", calls)
			}
		})
	}
}

func TestLoopEndsTaskScopeOnBreak(t *testing.T) {
	task := Task{Name: "Inspect", Description: "Establish the current behavior.", DefinitionOfDone: "The behavior is recorded."}
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		raw, err := json.Marshal(plan{Tasks: []Task{task}, Next: polytype.Nullable[int]{Present: true, Value: 0}})
		return string(raw), err
	}}
	var taskCtx context.Context
	var worker *Session
	err := runTest(t, func(ctx context.Context) error {
		planner := NewSession(ctx, "planner", f, "m", t.TempDir())
		loop := Loop(ctx, "work", "inspect", planner)
		for ctx := range loop.Tasks {
			taskCtx = ctx
			worker = NewSession(ctx, "worker", f, "m", t.TempDir())
			break
		}
		if err := loop.Err(); err != nil {
			return err
		}
		if !errors.Is(taskCtx.Err(), context.Canceled) {
			t.Fatalf("task context error = %v, want canceled", taskCtx.Err())
		}
		if _, err := worker.Generate[Text](ctx, "too late"); err == nil || !strings.Contains(err.Error(), "scope ended") {
			t.Fatalf("task-owned session remained usable: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoopEndsTaskScopeOnCancellation(t *testing.T) {
	task := Task{Name: "Wait", Description: "Observe cancellation while work is active.", DefinitionOfDone: "The active task stops with its parent."}
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		raw, err := json.Marshal(plan{Tasks: []Task{task}, Next: polytype.Nullable[int]{Present: true, Value: 0}})
		return string(raw), err
	}}
	ctx, cancel := context.WithCancel(t.Context())
	var taskCtx context.Context
	var worker *Session
	err := Run(Project(ctx, t.TempDir()), "test", func(ctx context.Context) error {
		planner := NewSession(ctx, "planner", f, "m", t.TempDir())
		loop := Loop(ctx, "work", "wait", planner)
		for ctx := range loop.Tasks {
			taskCtx = ctx
			worker = NewSession(ctx, "worker", f, "m", t.TempDir())
			cancel()
			<-ctx.Done()
			break
		}
		return loop.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(taskCtx.Err(), context.Canceled) {
		t.Fatalf("task context error = %v, want canceled", taskCtx.Err())
	}
	if _, err := worker.Generate[Text](taskCtx, "too late"); err == nil || !strings.Contains(err.Error(), "scope ended") {
		t.Fatalf("task-owned session remained usable: %v", err)
	}
}

func TestAttestEventFixture(t *testing.T) {
	project := t.TempDir()
	looking := make(chan struct{})
	f := &fake{}
	f.answer = func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		emit(fakeAgentEvent("session.text.ended", session, "message-1", map[string]any{"ordinal": 0, "text": "working"}))
		if len(schema) != 0 {
			close(looking)
			<-ctx.Done() // keep the look active until the worker finishes
			return "", ctx.Err()
		}
		if prompt == "build" {
			emit(fakeAgentEvent("session.tool.called", session, "message-1", map[string]any{"id": "call-1", "input": map[string]any{"command": "test"}, "executed": true}))
			emit(fakeAgentEvent("session.tool.success", session, "message-1", map[string]any{"id": "call-1", "content": []any{map[string]any{"type": "text", "text": "ok"}}, "executed": true}))
			for {
				f.mu.Lock()
				steered := len(f.steers) > 0
				f.mu.Unlock()
				if steered || ctx.Err() != nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		return "done", nil
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	runsGroup, runCtx := errgroup.WithContext(ctx)
	defer func() { cancel(); _ = runsGroup.Wait() }()
	runsGroup.Go(func() error {
		return Run(Project(runCtx, project), "attest", func(ctx context.Context) error {
			if runDir(ctx) == "" {
				return errors.New("run directory was empty inside a run")
			}
			if err := Set(ctx, "root", "value"); err != nil {
				return err
			}
			researcher := NewSession(ctx, "researcher", f, "model", project)
			if _, err := researcher.Generate[Text](ctx, "prime"); err != nil {
				return err
			}
			fork, err := researcher.Fork(ctx, "planner")
			if err != nil {
				return err
			}
			worker := NewSession(ctx, "worker", f, "model", project)
			reviewer := NewSession(ctx, "reviewer", f, "review", project)
			turns := Group(ctx, "build")
			turns.Go("worker", func(ctx context.Context) error {
				_, err := worker.Generate[Text](ctx, "build", WithSupervisor(reviewer, "watch", WithInterval(time.Millisecond)))
				return err
			})
			turns.Go("steer", func(ctx context.Context) error {
				select {
				case <-looking:
					return worker.Steer(withSteerSource(ctx, reviewer.id), "continue")
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err := turns.Wait(); err != nil {
				return err
			}
			if err := worker.Steer(ctx, "too late"); err != nil {
				return err
			}
			if _, err := fork.Generate[Text](ctx, "plan"); err != nil {
				return err
			}
			return Scope(ctx, "nested", func(ctx context.Context) error {
				return Set(ctx, "child", "value")
			})
		})
	})

	runs := filepath.Join(project, "runs")
	var runDir string
	for range 1000 {
		entries, _ := os.ReadDir(runs)
		if len(entries) == 1 {
			runDir = filepath.Join(runs, entries[0].Name())
			break
		}
		time.Sleep(time.Millisecond)
	}
	if runDir == "" {
		t.Fatal("run directory was not created")
	}
	var tailed []LifecycleRecord
	// Observation outlives the run context so it can consume the final record.
	readCtx, stopReading := context.WithTimeout(ctx, time.Second)
	defer stopReading()
	readErr := runlog.Read[LifecycleRecord](readCtx, runDir, func(e LifecycleRecord) error {
		tailed = append(tailed, e)
		return nil
	})
	if err := runsGroup.Wait(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}

	if len(tailed) < 2 || lifecycleKind(tailed[0].Event) != "run_started" || lifecycleKind(tailed[len(tailed)-1].Event) != "complete" {
		t.Fatalf("tail did not replay through completion: %v", tailed)
	}
	seq := uint64(0)
	seen := map[string]bool{}
	intervals := map[string][2]time.Time{}
	scopes := map[string]bool{}
	var sessionIDs, turnIDs []string
	for _, e := range tailed {
		if e.Seq <= seq || e.Time.IsZero() {
			t.Fatalf("invalid event ordering: %+v", e)
		}
		seq = e.Seq
		kind := lifecycleKind(e.Event)
		seen[kind] = true
		switch e.Event.(type) {
		case ScopeBegan:
			scopes[e.Scope] = true
			intervals[e.Scope] = [2]time.Time{e.Time}
		case ScopeEnded:
			pair := intervals[e.Scope]
			pair[1] = e.Time
			intervals[e.Scope] = pair
		case SessionCreated:
			sessionIDs = append(sessionIDs, e.Session.Value)
		case TurnStarted:
			turnIDs = append(turnIDs, e.Turn.Value)
		}
	}
	for _, e := range tailed {
		if e.Scope == "" {
			continue
		}
		contained := false
		for scope := range scopes {
			if e.Scope == scope || strings.HasPrefix(e.Scope, scope+"/") {
				contained = true
				break
			}
		}
		if !contained {
			t.Fatalf("event %q has scope %q outside the began-scope prefix tree", lifecycleKind(e.Event), e.Scope)
		}
	}
	for scope, pair := range intervals {
		if pair[1].IsZero() || !pair[0].Before(pair[1]) {
			t.Fatalf("scope %q lacks a valid began/ended interval: %v", scope, pair)
		}
	}
	if len(sessionIDs) < 4 || !slices.Contains(sessionIDs, "researcher.1") || !slices.Contains(sessionIDs, "planner.1") || !slices.Contains(sessionIDs, "worker.1") || !slices.Contains(sessionIDs, "reviewer.1") {
		t.Fatalf("ordinal session ids = %v", sessionIDs)
	}
	for _, id := range turnIDs {
		if !strings.Contains(id, "/turn.") {
			t.Fatalf("turn id lacks ordinal = %q", id)
		}
	}
	for _, kind := range []string{"scope_began", "scope_ended", "value_set", "session_created", "session_closed", "turn_started", "turn_ended", "supervise_attached", "steer"} {
		if !seen[kind] {
			t.Errorf("run log lacks %s", kind)
		}
	}
	if !seen["complete"] {
		t.Error("run log lacks completion event")
	}
	if got := tailed[0].Scope; got != "" {
		t.Errorf("run_started scope = %q, want root scope", got)
	}
	for _, want := range []string{"nested.1", ""} {
		if !slices.ContainsFunc(tailed, func(e LifecycleRecord) bool {
			_, began := e.Event.(ScopeBegan)
			return began && e.Scope == want
		}) {
			t.Errorf("scope tree lacks began event for %q", want)
		}
	}
	for _, e := range tailed {
		if value, ok := e.Event.(ValueSet); ok {
			if value.Key == "root" && value.Value != JSONText(`"value"`) {
				t.Errorf("root set value = %s, want JSON string", value.Value)
			}
			if value.Key == "child" && e.Scope != "nested.1" {
				t.Errorf("child set scope = %q, want nested.1", e.Scope)
			}
		}
	}
	var forked, supervised, landed, dropped bool
	var workerTurn, reviewerTurn [2]time.Time
	for _, e := range tailed {
		switch event := e.Event.(type) {
		case SessionCreated:
			if e.Session.Value == "planner.1" && event.Parent == "researcher.1" {
				forked = true
			}
		case SuperviseAttached:
			if event.Reviewer == "reviewer.1" && strings.HasPrefix(event.Worker, "worker.1/turn.") {
				supervised = true
			}
		case Steer:
			if event.Source == "reviewer.1" && event.Landed {
				landed = true
			}
			if event.Message == "too late" && !event.Landed {
				dropped = true
			}
		case TurnStarted:
			if strings.HasPrefix(e.Turn.Value, "worker.1/turn.") {
				workerTurn[0] = e.Time
			}
			if e.Session.Value == "reviewer.1" && reviewerTurn[0].IsZero() {
				reviewerTurn[0] = e.Time
			}
		case TurnEnded:
			if strings.HasPrefix(e.Turn.Value, "worker.1/turn.") {
				workerTurn[1] = e.Time
			}
			if e.Session.Value == "reviewer.1" && reviewerTurn[1].IsZero() {
				reviewerTurn[1] = e.Time
			}
		}
	}
	if !forked || !supervised || !landed || !dropped {
		t.Fatalf("relations: fork=%v supervise=%v landed=%v dropped=%v", forked, supervised, landed, dropped)
	}
	if workerTurn[0].IsZero() || workerTurn[1].IsZero() || reviewerTurn[0].IsZero() || !workerTurn[0].Before(reviewerTurn[0]) || !reviewerTurn[0].Before(workerTurn[1]) {
		t.Fatalf("worker/reviewer turns did not overlap: worker=%v reviewer=%v", workerTurn, reviewerTurn)
	}
	persisted := readRecords[LifecycleRecord](t, filepath.Join(runDir, "run.jsonl"))
	if len(persisted) != len(tailed) {
		t.Fatalf("persisted lifecycle records = %d, tailed %d", len(persisted), len(tailed))
	}

	projectEvents := readRecords[LifecycleRecord](t, filepath.Join(project, "project.jsonl"))
	if len(projectEvents) != 2 || lifecycleKind(projectEvents[0].Event) != "run_started" || lifecycleKind(projectEvents[1].Event) != "run_ended" {
		t.Fatalf("project lifecycle: %+v", projectEvents)
	}
	files, _ := filepath.Glob(filepath.Join(runDir, "sessions", "*.jsonl"))
	if len(files) < 3 {
		t.Fatalf("session transcripts = %d, want at least 3", len(files))
	}
	transcriptKinds := map[string]map[string]bool{}
	for _, file := range files {
		events := readRecords[AgentRecord](t, file)
		if len(events) == 0 || agentKind(events[0].Event) != "session.inbox.enqueued" {
			t.Errorf("%s is not a transcript: %+v", file, events)
		}
		seenKinds := map[string]bool{}
		for _, e := range events {
			if e.Seq == 0 || e.Time.IsZero() || e.Session == "" || e.Turn == "" {
				t.Errorf("%s has incomplete agent event placement: %+v", file, e)
			}
			seenKinds[agentKind(e.Event)] = true
		}
		transcriptKinds[filepath.Base(file)] = seenKinds
	}
	if !slices.ContainsFunc(files, func(file string) bool {
		kinds := transcriptKinds[filepath.Base(file)]
		return kinds["session.tool.called"] && kinds["session.tool.success"]
	}) {
		t.Error("no session transcript contains the tool call/result pair")
	}
}

func readRecords[T any](t *testing.T, file string) []T {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var records []T
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var e T
		if validator, ok := any(e).(interface{ ValidateJSON([]byte) error }); ok {
			if err := validator.ValidateJSON([]byte(line)); err != nil {
				t.Fatalf("validate %s: %v\n%s", file, err, line)
			}
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode %s: %v", file, err)
		}
		records = append(records, e)
	}
	return records
}

// TestCancelledRunStaysCancelled replays the shared fixture through the
// runtime's own fold. The browser reducer reads the same file.
func TestCancelledRunStaysCancelled(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("internal", "observation", "testdata", "cancelled-run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store := observation.Open(nil, "run-1", "cancelled", t.TempDir())
	defer func() { _ = store.Close() }()
	r := &run{store: store}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record LifecycleRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		r.observeLifecycle(record.Scope, record.Session.Value, record.Turn.Value, record.Event, append(json.RawMessage(nil), line...))
	}
	if got := store.Snapshot().Run; got.Status != observation.StatusCancelled {
		t.Fatalf("status = %q (error %q), want cancelled", got.Status, got.Error)
	}
}

// TestTurnRecordsValidationFailure is issue 144 item 1: a result the typed
// output rejects is the turn's own outcome, not a clean turn beside a failing
// scope.
func TestTurnRecordsValidationFailure(t *testing.T) {
	f := &fake{answer: func(ctx context.Context, session, prompt string, schema json.RawMessage, emit func(AgentEvent) error) (string, error) {
		return `{"objections": "not a list"}`, nil
	}}
	var dir string
	if err := Run(Project(t.Context(), t.TempDir()), "validate", func(ctx context.Context) error {
		dir = runDir(ctx)
		s := NewSession(ctx, "coder", f, "m", "/w")
		if _, err := s.Generate[review](ctx, "review"); err == nil {
			t.Error("a result that does not validate was accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var ended []TurnEnded
	if err := runlog.Read[LifecycleRecord](t.Context(), dir, func(e LifecycleRecord) error {
		if turn, ok := e.Event.(TurnEnded); ok {
			ended = append(ended, turn)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ended) != 1 {
		t.Fatalf("recorded %d turn_ended records, want 1", len(ended))
	}
	if !strings.Contains(ended[0].Error, "objections") || ended[0].Result == "" {
		t.Fatalf("turn_ended = %+v, want the validation failure and the result it rejected", ended[0])
	}
}
