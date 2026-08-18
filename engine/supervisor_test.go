package engine

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

func TestSupervisorPatrolSteersLiveTargetAndPersistsRecord(t *testing.T) {
	root := t.TempDir()
	backend := newSupervisorBackend()
	backend.verdict = harness.Verdict{Verdict: "steer", Target: "work", Message: "finish the bounded task"}
	pipeline := supervisedTestGraph("5ms")
	registry := NewRegistry()
	started := make(chan struct{})
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		close(started)
		select {
		case <-backend.steered:
			return harness.Outcome{Notes: "accepted coaching"}, nil
		case <-time.After(2 * time.Second):
			return harness.Outcome{}, terminalError("supervisor did not steer")
		}
	}))
	runner, err := NewRunner(pipeline, registry, RunnerConfig{
		LogsRoot: root, Workdir: t.TempDir(), Validate: func(graph.Graph) error { return nil }, Backend: backend,
		DefaultModel: "gpt-test", DefaultProvider: "openai", DefaultReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	<-started
	if backend.supervisorCalls.Load() != 1 {
		t.Fatalf("supervisor calls = %d", backend.supervisorCalls.Load())
	}
	if turns := backend.supervisorTurns(); len(turns) != 1 || !strings.Contains(turns[0].Parts[0].Text, "Supervisor briefing") {
		t.Fatalf("initial supervisor turns = %#v", turns)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NextNode != graph.Success || checkpoint.Sessions["coach"].SessionID == "" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	assertSupervisorTimeline(t, root, "coach", "steer", true)

	audit, err := os.ReadFile(filepath.Join(root, "stages", "latest", "work", "steering.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var steering struct {
		Origin string `json:"origin"`
	}
	if err := json.Unmarshal(audit, &steering); err != nil || steering.Origin != "coach" {
		t.Fatalf("steering audit = %s, %v", audit, err)
	}
	inbox, err := os.ReadFile(filepath.Join(root, "supervisors", "coach", "inbox.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var digest attemptDigest
	if err := json.Unmarshal(inbox, &digest); err != nil || digest.NodeID != "work" || digest.Disposition != "outcome" || digest.Attempt != 1 {
		t.Fatalf("digest = %s, %v", inbox, err)
	}
	if _, err := os.Stat(filepath.Join(root, "supervisors", "coach", "briefed.json")); err != nil {
		t.Fatalf("briefing marker: %v", err)
	}
	assertSupervisorFlushed(t, root, "coach", 0, false)
}

func TestSupervisorQuietScopeCostsNoTurn(t *testing.T) {
	backend := newSupervisorBackend()
	pipeline := graph.Graph{Start: "work", Nodes: []graph.Node{
		&graph.ToolNode{NodeBase: graph.NodeBase{ID: "work"}, ToolCommand: "true", OnSuccess: graph.Success},
		&graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}, Prompt: "watch", Supervises: []string{"work"}, Interval: optional(graph.Duration("200ms"))},
	}}
	runner, err := NewRunner(pipeline, NewRegistry(), RunnerConfig{
		LogsRoot: t.TempDir(), Workdir: t.TempDir(), Validate: func(graph.Graph) error { return nil }, Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if backend.supervisorCalls.Load() != 0 || len(backend.Bindings()) != 0 {
		t.Fatalf("quiet supervisor calls=%d bindings=%#v", backend.supervisorCalls.Load(), backend.Bindings())
	}
}

func TestSupervisorResumePreservesBindingBacklogAndBriefing(t *testing.T) {
	root := t.TempDir()
	workdir := t.TempDir()
	pipeline := supervisedTestGraph("2ms")
	backend := newSupervisorBackend()
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		select {
		case <-backend.started:
			return harness.Outcome{}, terminalError("pause for resume")
		case <-time.After(2 * time.Second):
			return harness.Outcome{}, terminalError("supervisor did not start")
		}
	}))
	runner, err := NewRunner(pipeline, registry, RunnerConfig{
		LogsRoot: root, Workdir: workdir, Validate: func(graph.Graph) error { return nil }, Backend: backend,
		DefaultModel: "gpt-test", DefaultProvider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run()
	if err != nil || result.Status != RunFailed {
		t.Fatalf("first Run() = %#v, %v", result, err)
	}
	checkpoint := mustCheckpoint(t, root)
	binding := checkpoint.Sessions["coach"]
	if binding.SessionID == "" {
		t.Fatalf("checkpoint sessions = %#v", checkpoint.Sessions)
	}
	if _, err := os.Stat(filepath.Join(root, "supervisors", "coach", "inbox.jsonl")); err != nil {
		t.Fatalf("resume backlog: %v", err)
	}

	resumedBackend := newSupervisorBackend()
	resumedBackend.bindings = cloneMap(checkpoint.Sessions)
	resumedRegistry := NewRegistry()
	resumedRegistry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		select {
		case <-resumedBackend.started:
			return harness.Outcome{Notes: "resumed"}, nil
		case <-time.After(2 * time.Second):
			return harness.Outcome{}, terminalError("resumed supervisor did not start")
		}
	}))
	resumed, err := ResumeRunner(pipeline, resumedRegistry, RunnerConfig{
		LogsRoot: root, Workdir: workdir, Validate: func(graph.Graph) error { return nil }, Backend: resumedBackend,
		DefaultModel: "gpt-test", DefaultProvider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = resumed.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("resumed Run() = %#v, %v", result, err)
	}
	turns := resumedBackend.supervisorTurns()
	if len(turns) != 1 {
		t.Fatalf("resumed supervisor turns = %d", len(turns))
	}
	message := turns[0].Parts[0].Text
	if strings.Contains(message, "Supervisor briefing") {
		t.Fatalf("same resumed binding received duplicate briefing: %s", message)
	}
	if !strings.Contains(message, `"batch":`) || strings.Contains(message, `"batches":`) ||
		!strings.Contains(message, "inbox.000001.jsonl") || !strings.Contains(message, `"count": 1`) {
		t.Fatalf("resumed nudge omitted backlog: %s", message)
	}
	if got := mustCheckpoint(t, root).Sessions["coach"]; got != binding {
		t.Fatalf("resumed binding = %#v, want %#v", got, binding)
	}
	assertSupervisorFlushed(t, root, "coach", 1, true)
}

func TestFreshSupervisorBindingRequiresBriefing(t *testing.T) {
	dir := t.TempDir()
	oldBinding := harness.ThreadBinding{Harness: "test", SessionID: "old", Workdir: "/work"}
	if err := writeJSON(filepath.Join(dir, "briefed.json"), supervisorBriefingRecord{Binding: oldBinding}); err != nil {
		t.Fatal(err)
	}
	backend := newSupervisorBackend()
	backend.bindings["coach"] = harness.ThreadBinding{Harness: "test", SessionID: "fresh", Workdir: "/work"}
	runtime := &supervisorRuntime{node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}}, dir: dir}
	service := &supervisionService{runner: &Runner{config: RunnerConfig{Backend: backend}}}
	briefing, err := service.supervisorNeedsBriefing(runtime)
	if err != nil || !briefing {
		t.Fatalf("supervisorNeedsBriefing() = %v, %v", briefing, err)
	}
}

