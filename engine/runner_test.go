package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

func TestRunnerCompletesLinearGraphAndWritesFinalCheckpoint(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(
		startNode("start", "work"),
		customNode("work", "task", []graph.Edge{{To: "done"}}, 0),
		exitNode("done"),
	)
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(node graph.Node, offered []graph.Edge, scope ExecutionScope, got *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID != "work" || !reflect.DeepEqual(offered, []graph.Edge{{To: graph.Success}}) {
			t.Fatalf("handler input node=%q offered=%v", node.Base().ID, offered)
		}
		if scope.Workdir != "/workspace" || scope.Goal != "ship it" || scope.Stop == nil {
			t.Fatalf("scope = %#v", scope)
		}
		if got.Goal != pipeline.Goal || filepath.Base(scope.StageDir) != "000001-work" {
			t.Fatalf("graph goal=%q stage=%q", got.Goal, scope.StageDir)
		}
		return harness.Outcome{Notes: strings.Repeat("ø", 205)}, nil
	}))
	runner := newTestRunner(t, pipeline, registry, root, nil)

	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted || result.FailureReason != "" {
		t.Fatalf("result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "work" || checkpoint.NextNode != graph.Success || checkpoint.RetryVisit {
		t.Fatalf("final checkpoint position = %#v", checkpoint)
	}
	if !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"work"}) {
		t.Fatalf("completed nodes = %v", checkpoint.CompletedNodes)
	}
	if !reflect.DeepEqual(checkpoint.NodeVisits, map[string]int{"work": 1}) ||
		!reflect.DeepEqual(checkpoint.NodeAttempts, map[string]int{"work": 1}) {
		t.Fatalf("checkpoint counters = visits %v attempts %v", checkpoint.NodeVisits, checkpoint.NodeAttempts)
	}
	if checkpoint.Seq != 1 || checkpoint.LastStage != "work" || len([]rune(checkpoint.LastResponse)) != 200 {
		t.Fatalf("checkpoint summary = seq %d stage %q response length %d", checkpoint.Seq, checkpoint.LastStage, len([]rune(checkpoint.LastResponse)))
	}
	assertJSONFile(t, filepath.Join(root, "stages", "000001-work", "outcome.json"), harness.Outcome{Notes: strings.Repeat("ø", 205)})
}

func TestRunnerLoopsUntilTargetVisitBudgetIsExhausted(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(
		startNode("start", "a"),
		customNode("a", "task", []graph.Edge{{To: "b"}}, 2),
		customNode("b", "task", []graph.Edge{{To: "a"}, {To: "done"}}, 0),
		exitNode("done"),
	)
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(node graph.Node, offered []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID == "a" {
			return harness.Outcome{Notes: "to b"}, nil
		}
		if len(offered) == 2 {
			return harness.Outcome{Next: "a", Notes: "loop"}, nil
		}
		if !reflect.DeepEqual(offered, []graph.Edge{{To: graph.Success}}) {
			t.Fatalf("offered after budget exhaustion = %v", offered)
		}
		return harness.Outcome{Notes: "finish"}, nil
	}))

	result, err := newTestRunner(t, pipeline, registry, root, nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"a", "b", "a", "b"}) {
		t.Fatalf("completed nodes = %v", checkpoint.CompletedNodes)
	}
	if checkpoint.NodeVisits["a"] != 2 || checkpoint.NodeVisits["b"] != 2 {
		t.Fatalf("visits = %v", checkpoint.NodeVisits)
	}
}

