package engine

import (
	"encoding/json"
	"fmt"
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

func TestParallelRunnerIsolatesBranchesCapsConcurrencyAndFinalizesEvidence(t *testing.T) {
	repo := newGitTestRepository(t)
	workdir := filepath.Join(repo, "nested")
	root := t.TempDir()
	branchIDs := []string{"left", "middle", "right"}
	pipeline := parallelRunnerGraph(branchIDs, 2)
	if err := os.MkdirAll(filepath.Join(root, "events"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := appendTestSegmentIndex(root, "left", filepath.Join("events", "old-left.jsonl")); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	entered := make(chan string, len(branchIDs))
	release := make(chan struct{})
	unblock := func() { close(release) }
	var active atomic.Int32
	var peak atomic.Int32
	var indexMu sync.Mutex
	registry.Register("task", HandlerFunc(func(node graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		entered <- node.Base().ID
		<-release

		if filepath.Base(scope.Workdir) != "nested" {
			return harness.Outcome{}, terminalError("branch lost the configured workspace subdirectory")
		}
		if _, err := os.ReadFile(filepath.Join(scope.Workdir, "..", "committed.txt")); err != nil {
			return harness.Outcome{}, terminalError(fmt.Sprintf("read frozen workspace: %v", err))
		}
		product := filepath.Join(scope.Workdir, "product.txt")
		if _, err := os.Stat(product); !os.IsNotExist(err) {
			return harness.Outcome{}, terminalError("branch observed another branch product")
		}
		if err := os.WriteFile(product, []byte(node.Base().ID), 0o644); err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
		segment := filepath.Join("events", node.Base().ID+".jsonl")
		if err := os.WriteFile(filepath.Join(root, segment), []byte("{}\n"), 0o644); err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
		indexMu.Lock()
		err := appendTestSegmentIndex(root, node.Base().ID, segment)
		indexMu.Unlock()
		if err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
		return harness.Outcome{Notes: "finished " + node.Base().ID}, nil
	}))

	var evidence []BranchResult
	registry.Register("parallel.fan_in", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		var err error
		evidence, err = readBranchResults(filepath.Join(root, "stages", "latest", "fanout", "branches.json"))
		if err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
		for _, result := range evidence {
			raw, err := os.ReadFile(filepath.Join(result.Workdir, "product.txt"))
			if err != nil || string(raw) != result.BranchID {
				return harness.Outcome{}, terminalError(fmt.Sprintf("branch product %q: %v", raw, err))
			}
		}
		return harness.Outcome{Notes: "evaluated branches"}, nil
	}))

	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, workdir, nil)
	type runResponse struct {
		result RunResult
		err    error
	}
	finished := make(chan runResponse, 1)
	go func() {
		result, err := runner.Run()
		finished <- runResponse{result: result, err: err}
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			unblock()
			t.Fatal("two branches did not start concurrently")
		}
	}
	select {
	case third := <-entered:
		unblock()
		t.Fatalf("third branch %q exceeded max_parallel", third)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	response := <-finished
	if response.err != nil || response.result.Status != RunCompleted {
		t.Fatalf("result=%#v err=%v", response.result, response.err)
	}
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak.Load())
	}
	if _, err := os.Stat(filepath.Join(workdir, "product.txt")); !os.IsNotExist(err) {
		t.Fatalf("main workspace contains branch product: %v", err)
	}

	if got := branchResultIDs(evidence); !reflect.DeepEqual(got, branchIDs) {
		t.Fatalf("branch order = %v, want %v", got, branchIDs)
	}
	for _, result := range evidence {
		if result.Error != nil || result.Outcome == nil || result.Notes != "finished "+result.BranchID ||
			!reflect.DeepEqual(result.Path, []string{result.BranchID}) || len(result.StageDirs) != 1 ||
			!reflect.DeepEqual(result.Segments, []string{filepath.Join("events", result.BranchID+".jsonl")}) {
			t.Fatalf("branch evidence = %#v", result)
		}
		if filepath.Base(result.StageDirs[0]) == "" || !strings.HasSuffix(filepath.Base(result.StageDirs[0]), "-"+result.BranchID) {
			t.Fatalf("branch stage dir = %q", result.StageDirs[0])
		}
		if _, err := os.Stat(result.Workdir); !os.IsNotExist(err) {
			t.Fatalf("completed branch worktree still exists: %v", err)
		}
	}
	inventory, err := readWorktreeInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range inventory {
		if entry.Path != filepath.Dir(evidence[index].Workdir) {
			t.Fatalf("inventory root %q does not contain branch workdir %q", entry.Path, evidence[index].Workdir)
		}
	}
	checkpoint := mustCheckpoint(t, root)
	if !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"start", "fanout", "join"}) || checkpoint.NodeVisits["fanout"] != 1 {
		t.Fatalf("top-level checkpoint = %#v", checkpoint)
	}
	for _, branchID := range branchIDs {
		if checkpoint.NodeVisits[branchID] != 1 || checkpoint.NodeAttempts[branchID] != 1 {
			t.Fatalf("branch counters for %s = visits %d attempts %d", branchID, checkpoint.NodeVisits[branchID], checkpoint.NodeAttempts[branchID])
		}
	}
	resumed, err := ResumeRunner(pipeline, registry, RunnerConfig{
		LogsRoot: root,
		Workdir:  workdir,
		Validate: func(graph.Graph) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := resumed.Run(); err != nil || result.Status != RunCompleted {
		t.Fatalf("final checkpoint resume=%#v err=%v", result, err)
	}
}

func TestParallelRunnerWritesFailureEvidenceAndRollsBackBranchCounters(t *testing.T) {
	repo := newGitTestRepository(t)
	root := t.TempDir()
	pipeline := parallelRunnerGraph([]string{"left", "right"}, 2)
	for _, node := range pipeline.Nodes {
		if branch, ok := node.(*graph.CustomNode); ok && branch.ID == "left" {
			branch.MaxRetries = jsonschema.Optional[int]{Present: true, Value: 1}
		}
	}
	registry := NewRegistry()
	registry.Register("task", HandlerFunc(func(node graph.Node, _ []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		if node.Base().ID == "left" {
			return harness.Outcome{}, &harness.Error{Category: harness.ErrorRetryable, Message: "transient branch failure"}
		}
		return harness.Outcome{Notes: "right completed"}, nil
	}))
	registry.Register("parallel.fan_in", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{}, terminalError("fan-in ran after branch failure")
	}))
	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, repo, nil)
	runner.retryDelay = func(int) time.Duration { return 0 }
	result, err := runner.Run()
	if err != nil || result.Status != RunFailed || result.FailureReason != "transient branch failure" {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	evidence, err := readBranchResults(filepath.Join(root, "stages", "000002-fanout", "branches.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := branchResultIDs(evidence); !reflect.DeepEqual(got, []string{"left", "right"}) {
		t.Fatalf("branch order = %v", got)
	}
	if evidence[0].Error == nil || evidence[0].Error.Category != harness.ErrorTerminal || len(evidence[0].StageDirs) != 2 {
		t.Fatalf("failed branch evidence = %#v", evidence[0])
	}
	if evidence[1].Outcome == nil || evidence[1].Error != nil {
		t.Fatalf("successful branch evidence = %#v", evidence[1])
	}
	checkpoint := mustCheckpoint(t, root)
	if !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"start"}) || !checkpoint.RetryVisit || checkpoint.NodeVisits["fanout"] != 1 || checkpoint.NodeAttempts["fanout"] != 1 ||
		checkpoint.NodeVisits["left"] != 0 || checkpoint.NodeAttempts["left"] != 0 || checkpoint.NodeVisits["right"] != 0 || checkpoint.NodeAttempts["right"] != 0 {
		t.Fatalf("rolled-back checkpoint = %#v", checkpoint)
	}
	for _, branch := range evidence {
		if _, err := os.Stat(branch.Workdir); err != nil {
			t.Fatalf("failed run removed worktree %q: %v", branch.Workdir, err)
		}
	}
	if err := cleanupBranchWorktrees(repo, root); err != nil {
		t.Fatal(err)
	}
}