func TestSupervisorBindingCheckpointEventNamesSupervisor(t *testing.T) {
	root := t.TempDir()
	store, err := openRunStore(root, newEngineState())
	if err != nil {
		t.Fatal(err)
	}
	backend := newSupervisorBackend()
	binding := harness.ThreadBinding{Harness: "test", SessionID: "coach-session", Workdir: t.TempDir()}
	backend.bindings["coach"] = binding
	runtime := &supervisorRuntime{node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}}}
	runner := &Runner{
		config: RunnerConfig{Backend: backend},
		supervision: &supervisionService{
			byID: map[string]*supervisorRuntime{"coach": runtime},
		},
	}
	runner.installBindingCallback(store)
	backend.mu.Lock()
	callback := backend.callback
	backend.mu.Unlock()
	if callback == nil {
		t.Fatal("binding callback was not installed")
	}
	if callbackErr := callback("coach", binding); callbackErr != nil {
		t.Fatal(callbackErr)
	}
	events, err := readTimeline(filepath.Join(root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0]["type"] != "CheckpointSaved" || events[0]["node_id"] != "coach" {
		t.Fatalf("checkpoint events = %#v", events)
	}
}

func TestSupervisorFlushesNeverOverlap(t *testing.T) {
	backend := newSupervisorBackend()
	backend.verdict = harness.Verdict{Verdict: "ok"}
	backend.turnDelay = 20 * time.Millisecond
	pipeline := supervisedTestGraph("2ms")
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		time.Sleep(70 * time.Millisecond)
		return harness.Outcome{Notes: "done"}, nil
	}))
	runner, err := NewRunner(pipeline, registry, RunnerConfig{
		LogsRoot: t.TempDir(), Workdir: t.TempDir(), Validate: func(graph.Graph) error { return nil }, Backend: backend,
		DefaultModel: "gpt-test", DefaultProvider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if backend.supervisorCalls.Load() < 2 || backend.peakSupervisor.Load() != 1 {
		t.Fatalf("calls=%d peak=%d", backend.supervisorCalls.Load(), backend.peakSupervisor.Load())
	}
}

func TestSupervisorStopAndFinalizeInterruptAndAwait(t *testing.T) {
	for _, test := range []struct {
		name         string
		operatorStop bool
	}{
		{name: "finalize"},
		{name: "operator stop", operatorStop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newSupervisorBackend()
			backend.blockSupervisor = true
			pipeline := supervisedTestGraph("2ms")
			registry := NewRegistry()
			registry.Register("codergen", HandlerFunc(func(_ graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
				select {
				case <-backend.started:
				case <-time.After(2 * time.Second):
					return harness.Outcome{}, terminalError("supervisor did not start")
				}
				if test.operatorStop {
					<-scope.Stop.done
					return harness.Outcome{}, interruptedError("stopped by operator")
				}
				return harness.Outcome{Notes: "done"}, nil
			}))
			root := t.TempDir()
			runner, err := NewRunner(pipeline, registry, RunnerConfig{
				LogsRoot: root, Workdir: t.TempDir(), Validate: func(graph.Graph) error { return nil }, Backend: backend,
				DefaultModel: "gpt-test", DefaultProvider: "openai",
			})
			if err != nil {
				t.Fatal(err)
			}
			type runResponse struct {
				result RunResult
				err    error
			}
			finished := make(chan runResponse, 1)
			go func() {
				result, err := runner.Run()
				finished <- runResponse{result: result, err: err}
			}()
			if test.operatorStop {
				select {
				case <-backend.started:
				case <-time.After(2 * time.Second):
					t.Fatal("supervisor did not start")
				}
				runner.Stop()
			}
			var response runResponse
			select {
			case response = <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not await an interrupted supervisor")
			}
			if response.err != nil {
				t.Fatal(response.err)
			}
			wantStatus := RunCompleted
			if test.operatorStop {
				wantStatus = RunFailed
			}
			if response.result.Status != wantStatus || backend.interruptCalls.Load() == 0 {
				t.Fatalf("Run()=%#v interrupts=%d", response.result, backend.interruptCalls.Load())
			}
			select {
			case <-backend.returned:
			default:
				t.Fatal("Run returned before supervisor turn")
			}
			if _, err := os.Stat(filepath.Join(root, "supervisors", "coach", "briefed.json")); !os.IsNotExist(err) {
				t.Fatalf("interrupted briefing was marked complete: %v", err)
			}
			assertSupervisorTimeline(t, root, "coach", "error", false)
			assertSupervisorError(t, root, "supervisor_turn", "supervisor interrupted")
		})
	}
}