func TestRunnerFailsWithoutDispatchWhenAllSuccessorsAreExhausted(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(
		startNode("start", "work"),
		customNode("work", "task", []graph.Edge{{To: "gate"}}, 1),
		customNode("gate", "task", []graph.Edge{{To: "work"}}, 0),
	)
	registry := NewRegistry()
	gateCalled := false
	registry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID == "gate" {
			gateCalled = true
		}
		return harness.Outcome{Notes: "ok"}, nil
	}))

	result, err := newTestRunner(t, pipeline, registry, root, nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunFailed || !strings.Contains(result.FailureReason, "every successor of gate") {
		t.Fatalf("result = %#v", result)
	}
	if gateCalled {
		t.Fatal("budget-exhausted node was dispatched")
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "work" || checkpoint.NextNode != "gate" || checkpoint.RetryVisit {
		t.Fatalf("checkpoint changed after pre-execution failure: %#v", checkpoint)
	}
	if _, err := os.Stat(filepath.Join(root, "stages", "000002-gate")); !os.IsNotExist(err) {
		t.Fatalf("gate stage exists: %v", err)
	}
}

func TestRunnerRoutingRules(t *testing.T) {
	tests := []struct {
		name       string
		edges      []graph.Edge
		outcome    harness.Outcome
		wantStatus RunStatus
		wantNext   string
		wantReason string
	}{
		{name: "single offered requires no choice", edges: []graph.Edge{{To: graph.Success}}, outcome: harness.Outcome{Notes: "only"}, wantStatus: RunCompleted},
		{name: "multiple offered follows choice", edges: []graph.Edge{{To: graph.Failure}, {To: graph.Success}}, outcome: harness.Outcome{Next: graph.Success, Notes: "picked"}, wantStatus: RunCompleted},
		{name: "multiple offered requires choice", edges: []graph.Edge{{To: graph.Failure}, {To: graph.Success}}, outcome: harness.Outcome{Notes: "missing"}, wantStatus: RunFailed, wantNext: "choose", wantReason: "no choice among 2"},
		{name: "choice must be offered", edges: []graph.Edge{{To: graph.Failure}, {To: graph.Success}}, outcome: harness.Outcome{Next: "elsewhere", Notes: "invalid"}, wantStatus: RunFailed, wantNext: "choose", wantReason: "unoffered successor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			nodes := []graph.Node{startNode("start", "choose"), customNode("choose", "task", test.edges, 0)}
			registry := NewRegistry()
			registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
				return test.outcome, nil
			}))
			result, err := newTestRunner(t, testGraph(nodes...), registry, root, nil).Run()
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus || !strings.Contains(result.FailureReason, test.wantReason) {
				t.Fatalf("result = %#v", result)
			}
			if test.wantNext != "" {
				checkpoint := mustCheckpoint(t, root)
				if checkpoint.CurrentNode != "choose" || checkpoint.NextNode != test.wantNext || !checkpoint.RetryVisit {
					t.Fatalf("routing failure checkpoint = %#v", checkpoint)
				}
				assertJSONFile(t, filepath.Join(root, "stages", "000001-choose", "outcome.json"), test.outcome)
			}
		})
	}
}

