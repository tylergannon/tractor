package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

func TestRunnerTimelineNarratesRetriesParallelBranchesAndCheckpoints(t *testing.T) {
	repo := newGitTestRepository(t)
	root := t.TempDir()
	retry := customNode("retry", "retry_task", []graph.Edge{{To: "fanout"}}, 0).(*graph.CodergenNode)
	retry.MaxRetries = jsonschema.Optional[int]{Present: true, Value: 1}
	parallel := &graph.ParallelNode{NodeBase: graph.NodeBase{ID: "fanout"}, Branches: []string{"left", "right"}}
	parallel.MaxParallel = jsonschema.Optional[int]{Present: true, Value: 1}
	pipeline := testGraph(
		startNode("start", "retry"),
		retry,
		parallel,
		customNode("left", "task", []graph.Edge{{To: "join"}}, 0),
		customNode("right", "task", []graph.Edge{{To: "join"}}, 0),
		&graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}, Edges: []graph.Edge{{To: "done"}}},
		exitNode("done"),
	)
	pipeline.Name = "timeline-test"

	registry := NewRegistry()
	var retryCalls atomic.Int32
	registry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID == "retry" && retryCalls.Add(1) == 1 {
			return harness.Outcome{}, &harness.Error{Category: harness.ErrorRetryable, Message: "try again"}
		}
		return harness.Outcome{Notes: node.Base().ID}, nil
	}))
	registry.Register("parallel.fan_in", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "joined"}, nil
	}))
	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, repo, nil)
	runner.retryDelay = func(int) time.Duration { return 0 }
	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}

	events, err := readTimeline(filepath.Join(root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]any, len(events))
	for index, event := range events {
		got[index] = event["type"]
	}
	want := []any{
		"PipelineStarted", "CheckpointSaved",
		"StageStarted", "StageFailed", "StageRetrying", "StageStarted", "StageCompleted", "CheckpointSaved",
		"StageStarted", "ParallelStarted",
		"ParallelBranchStarted", "StageStarted", "StageCompleted", "ParallelBranchCompleted",
		"ParallelBranchStarted", "StageStarted", "StageCompleted", "ParallelBranchCompleted",
		"ParallelCompleted", "StageCompleted", "CheckpointSaved",
		"StageStarted", "StageCompleted", "CheckpointSaved", "PipelineCompleted",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline event types =\n%v\nwant\n%v", got, want)
	}
	branchWorkdirs := map[string]string{}
	for _, event := range events {
		timestamp, ok := event["ts"].(string)
		if !ok {
			t.Fatalf("timeline event has no timestamp: %#v", event)
		}
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			t.Fatalf("timeline timestamp %q: %v", timestamp, err)
		}
		name, _ := event["name"].(string)
		if name == "left" || name == "right" {
			workdir, ok := event["workdir"].(string)
			if event["branch"] != name || !ok || workdir == "" {
				t.Fatalf("branch stage is not attributed: %#v", event)
			}
			if previous := branchWorkdirs[name]; previous != "" && previous != workdir {
				t.Fatalf("branch %q changed workdir from %q to %q", name, previous, workdir)
			}
			branchWorkdirs[name] = workdir
		}
	}
	if len(branchWorkdirs) != 2 || branchWorkdirs["left"] == branchWorkdirs["right"] {
		t.Fatalf("branch workdirs = %#v", branchWorkdirs)
	}
	if events[3]["will_retry"] != true || events[4]["attempt"] != float64(2) {
		t.Fatalf("retry events = %#v, %#v", events[3], events[4])
	}
	if events[9]["branch_count"] != float64(2) || events[18]["branch_count"] != float64(2) {
		t.Fatalf("parallel events = %#v, %#v", events[9], events[18])
	}
}