func TestStopPreventsNewPatrolWhileWorkDrains(t *testing.T) {
	backend := newSupervisorBackend()
	pipeline := supervisedTestGraph("50ms")
	workStarted := make(chan struct{})
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(_ graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		close(workStarted)
		<-scope.Stop.done
		time.Sleep(150 * time.Millisecond)
		return harness.Outcome{}, interruptedError("stopped by operator")
	}))
	runner, err := NewRunner(pipeline, registry, RunnerConfig{
		LogsRoot: t.TempDir(), Workdir: t.TempDir(), Validate: func(graph.Graph) error { return nil }, Backend: backend,
		DefaultModel: "gpt-test", DefaultProvider: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := runner.Run()
		finished <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()
	select {
	case <-workStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("work did not start")
	}
	runner.Stop()
	select {
	case response := <-finished:
		if response.err != nil || response.result.Status != RunFailed {
			t.Fatalf("Run() = %#v, %v", response.result, response.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish")
	}
	if calls := backend.supervisorCalls.Load(); calls != 0 {
		t.Fatalf("supervisor turns started after Stop: %d", calls)
	}
}

func TestSupervisorInboxRotationIsLosslessAndMonotonic(t *testing.T) {
	dir := t.TempDir()
	runtime := &supervisorRuntime{dir: dir, nextBatch: 7}
	const count = 100
	var wg sync.WaitGroup
	wg.Add(count)
	for index := range count {
		go func() {
			defer wg.Done()
			_ = runtime.append(attemptDigest{NodeID: "work", Disposition: "outcome", Seq: uint64(index + 1)})
		}()
	}
	batch, rotated, _, err := runtime.rotate()
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	remaining, _, err := digestTally(filepath.Join(dir, "inbox.jsonl"))
	if os.IsNotExist(err) {
		remaining = 0
		err = nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if rotated+remaining != count {
		t.Fatalf("rotated=%d remaining=%d", rotated, remaining)
	}
	if rotated > 0 && filepath.Base(batch) != "inbox.000008.jsonl" {
		t.Fatalf("batch = %q", batch)
	}
	next, err := recoverSupervisorBatch(dir)
	if err != nil || (rotated > 0 && next != 8) {
		t.Fatalf("recovered batch = %d, %v", next, err)
	}
}

func TestSupervisorDigestTallyAcceptsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	runtime := &supervisorRuntime{node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}}, dir: dir}
	if err := runtime.append(attemptDigest{NodeID: "work", Disposition: "outcome", Notes: strings.Repeat("x", 128*1024)}); err != nil {
		t.Fatal(err)
	}
	batch, count, tally, err := runtime.rotate()
	if err != nil {
		t.Fatal(err)
	}
	if batch == "" || count != 1 || tally["work"]["outcome"] != 1 {
		t.Fatalf("batch=%q count=%d tally=%#v", batch, count, tally)
	}
}

func TestOutOfScopeAttemptAppendsNothing(t *testing.T) {
	dir := t.TempDir()
	runtime := &supervisorRuntime{
		node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}, Supervises: []string{"work"}},
		dir:  dir,
	}
	service := &supervisionService{all: []*supervisorRuntime{runtime}}
	service.appendAttempt("other", 1, stage{Seq: 1, Dir: t.TempDir()}, harness.Outcome{Notes: "outside scope"}, nil)
	if _, err := os.Stat(filepath.Join(dir, "inbox.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("out-of-scope inbox stat error = %v", err)
	}
}