func TestRunnerUnknownHandlerIsPreExecutionFailure(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(startNode("start", "mystery"), customNode("mystery", "unregistered", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	result, err := newTestRunner(t, pipeline, NewRegistry(), root, nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunFailed || result.FailureReason != "unknown handler type: codergen" {
		t.Fatalf("result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "" || checkpoint.NextNode != "mystery" || checkpoint.RetryVisit {
		t.Fatalf("checkpoint changed after registry failure: %#v", checkpoint)
	}
	if _, err := os.Stat(filepath.Join(root, "stages", "000001-mystery")); !os.IsNotExist(err) {
		t.Fatalf("mystery stage exists: %v", err)
	}
}

func TestRunnerExecutionFailureWritesFailureCheckpointAndErrorArtifact(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(startNode("start", "work"), customNode("work", "task", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	registry := NewRegistry()
	wantError := &harness.Error{Category: harness.ErrorTerminal, Message: "broken"}
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{}, wantError
	}))
	backend := &fakeBackend{bindings: map[string]harness.ThreadBinding{"work": {Harness: "codex", SessionID: "thread-1", Workdir: "/workspace"}}}

	result, err := newTestRunner(t, pipeline, registry, root, backend).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunFailed || result.FailureReason != "broken" {
		t.Fatalf("result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "work" || checkpoint.NextNode != "work" || !checkpoint.RetryVisit {
		t.Fatalf("failure checkpoint position = %#v", checkpoint)
	}
	if len(checkpoint.CompletedNodes) != 0 || checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 1 {
		t.Fatalf("failure checkpoint state = %#v", checkpoint)
	}
	if !reflect.DeepEqual(checkpoint.Sessions, backend.bindings) {
		t.Fatalf("sessions = %v", checkpoint.Sessions)
	}
	assertJSONFile(t, filepath.Join(root, "stages", "000001-work", "error.json"), *wantError)
}

func TestRunnerRetriesRetryableErrorsWithFreshStagesAndOneVisit(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(startNode("start", "work"), customNode("work", "task", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	pipeline.Defaults.MaxRetries = jsonschema.Optional[int]{Present: true, Value: 2}
	registry := NewRegistry()
	calls := 0
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		calls++
		if calls == 2 {
			checkpoint := mustCheckpoint(t, root)
			if checkpoint.CurrentNode != "" || checkpoint.NextNode != "work" || checkpoint.Seq != 0 || checkpoint.NodeAttempts["work"] != 0 {
				t.Fatalf("checkpoint written between attempts: %#v", checkpoint)
			}
		}
		if calls < 3 {
			return harness.Outcome{}, &harness.Error{Category: harness.ErrorRetryable, Message: fmt.Sprintf("outage-%d", calls)}
		}
		return harness.Outcome{Notes: "recovered"}, nil
	}))
	runner := newTestRunner(t, pipeline, registry, root, nil)
	var delays []int
	runner.retryDelay = func(attempt int) time.Duration {
		delays = append(delays, attempt)
		return 0
	}

	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted || calls != 3 || !reflect.DeepEqual(delays, []int{1, 2}) {
		t.Fatalf("result=%#v calls=%d delays=%v", result, calls, delays)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 3 || checkpoint.Seq != 3 {
		t.Fatalf("retry counters = %#v", checkpoint)
	}
	assertJSONFile(t, filepath.Join(root, "stages", "000001-work", "error.json"), harness.Error{Category: harness.ErrorRetryable, Message: "outage-1"})
	assertJSONFile(t, filepath.Join(root, "stages", "000002-work", "error.json"), harness.Error{Category: harness.ErrorRetryable, Message: "outage-2"})
	assertJSONFile(t, filepath.Join(root, "stages", "000003-work", "outcome.json"), harness.Outcome{Notes: "recovered"})
	latest, err := os.Readlink(filepath.Join(root, "stages", "latest", "work"))
	if err != nil {
		t.Fatal(err)
	}
	if latest != filepath.Join("..", "000003-work") {
		t.Fatalf("latest work stage = %q", latest)
	}
}

func TestRunnerRetryBudgetAndNonRetryableCategories(t *testing.T) {
	t.Run("node budget overrides default", func(t *testing.T) {
		root := t.TempDir()
		work := customNode("work", "task", []graph.Edge{{To: "done"}}, 0)
		work.(*graph.CodergenNode).MaxRetries = jsonschema.Optional[int]{Present: true, Value: 1}
		pipeline := testGraph(startNode("start", "work"), work, exitNode("done"))
		pipeline.Defaults.MaxRetries = jsonschema.Optional[int]{Present: true, Value: 3}
		registry := NewRegistry()
		calls := 0
		registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
			calls++
			return harness.Outcome{}, &harness.Error{Category: harness.ErrorRetryable, Message: "still unavailable"}
		}))
		runner := newTestRunner(t, pipeline, registry, root, nil)
		runner.retryDelay = func(int) time.Duration { return 0 }
		result, err := runner.Run()
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != RunFailed || calls != 2 {
			t.Fatalf("result=%#v calls=%d", result, calls)
		}
		checkpoint := mustCheckpoint(t, root)
		if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 2 {
			t.Fatalf("counters = %#v", checkpoint)
		}
	})

	for _, category := range []harness.ErrorCategory{harness.ErrorTerminal, harness.ErrorInterrupted} {
		t.Run(string(category), func(t *testing.T) {
			root := t.TempDir()
			work := customNode("work", "task", []graph.Edge{{To: "done"}}, 0)
			work.(*graph.CodergenNode).MaxRetries = jsonschema.Optional[int]{Present: true, Value: 3}
			registry := NewRegistry()
			calls := 0
			registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
				calls++
				return harness.Outcome{}, &harness.Error{Category: category, Message: "stop"}
			}))
			runner := newTestRunner(t, testGraph(startNode("start", "work"), work, exitNode("done")), registry, root, nil)
			runner.retryDelay = func(int) time.Duration {
				t.Fatal("non-retryable error entered backoff")
				return 0
			}
			result, err := runner.Run()
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != RunFailed || calls != 1 {
				t.Fatalf("result=%#v calls=%d", result, calls)
			}
		})
	}
}