func TestRunnerTimelineRecordsPipelineFailure(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{}, terminalError("failed deliberately")
	}))
	pipeline := testGraph(startNode("start", "work"), customNode("work", "task", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	result, err := newTestRunner(t, pipeline, registry, root, nil).Run()
	if err != nil || result.Status != RunFailed {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	events, err := readTimeline(filepath.Join(root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last["type"] != "PipelineFailed" || last["error"] != "failed deliberately" {
		t.Fatalf("last timeline event = %#v", last)
	}
}

func TestControlSocketAcceptsSteeringAndAuditsActiveStage(t *testing.T) {
	root := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		close(entered)
		<-release
		return harness.Outcome{Notes: "done"}, nil
	}))
	backend := &steeringBackend{status: harness.SteerAccepted}
	pipeline := testGraph(startNode("start", "work"), customNode("work", "task", []graph.Edge{{To: "done"}}, 0), exitNode("done"))
	pipeline.Name = "steer-test"
	pipeline.Goal = "change direction while work is live"
	runner := newTestRunner(t, pipeline, registry, root, backend)
	finished := runAsync(runner)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not start")
	}

	manifestRaw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest runManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != pipeline.Name || manifest.Goal != pipeline.Goal || manifest.Workdir != runner.config.Workdir ||
		manifest.ID == "" || manifest.StartedAt.IsZero() {
		t.Fatalf("manifest metadata = %#v", manifest)
	}
	parts := []harness.ContentPart{{Type: harness.ContentPartText, Text: "change course"}}
	body, _ := json.Marshal(parts)
	response := unixRequest(t, manifest.ControlSocket, http.MethodPost, "/steer", body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /steer status = %d", response.StatusCode)
	}
	if raw, err := io.ReadAll(response.Body); err != nil || len(raw) != 0 {
		t.Fatalf("POST /steer body = %q, %v", raw, err)
	}
	_ = response.Body.Close()
	close(release)
	assertRunCompleted(t, <-finished)

	if backend.calls.Load() != 1 || !reflect.DeepEqual(backend.lastParts(), parts) {
		t.Fatalf("backend steering = %d, %#v", backend.calls.Load(), backend.lastParts())
	}
	audit, err := os.ReadFile(filepath.Join(root, "stages", "latest", "work", "steering.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Parts []harness.ContentPart `json:"parts"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(audit), &record); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Parts, parts) {
		t.Fatalf("steering audit parts = %#v", record.Parts)
	}
	if _, err := os.Stat(manifest.ControlSocket); !os.IsNotExist(err) {
		t.Fatalf("control socket remains after Run: %v", err)
	}
}

func TestControlRejectsInvalidOrInactiveRequestsBeforeBackend(t *testing.T) {
	root := t.TempDir()
	store, err := openRunStore(root, newEngineState())
	if err != nil {
		t.Fatal(err)
	}
	backend := &steeringBackend{status: harness.SteerAccepted}
	runner := &Runner{config: RunnerConfig{Backend: backend}}
	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodPost, "/steer", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/steer", `[]`, http.StatusBadRequest},
		{http.MethodPost, "/steer", `[null]`, http.StatusBadRequest},
		{http.MethodPost, "/steer", `[{"type":"image","text":"x"}]`, http.StatusBadRequest},
		{http.MethodPost, "/steer", `[{"type":"text","text":"x","extra":true}]`, http.StatusBadRequest},
		{http.MethodPost, "/steer", `[{"type":"text","text":"x"}] {}`, http.StatusBadRequest},
		{http.MethodPost, "/steer", `[{"type":"text","text":"x"}]`, http.StatusConflict},
		{http.MethodGet, "/steer", ``, http.StatusMethodNotAllowed},
		{http.MethodPost, "/other", `[]`, http.StatusNotFound},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		response := httptest.NewRecorder()
		runner.serveControl(store, response, request)
		if response.Code != test.status || response.Body.Len() != 0 {
			t.Errorf("%s %s %q = status %d body %q, want %d empty", test.method, test.path, test.body, response.Code, response.Body.String(), test.status)
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatalf("backend received %d rejected steering calls", backend.calls.Load())
	}
}

func TestControlRejectsTopLevelParallelBeforeBackendHandoff(t *testing.T) {
	repo := newGitTestRepository(t)
	root := t.TempDir()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		entered <- struct{}{}
		<-release
		return harness.Outcome{Notes: "done"}, nil
	}))
	registry.Register("parallel.fan_in", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "joined"}, nil
	}))
	backend := &steeringBackend{status: harness.SteerAccepted}
	runner := newTestRunnerWithWorkdir(t, parallelRunnerGraph([]string{"left"}, 1), registry, root, repo, backend)
	finished := runAsync(runner)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("parallel branch did not start")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest runManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	response := unixRequest(t, manifest.ControlSocket, http.MethodPost, "/steer", []byte(`[{"type":"text","text":"wrong target"}]`))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("parallel steering status = %d", response.StatusCode)
	}
	if backend.calls.Load() != 0 {
		t.Fatalf("parallel steering reached backend %d times", backend.calls.Load())
	}
	close(release)
	assertRunCompleted(t, <-finished)
}

type steeringBackend struct {
	status harness.SteerStatus
	calls  atomic.Int32
	mu     sync.Mutex
	parts  []harness.ContentPart
}

func (*steeringBackend) Run(harness.CodergenTurn) (harness.Outcome, *harness.Error) {
	return harness.Outcome{}, nil
}

func (*steeringBackend) RunSupervisor(harness.SupervisorTurn) (harness.Verdict, *harness.Error) {
	return harness.Verdict{}, nil
}

func (b *steeringBackend) Steer(parts []harness.ContentPart) harness.SteerStatus {
	b.calls.Add(1)
	b.mu.Lock()
	b.parts = append([]harness.ContentPart(nil), parts...)
	b.mu.Unlock()
	return b.status
}

func (*steeringBackend) InterruptAll() {}

func (*steeringBackend) Bindings() map[string]harness.ThreadBinding { return nil }

func (*steeringBackend) SetBindingOpened(harness.BindingOpened) {}

func (b *steeringBackend) lastParts() []harness.ContentPart {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]harness.ContentPart(nil), b.parts...)
}

type runResponse struct {
	result RunResult
	err    error
}

func runAsync(runner *Runner) <-chan runResponse {
	finished := make(chan runResponse, 1)
	go func() {
		result, err := runner.Run()
		finished <- runResponse{result: result, err: err}
	}()
	return finished
}

func assertRunCompleted(t *testing.T, response runResponse) {
	t.Helper()
	if response.err != nil || response.result.Status != RunCompleted {
		t.Fatalf("Run() = %#v, %v", response.result, response.err)
	}
}

func unixRequest(t *testing.T, socketPath, method, path string, body []byte) *http.Response {
	t.Helper()
	transport := &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
		return net.Dial("unix", socketPath)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(fmt.Errorf("control request: %w", err))
	}
	return response
}