func TestSupervisorAppendFailureIsRecorded(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "inbox.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := &supervisorRuntime{
		node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}, Supervises: []string{"work"}},
		dir:  dir,
	}
	service := &supervisionService{all: []*supervisorRuntime{runtime}}
	service.appendAttempt("work", 1, stage{Seq: 1, Dir: t.TempDir()}, harness.Outcome{Notes: "done"}, nil)
	raw, err := os.ReadFile(filepath.Join(dir, "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record supervisionErrorRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Operation != "append_attempt_digest" {
		t.Fatalf("error record = %s, %v", raw, err)
	}
}

func TestSupervisorVerdictsFlowUpAndCoachingFlowsDown(t *testing.T) {
	root := t.TempDir()
	store, err := openRunStore(root, newEngineState())
	if err != nil {
		t.Fatal(err)
	}
	lower := &supervisorRuntime{node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "lead"}, Supervises: []string{"work"}}, dir: filepath.Join(root, "supervisors", "lead")}
	upper := &supervisorRuntime{node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "director"}, Supervises: []string{"lead"}}, dir: filepath.Join(root, "supervisors", "director")}
	for _, runtime := range []*supervisorRuntime{lower, upper} {
		if err := os.MkdirAll(runtime.dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	service := &supervisionService{
		runner: &Runner{}, store: store, all: []*supervisorRuntime{lower, upper},
		byID: map[string]*supervisorRuntime{"lead": lower, "director": upper},
	}
	service.recordVerdict(lower, harness.Verdict{Verdict: "ok", Message: "work is on track"}, nil)
	service.recordVerdict(upper, harness.Verdict{Verdict: "steer", Target: "lead", Message: "watch the validation loop"}, nil)

	upperInbox, err := os.ReadFile(filepath.Join(upper.dir, "inbox.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var upward supervisorDigest
	if err := json.Unmarshal(upperInbox, &upward); err != nil || upward.Disposition != "verdict" || upward.NodeID != "lead" || upward.Verdict != "ok" {
		t.Fatalf("upward digest = %s, %v", upperInbox, err)
	}
	lowerInbox, err := os.ReadFile(filepath.Join(lower.dir, "inbox.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var downward supervisorDigest
	if err := json.Unmarshal(lowerInbox, &downward); err != nil || downward.Disposition != "coaching" || downward.NodeID != "director" || downward.Message != "watch the validation loop" {
		t.Fatalf("downward digest = %s, %v", lowerInbox, err)
	}
	assertSupervisorTimeline(t, root, "director", "steer", true)
}

func TestMalformedSupervisorSteerDegradesToRecordedOK(t *testing.T) {
	root := t.TempDir()
	store, err := openRunStore(root, newEngineState())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &supervisorRuntime{
		node: &graph.SupervisorNode{NodeBase: graph.NodeBase{ID: "coach"}, Supervises: []string{"work"}},
		dir:  filepath.Join(root, "supervisors", "coach"),
	}
	service := &supervisionService{runner: &Runner{}, store: store, all: []*supervisorRuntime{runtime}, byID: map[string]*supervisorRuntime{"coach": runtime}}
	service.recordVerdict(runtime, harness.Verdict{Verdict: "steer", Target: "elsewhere", Message: "wrong scope"}, nil)
	events, err := readTimeline(filepath.Join(root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0]["verdict"] != "ok" {
		t.Fatalf("events = %#v", events)
	}
	if _, exists := events[0]["target"]; exists {
		t.Fatalf("degraded event retained target: %#v", events[0])
	}
}

func supervisedTestGraph(interval graph.Duration) graph.Graph {
	return graph.Graph{Goal: "ship the task", Start: "work", Nodes: []graph.Node{
		&graph.CodergenNode{NodeBase: graph.NodeBase{ID: "work"}, Edges: []graph.Edge{{To: graph.Success}}},
		&graph.SupervisorNode{
			NodeBase: graph.NodeBase{ID: "coach"}, Prompt: "Keep $goal bounded.", Supervises: []string{"work"}, Interval: optional(interval),
		},
	}}
}

func assertSupervisorTimeline(t *testing.T, root, supervisor, verdict string, delivered bool) {
	t.Helper()
	events, err := readTimeline(filepath.Join(root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event["type"] == "SupervisorVerdict" && event["supervisor"] == supervisor {
			if event["verdict"] != verdict || (verdict == "steer" && event["delivered"] != delivered) {
				t.Fatalf("verdict event = %#v", event)
			}
			return
		}
	}
	t.Fatalf("no SupervisorVerdict in %#v", events)
}

func assertSupervisorFlushed(t *testing.T, root, supervisor string, count int, hasBatch bool) {
	t.Helper()
	events, err := readTimeline(filepath.Join(root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event["type"] != "SupervisorFlushed" || event["supervisor"] != supervisor {
			continue
		}
		_, exists := event["batch"]
		if event["count"] != float64(count) || exists != hasBatch {
			continue
		}
		return
	}
	t.Fatalf("no matching SupervisorFlushed in %#v", events)
}

func assertSupervisorError(t *testing.T, root, operation, message string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "supervisors", "coach", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var record supervisionErrorRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Operation == operation && strings.Contains(record.Error, message) {
			return
		}
	}
	t.Fatalf("no matching supervisor error in %s", raw)
}

type supervisorBackend struct {
	mu               sync.Mutex
	callback         harness.BindingOpened
	bindings         map[string]harness.ThreadBinding
	turns            []harness.SupervisorTurn
	steered          chan struct{}
	steerOnce        sync.Once
	started          chan struct{}
	startOnce        sync.Once
	verdict          harness.Verdict
	turnDelay        time.Duration
	supervisorErrors []*harness.Error

	blockSupervisor bool
	interrupted     chan struct{}
	returned        chan struct{}
	interruptOnce   sync.Once
	returnOnce      sync.Once
	interruptCalls  atomic.Int32

	supervisorCalls  atomic.Int32
	activeSupervisor atomic.Int32
	peakSupervisor   atomic.Int32
}

func newSupervisorBackend() *supervisorBackend {
	return &supervisorBackend{
		bindings: make(map[string]harness.ThreadBinding), steered: make(chan struct{}), started: make(chan struct{}),
		interrupted: make(chan struct{}), returned: make(chan struct{}),
		verdict: harness.Verdict{Verdict: "ok"},
	}
}

func (*supervisorBackend) Run(harness.CodergenTurn) (harness.Outcome, *harness.Error) {
	panic("walk handler owns the test execution")
}

func (b *supervisorBackend) RunSupervisor(turn harness.SupervisorTurn) (harness.Verdict, *harness.Error) {
	call := b.supervisorCalls.Add(1)
	b.startOnce.Do(func() { close(b.started) })
	active := b.activeSupervisor.Add(1)
	defer b.activeSupervisor.Add(-1)
	for {
		peak := b.peakSupervisor.Load()
		if active <= peak || b.peakSupervisor.CompareAndSwap(peak, active) {
			break
		}
	}
	b.mu.Lock()
	b.turns = append(b.turns, turn)
	_, exists := b.bindings[turn.NodeID]
	callback := b.callback
	if !exists {
		b.bindings[turn.NodeID] = harness.ThreadBinding{Harness: "test", SessionID: turn.NodeID + "-session", Workdir: turn.Workdir}
	}
	binding := b.bindings[turn.NodeID]
	b.mu.Unlock()
	if !exists && callback != nil {
		if err := callback(turn.NodeID, binding); err != nil {
			return harness.Verdict{}, err
		}
	}
	file, err := os.OpenFile(turn.RunLog, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return harness.Verdict{}, terminalError(err.Error())
	}
	_ = json.NewEncoder(file).Encode(map[string]any{"type": "user", "parts": turn.Parts})
	_ = file.Close()
	if b.turnDelay > 0 {
		time.Sleep(b.turnDelay)
	}
	if b.blockSupervisor {
		<-b.interrupted
		b.returnOnce.Do(func() { close(b.returned) })
		return harness.Verdict{}, interruptedError("supervisor interrupted")
	}
	if index := int(call - 1); index < len(b.supervisorErrors) && b.supervisorErrors[index] != nil {
		return harness.Verdict{}, b.supervisorErrors[index]
	}
	return b.verdict, nil
}

func (b *supervisorBackend) supervisorTurns() []harness.SupervisorTurn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]harness.SupervisorTurn(nil), b.turns...)
}

func (b *supervisorBackend) Steer([]harness.ContentPart) harness.SteerStatus {
	b.steerOnce.Do(func() { close(b.steered) })
	return harness.SteerAccepted
}

func (b *supervisorBackend) InterruptAll() {
	b.interruptCalls.Add(1)
	b.interruptOnce.Do(func() { close(b.interrupted) })
}

func (b *supervisorBackend) Bindings() map[string]harness.ThreadBinding {
	b.mu.Lock()
	defer b.mu.Unlock()
	copy := make(map[string]harness.ThreadBinding, len(b.bindings))
	maps.Copy(copy, b.bindings)
	return copy
}

func (b *supervisorBackend) SetBindingOpened(callback harness.BindingOpened) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.callback = callback
}