func TestExecuteWithRetryRejectsNegativeBudgetWithoutDispatch(t *testing.T) {
	work := customNode("work", "task", []graph.Edge{{To: "done"}}, 0)
	work.(*graph.CodergenNode).MaxRetries = jsonschema.Optional[int]{Present: true, Value: -1}
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		t.Fatal("handler dispatched with a negative retry budget")
		return harness.Outcome{}, nil
	}))
	root := t.TempDir()
	result, err := newTestRunner(t, testGraph(startNode("start", "work"), work, exitNode("done")), registry, root, nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunFailed || result.FailureReason != "max_retries must be nonnegative" {
		t.Fatalf("result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "" || checkpoint.NextNode != "work" || checkpoint.NodeVisits["work"] != 0 {
		t.Fatalf("checkpoint changed before dispatch: %#v", checkpoint)
	}
}

func TestRunnerStopDuringBackoffEndsWithoutAnotherAttempt(t *testing.T) {
	root := t.TempDir()
	work := customNode("work", "task", []graph.Edge{{To: "done"}}, 0)
	work.(*graph.CodergenNode).MaxRetries = jsonschema.Optional[int]{Present: true, Value: 2}
	registry := NewRegistry()
	firstAttempt := make(chan struct{})
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		close(firstAttempt)
		return harness.Outcome{}, &harness.Error{Category: harness.ErrorRetryable, Message: "temporary"}
	}))
	runner := newTestRunner(t, testGraph(startNode("start", "work"), work, exitNode("done")), registry, root, nil)
	runner.retryDelay = func(int) time.Duration { return time.Hour }
	type runResponse struct {
		result RunResult
		err    error
	}
	finished := make(chan runResponse, 1)
	go func() {
		result, err := runner.Run()
		finished <- runResponse{result, err}
	}()
	<-firstAttempt
	runner.Stop()
	select {
	case response := <-finished:
		if response.err != nil {
			t.Fatal(response.err)
		}
		if response.result.Status != RunFailed || response.result.FailureReason != "stopped by operator" {
			t.Fatalf("result = %#v", response.result)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel retry backoff")
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 1 || !checkpoint.RetryVisit {
		t.Fatalf("stopped retry checkpoint = %#v", checkpoint)
	}
	if _, err := os.Stat(filepath.Join(root, "stages", "000002-work")); !os.IsNotExist(err) {
		t.Fatalf("second attempt stage exists: %v", err)
	}
}

func TestRetryVisitRestoresAndIsConsumedByFirstActualAttempt(t *testing.T) {
	state := stateFromCheckpoint(Checkpoint{
		NodeVisits:   map[string]int{"work": 1},
		NodeAttempts: map[string]int{"work": 2},
		RetryVisit:   true,
	})
	state.beginVisit("work")
	state.beginAttempt("work")
	checkpoint := state.checkpoint("work", "done", false, nil)
	if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 3 || state.retryVisit {
		t.Fatalf("restored retry state = %#v retry_visit=%v", checkpoint, state.retryVisit)
	}
	state.beginVisit("work")
	if got := state.checkpoint("work", "done", false, nil).NodeVisits["work"]; got != 2 {
		t.Fatalf("visit after retry consumption = %d, want 2", got)
	}
}

func TestExecuteWithRetryStopBeforeFirstAttemptConsumesNothing(t *testing.T) {
	runner := newTestRunner(t, testGraph(startNode("start", "done"), exitNode("done")), NewRegistry(), t.TempDir(), nil)
	state := newEngineState()
	store, err := openRunStore(t.TempDir(), state)
	if err != nil {
		t.Fatal(err)
	}
	runner.stop.Stop()
	_, _, runErr, attempted, _, _, executeErr := runner.executeWithRetry(
		customNode("work", "task", []graph.Edge{{To: graph.Success}}, 0), HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
			return harness.Outcome{}, nil
		}), []graph.Edge{{To: graph.Success}}, state, store, "/workspace", "",
	)
	if executeErr != nil || runErr == nil || runErr.Category != harness.ErrorInterrupted || attempted {
		t.Fatalf("result error=%v runErr=%v attempted=%v", executeErr, runErr, attempted)
	}
	checkpoint := state.checkpoint("", "start", false, nil)
	if checkpoint.NodeVisits["start"] != 0 || checkpoint.NodeAttempts["start"] != 0 || checkpoint.Seq != 0 {
		t.Fatalf("state changed before first attempt: %#v", checkpoint)
	}
}