func TestParallelRunnerStopInterruptsActiveAndQueuedBranches(t *testing.T) {
	repo := newGitTestRepository(t)
	root := t.TempDir()
	pipeline := parallelRunnerGraph([]string{"left", "right"}, 1)
	registry := NewRegistry()
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	registry.Register("task", HandlerFunc(func(_ graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		calls.Add(1)
		started <- struct{}{}
		scope.Stop.Wait()
		return harness.Outcome{}, &harness.Error{Category: harness.ErrorInterrupted, Message: "cancelled"}
	}))
	registry.Register("parallel.fan_in", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{}, terminalError("fan-in ran after stop")
	}))
	backend := &fakeBackend{}
	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, repo, backend)
	type runResponse struct {
		result RunResult
		err    error
	}
	finished := make(chan runResponse, 1)
	go func() {
		result, err := runner.Run()
		finished <- runResponse{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("branch did not start")
	}
	runner.Stop()
	response := <-finished
	if response.err != nil || response.result.Status != RunFailed || response.result.FailureReason != "cancelled" {
		t.Fatalf("result=%#v err=%v", response.result, response.err)
	}
	if calls.Load() != 1 || backend.interrupts != 1 {
		t.Fatalf("handler calls=%d backend interrupts=%d", calls.Load(), backend.interrupts)
	}
	evidence, err := readBranchResults(filepath.Join(root, "stages", "000002-fanout", "branches.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range evidence {
		if branch.Error == nil || branch.Error.Category != harness.ErrorInterrupted {
			t.Fatalf("interrupted evidence = %#v", branch)
		}
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NodeVisits["fanout"] != 1 || checkpoint.NodeVisits["left"] != 0 || checkpoint.NodeVisits["right"] != 0 {
		t.Fatalf("stop checkpoint = %#v", checkpoint)
	}
	if err := cleanupBranchWorktrees(repo, root); err != nil {
		t.Fatal(err)
	}
}

func parallelRunnerGraph(branchIDs []string, maxParallel int) graph.Graph {
	edges := make([]graph.Edge, len(branchIDs))
	nodes := make([]graph.Node, 0, len(branchIDs)+4)
	nodes = append(nodes, startNode("start", "fanout"))
	parallel := &graph.ParallelNode{NodeBase: graph.NodeBase{ID: "fanout"}}
	parallel.MaxParallel = jsonschema.Optional[int]{Present: true, Value: maxParallel}
	for index, branchID := range branchIDs {
		edges[index] = graph.Edge{To: branchID}
	}
	parallel.Edges = edges
	nodes = append(nodes, parallel)
	for _, branchID := range branchIDs {
		nodes = append(nodes, customNode(branchID, "task", []graph.Edge{{To: "join"}}, 0))
	}
	nodes = append(nodes,
		&graph.FanInNode{NodeBase: graph.NodeBase{ID: "join", Edges: []graph.Edge{{To: "done"}}}},
		exitNode("done"),
	)
	return testGraph(nodes...)
}

func newTestRunnerWithWorkdir(t *testing.T, pipeline graph.Graph, registry *Registry, root, workdir string, backend harness.CodergenBackend) *Runner {
	t.Helper()
	runner, err := NewRunner(pipeline, registry, RunnerConfig{
		LogsRoot: root,
		Workdir:  workdir,
		Validate: func(graph.Graph) error { return nil },
		Backend:  backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func appendTestSegmentIndex(root, nodeID, path string) error {
	file, err := os.OpenFile(filepath.Join(root, "events", "index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(map[string]any{"seq": 1, "node_id": nodeID, "path": path, "ts": time.Now().UTC()})
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func branchResultIDs(results []BranchResult) []string {
	ids := make([]string, len(results))
	for index, result := range results {
		ids[index] = result.BranchID
	}
	return ids
}