func TestBackoffDelay(t *testing.T) {
	if got := backoffDelay(1, 0); got != 100*time.Millisecond {
		t.Fatalf("first minimum delay = %s", got)
	}
	if got := backoffDelay(1, 0.5); got != 200*time.Millisecond {
		t.Fatalf("first midpoint delay = %s", got)
	}
	if got := backoffDelay(2, 1); got != 600*time.Millisecond {
		t.Fatalf("second maximum delay = %s", got)
	}
	if got := backoffDelay(100, 1); got != 90*time.Second {
		t.Fatalf("capped maximum delay = %s", got)
	}
}

func TestRunnerConvertsHandlerPanicToTerminalFailure(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(startNode("start", "work"), customNode("work", "task", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		panic("boom")
	}))

	result, err := newTestRunner(t, pipeline, registry, root, nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunFailed || result.FailureReason != "handler panic: boom" {
		t.Fatalf("result = %#v", result)
	}
	assertJSONFile(t, filepath.Join(root, "stages", "000001-work", "error.json"), harness.Error{
		Category: harness.ErrorTerminal,
		Message:  "handler panic: boom",
	})
}

func TestRunnerStopBeforeDispatchPreservesInitialCheckpoint(t *testing.T) {
	root := t.TempDir()
	backend := &fakeBackend{}
	pipeline := graph.Graph{Start: "work", Nodes: []graph.Node{
		customNode("work", "task", []graph.Edge{{To: graph.Success}}, 0),
	}}
	runner := newTestRunner(t, pipeline, NewRegistry(), root, backend)
	runner.Stop()

	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunFailed || result.FailureReason != "stopped by operator" || backend.interrupts != 1 {
		t.Fatalf("result=%#v interrupts=%d", result, backend.interrupts)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "" || checkpoint.NextNode != "work" || checkpoint.RetryVisit || checkpoint.Seq != 0 {
		t.Fatalf("initial checkpoint = %#v", checkpoint)
	}
	if len(checkpoint.CompletedNodes) != 0 || len(checkpoint.NodeVisits) != 0 || len(checkpoint.NodeAttempts) != 0 {
		t.Fatalf("initial checkpoint state = %#v", checkpoint)
	}
}

func TestRunnerReachingPseudoTargetNeverDispatchesIt(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "done"}, nil
	}))
	pipeline := graph.Graph{Start: "work", Nodes: []graph.Node{customNode("work", "task", []graph.Edge{{To: graph.Success}}, 0)}}
	result, err := newTestRunner(t, pipeline, registry, root, nil).Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NextNode != graph.Success || checkpoint.NodeVisits[graph.Success] != 0 || checkpoint.NodeAttempts[graph.Success] != 0 || checkpoint.Seq != 1 {
		t.Fatalf("pseudo-target was counted: %#v", checkpoint)
	}
}

func TestResumeRunnerContinuesInitialCheckpointWithoutRewritingIt(t *testing.T) {
	root := t.TempDir()
	pipeline := graph.Graph{Start: "work", Nodes: []graph.Node{customNode("work", "task", []graph.Edge{{To: graph.Success}}, 0)}}
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "resumed work"}, nil
	}))
	initial := newTestRunner(t, pipeline, registry, root, nil)
	initial.Stop()
	result, err := initial.Run()
	if err != nil || result.Status != RunFailed {
		t.Fatalf("initial result=%#v err=%v", result, err)
	}
	wantCheckpoint, err := os.ReadFile(filepath.Join(root, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}

	registry = NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		gotCheckpoint, err := os.ReadFile(filepath.Join(root, "checkpoint.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotCheckpoint, wantCheckpoint) {
			t.Fatal("resume rewrote the initial checkpoint before dispatch")
		}
		return harness.Outcome{Notes: "resumed work"}, nil
	}))
	resumed := newTestResumeRunner(t, pipeline, registry, root, nil)
	result, err = resumed.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("resumed result=%#v err=%v", result, err)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 1 {
		t.Fatalf("resumed initial counters = %#v", checkpoint)
	}
}

func TestResumeRunnerUsesResolvedSuccessorAndRecoversCrashedStageSequence(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(
		startNode("start", "choose"),
		customNode("choose", "task", []graph.Edge{{To: "left"}, {To: "right"}}, 0),
		customNode("right", "task", []graph.Edge{{To: "done"}}, 0),
		exitNode("done"),
		exitNode("left"),
	)
	registry := NewRegistry()
	var initial *Runner
	registry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID != "choose" {
			t.Fatalf("unexpected initial dispatch: %s", node.Base().ID)
		}
		initial.Stop()
		return harness.Outcome{Next: "right", Notes: "chosen once"}, nil
	}))
	initial = newTestRunner(t, pipeline, registry, root, nil)
	result, err := initial.Run()
	if err != nil || result.Status != RunFailed {
		t.Fatalf("initial result=%#v err=%v", result, err)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "choose" || checkpoint.NextNode != "right" || checkpoint.Seq != 1 {
		t.Fatalf("success checkpoint = %#v", checkpoint)
	}
	if err := os.Mkdir(filepath.Join(root, "stages", "000009-crashed"), 0o755); err != nil {
		t.Fatal(err)
	}

	resumedRegistry := NewRegistry()
	resumedRegistry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID == "choose" {
			t.Fatal("resume re-asked the completed routing decision")
		}
		if node.Base().ID != "right" || filepath.Base(scope.StageDir) != "000010-right" {
			t.Fatalf("resumed node=%q stage=%q", node.Base().ID, scope.StageDir)
		}
		return harness.Outcome{Notes: "continued"}, nil
	}))
	result, err = newTestResumeRunner(t, pipeline, resumedRegistry, root, nil).Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("resumed result=%#v err=%v", result, err)
	}
	checkpoint = mustCheckpoint(t, root)
	if checkpoint.Seq != 10 || !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"choose", "right"}) {
		t.Fatalf("resumed checkpoint = %#v", checkpoint)
	}
}

func TestResumeRunnerRepeatedFailureContinuesOneVisit(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(
		startNode("start", "work"),
		customNode("work", "task", []graph.Edge{{To: "done"}}, 1),
		exitNode("done"),
	)

	runWork := func(succeed bool, resume bool) RunResult {
		registry := NewRegistry()
		registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
			if succeed {
				return harness.Outcome{Notes: "recovered"}, nil
			}
			return harness.Outcome{}, &harness.Error{Category: harness.ErrorTerminal, Message: "still broken"}
		}))
		var runner *Runner
		if resume {
			runner = newTestResumeRunner(t, pipeline, registry, root, nil)
		} else {
			runner = newTestRunner(t, pipeline, registry, root, nil)
		}
		result, err := runner.Run()
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if result := runWork(false, false); result.Status != RunFailed {
		t.Fatalf("initial result = %#v", result)
	}
	if result := runWork(false, true); result.Status != RunFailed {
		t.Fatalf("second result = %#v", result)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 2 || !checkpoint.RetryVisit ||
		len(checkpoint.CompletedNodes) != 0 || checkpoint.LastStage != "" || checkpoint.LastResponse != "" {
		t.Fatalf("second failure checkpoint = %#v", checkpoint)
	}
	if result := runWork(true, true); result.Status != RunCompleted {
		t.Fatalf("final result = %#v", result)
	}
	checkpoint = mustCheckpoint(t, root)
	if checkpoint.NodeVisits["work"] != 1 || checkpoint.NodeAttempts["work"] != 3 || checkpoint.RetryVisit {
		t.Fatalf("recovered checkpoint = %#v", checkpoint)
	}
}

func TestResumeRunnerSnapshotsBindingsFromReconstructedBackend(t *testing.T) {
	root := t.TempDir()
	pipeline := testGraph(startNode("start", "work"), customNode("work", "task", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	initialBindings := map[string]harness.ThreadBinding{
		"work": {Harness: "codex", SessionID: "old-session", Workdir: "/workspace"},
	}
	initialBackend := &fakeBackend{bindings: initialBindings}
	failingRegistry := NewRegistry()
	failingRegistry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{}, &harness.Error{Category: harness.ErrorTerminal, Message: "restart me"}
	}))
	if result, err := newTestRunner(t, pipeline, failingRegistry, root, initialBackend).Run(); err != nil || result.Status != RunFailed {
		t.Fatalf("initial result=%#v err=%v", result, err)
	}
	checkpoint := mustCheckpoint(t, root)
	if !reflect.DeepEqual(checkpoint.Sessions, initialBindings) {
		t.Fatalf("initial sessions = %v", checkpoint.Sessions)
	}

	restoredBindings := cloneMap(checkpoint.Sessions)
	restoredBindings["review"] = harness.ThreadBinding{Harness: "claude", SessionID: "new-session", Workdir: "/workspace"}
	reconstructedBackend := &fakeBackend{bindings: restoredBindings}
	successRegistry := NewRegistry()
	successRegistry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "resumed session"}, nil
	}))
	if result, err := newTestResumeRunner(t, pipeline, successRegistry, root, reconstructedBackend).Run(); err != nil || result.Status != RunCompleted {
		t.Fatalf("resumed result=%#v err=%v", result, err)
	}
	checkpoint = mustCheckpoint(t, root)
	if !reflect.DeepEqual(checkpoint.Sessions, restoredBindings) {
		t.Fatalf("resumed sessions = %v, want %v", checkpoint.Sessions, restoredBindings)
	}
}

func TestResumeRunnerFinalCheckpointReturnsWithoutDispatchOrRewrite(t *testing.T) {
	root := t.TempDir()
	pipeline := graph.Graph{Start: "work", Nodes: []graph.Node{customNode("work", "task", []graph.Edge{{To: graph.Success}}, 0)}}
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "done"}, nil
	}))
	if result, err := newTestRunner(t, pipeline, registry, root, nil).Run(); err != nil || result.Status != RunCompleted {
		t.Fatalf("initial result=%#v err=%v", result, err)
	}
	wantCheckpoint, err := os.ReadFile(filepath.Join(root, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry = NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		t.Fatal("final checkpoint dispatched work")
		return harness.Outcome{}, nil
	}))
	result, err := newTestResumeRunner(t, pipeline, registry, root, nil).Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("resumed result=%#v err=%v", result, err)
	}
	gotCheckpoint, err := os.ReadFile(filepath.Join(root, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCheckpoint, wantCheckpoint) {
		t.Fatal("completed resume rewrote the final checkpoint")
	}
}

func TestResumeRunnerRejectsMalformedCheckpointContinuation(t *testing.T) {
	pipeline := graph.Graph{Start: "work", Nodes: []graph.Node{customNode("work", "task", []graph.Edge{{To: graph.Success}}, 0)}}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "malformed json", raw: "{", want: "decode checkpoint"},
		{name: "empty continuation", raw: `{}`, want: "not in the graph"},
		{name: "unknown next node", raw: `{"current_node":"work","next_node":"missing"}`, want: "not in the graph"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "checkpoint.json"), []byte(test.raw), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ResumeRunner(pipeline, NewRegistry(), RunnerConfig{
				LogsRoot: root,
				Workdir:  "/workspace",
				Validate: func(graph.Graph) error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNewRunnerValidationFailureCreatesNoRunFiles(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "run")
	_, err := NewRunner(testGraph(startNode("start", "done"), exitNode("done")), nil, RunnerConfig{
		LogsRoot: root,
		Workdir:  "/workspace",
		Validate: func(graph.Graph) error { return errors.New("lint failed") },
	})
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("run root exists after validation failure: %v", statErr)
	}
}

func TestNewRunnerRequiresValidationBeforeCreatingRunFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "run")
	_, err := NewRunner(testGraph(startNode("start", "done"), exitNode("done")), nil, RunnerConfig{
		LogsRoot: root,
		Workdir:  "/workspace",
	})
	if err == nil || !strings.Contains(err.Error(), "validate function must not be nil") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("run root exists without validation: %v", statErr)
	}
}

func TestStopSignalIsOneShotAndWaits(t *testing.T) {
	signal := NewStopSignal()
	returned := make(chan struct{})
	go func() {
		signal.Wait()
		close(returned)
	}()
	if signal.IsSet() {
		t.Fatal("new signal is set")
	}
	signal.Stop()
	signal.Stop()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return")
	}
	if !signal.IsSet() {
		t.Fatal("stopped signal is not set")
	}
}

func newTestRunner(t *testing.T, pipeline graph.Graph, registry *Registry, root string, backend harness.CodergenBackend) *Runner {
	t.Helper()
	runner, err := NewRunner(pipeline, registry, RunnerConfig{
		LogsRoot: root,
		Workdir:  "/workspace",
		Validate: func(graph.Graph) error { return nil },
		Backend:  backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func newTestResumeRunner(t *testing.T, pipeline graph.Graph, registry *Registry, root string, backend harness.CodergenBackend) *Runner {
	t.Helper()
	runner, err := ResumeRunner(pipeline, registry, RunnerConfig{
		LogsRoot: root,
		Workdir:  "/workspace",
		Validate: func(graph.Graph) error { return nil },
		Backend:  backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func mustCheckpoint(t *testing.T, root string) Checkpoint {
	t.Helper()
	checkpoint, err := LoadCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func testGraph(nodes ...graph.Node) graph.Graph {
	pipeline := graph.Graph{Name: "test", Goal: "ship it"}
	exits := make(map[string]string)
	for _, node := range nodes {
		if marker, ok := node.(*graph.SupervisorNode); ok && marker.Prompt == "__test_exit__" {
			target := graph.Success
			if len(exits) > 0 {
				target = graph.Failure
			}
			exits[marker.ID] = target
			continue
		}
		if marker, ok := node.(*graph.CodergenNode); ok && marker.Prompt.Present && marker.Prompt.Value == "__test_start__" {
			pipeline.Start = marker.Edges[0].To
			continue
		}
		pipeline.Nodes = append(pipeline.Nodes, node)
	}
	for _, node := range pipeline.Nodes {
		switch node := node.(type) {
		case *graph.CodergenNode:
			rewriteTestExitTargets(node.Edges, exits)
		case *graph.FanInNode:
			rewriteTestExitTargets(node.Edges, exits)
		case *graph.ToolNode:
			if target := exits[node.OnSuccess]; target != "" {
				node.OnSuccess = target
			}
			if node.OnError.Present {
				if target := exits[node.OnError.Value]; target != "" {
					node.OnError.Value = target
				}
			}
		}
	}
	if pipeline.Start == "" && len(pipeline.Nodes) > 0 {
		pipeline.Start = pipeline.Nodes[0].Base().ID
	}
	return pipeline
}

func startNode(id, next string) graph.Node {
	node := &graph.CodergenNode{NodeBase: graph.NodeBase{ID: id}, Edges: []graph.Edge{{To: next}}}
	node.Prompt = optional("__test_start__")
	return node
}

func exitNode(id string) graph.Node {
	return &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: id}, Prompt: "__test_exit__"}
}

func customNode(id, nodeType string, edges []graph.Edge, maxVisits int) graph.Node {
	node := &graph.CodergenNode{NodeBase: graph.NodeBase{ID: id}, Edges: edges}
	if maxVisits > 0 {
		node.MaxVisits = jsonschema.Optional[int]{Present: true, Value: maxVisits}
	}
	return node
}

func rewriteTestExitTargets(edges []graph.Edge, exits map[string]string) {
	for index := range edges {
		if target := exits[edges[index].To]; target != "" {
			edges[index].To = target
		}
	}
}

type fakeBackend struct {
	bindings   map[string]harness.ThreadBinding
	interrupts int
}

func (*fakeBackend) Run(harness.CodergenTurn) (harness.Outcome, *harness.Error) {
	panic("not used")
}

func (*fakeBackend) RunSupervisor(harness.SupervisorTurn) (harness.Verdict, *harness.Error) {
	panic("not used")
}

func (*fakeBackend) Steer([]harness.ContentPart) harness.SteerStatus {
	panic("not used")
}

func (b *fakeBackend) InterruptAll() {
	b.interrupts++
}

func (b *fakeBackend) Bindings() map[string]harness.ThreadBinding {
	return b.bindings
}

func (*fakeBackend) SetBindingOpened(harness.BindingOpened) {}
