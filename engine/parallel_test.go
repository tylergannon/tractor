package engine

import (
	"bytes"
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
	"github.com/tylergannon/tractor/lint"
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
	registry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
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
			!reflect.DeepEqual(result.Path, []string{result.BranchID}) || len(result.StageDirs) != 1 || len(result.Segments) != 0 {
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
	if !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"fanout", "join"}) || checkpoint.NodeVisits["fanout"] != 1 {
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
		if branch, ok := node.(*graph.CodergenNode); ok && branch.ID == "left" {
			branch.MaxRetries = jsonschema.Optional[int]{Present: true, Value: 1}
		}
	}
	registry := NewRegistry()
	registry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
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

	evidence, err := readBranchResults(filepath.Join(root, "stages", "000001-fanout", "branches.json"))
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
	if len(checkpoint.CompletedNodes) != 0 || !checkpoint.RetryVisit || checkpoint.NodeVisits["fanout"] != 1 || checkpoint.NodeAttempts["fanout"] != 1 ||
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

func TestParallelRunnerResolvesHeterogeneousCodergenBranchesAndGathersArtifacts(t *testing.T) {
	pipeline := parseParallelTestGraph(t, `{
  "start":"fanout",
  "nodes":[
    {
      "id":"fanout","type":"parallel","max_parallel":1,
      "prompt":"parent prompt","llm_provider":"openai","llm_model":"gpt-parent","reasoning_effort":"high",
      "edges":[{"to":"join"}],
      "branches":[
        {"id":"openai_branch","artifacts":["openai.txt"],"codergen":{"prompt":"openai prompt"}},
        {"id":"anthropic_branch","artifacts":["anthropic.txt"],"codergen":{"llm_provider":"anthropic","llm_model":"claude-child","reasoning_effort":"medium"}}
      ]
    },
    {"id":"join","type":"parallel.fan_in","prompt":"inspect artifacts","edges":[{"to":"success"}]}
  ]
}`)
	repo := newGitTestRepository(t)
	root := t.TempDir()
	backend := &artifactCaptureBackend{artifacts: map[string]string{
		"openai_branch":    "openai.txt",
		"anthropic_branch": "anthropic.txt",
	}}
	registry := NewRegistry()
	config := CodergenConfig{Backend: backend, DefaultModel: "gpt-fan-in"}
	registry.Register("codergen", NewCodergenHandler(config))
	registry.Register("parallel.fan_in", NewFanInHandler(config))
	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, repo, backend)

	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	turns := backend.snapshot()
	if len(turns) != 3 {
		t.Fatalf("turns = %#v", turns)
	}
	if turns[0].NodeID != "openai_branch" || turns[0].Provider != "openai" || turns[0].Model != "gpt-parent" || turns[0].ReasoningEffort != "high" || turns[0].Parts[0].Text != "openai prompt" {
		t.Fatalf("inherited turn = %#v", turns[0])
	}
	if turns[1].NodeID != "anthropic_branch" || turns[1].Provider != "anthropic" || turns[1].Model != "claude-child" || turns[1].ReasoningEffort != "medium" || turns[1].Parts[0].Text != "parent prompt" {
		t.Fatalf("overridden turn = %#v", turns[1])
	}
	evidence, err := readBranchResults(filepath.Join(root, "stages", "latest", "fanout", "branches.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range evidence {
		if branch.Workspace != "isolated" || len(branch.Artifacts) != 1 {
			t.Fatalf("branch evidence = %#v", branch)
		}
		artifact := branch.Artifacts[0]
		if !strings.Contains(artifact.Path, filepath.Join("artifacts", branch.BranchID)) {
			t.Fatalf("gathered artifact path = %q", artifact.Path)
		}
		raw, err := os.ReadFile(artifact.Path)
		if err != nil || string(raw) != branch.BranchID {
			t.Fatalf("gathered artifact = %q, %v", raw, err)
		}
		resolved, err := os.ReadFile(filepath.Join(branch.StageDirs[0], "resolved.json"))
		if err != nil || !bytes.Contains(resolved, []byte(`"type": "codergen"`)) || !bytes.Contains(resolved, []byte(`"id": "`+branch.BranchID+`"`)) {
			t.Fatalf("resolved runtime branch = %s, %v", resolved, err)
		}
	}
	resolvedRaw, err := os.ReadFile(filepath.Join(root, "stages", "latest", "fanout", "resolved-branches.json"))
	if err != nil || !bytes.Contains(resolvedRaw, []byte(`"type": "codergen"`)) || !bytes.Contains(resolvedRaw, []byte(`"llm_model": "claude-child"`)) {
		t.Fatalf("resolved branches = %s, %v", resolvedRaw, err)
	}
}

func TestParallelRunnerSharedWorkspaceExposesDeclaredArtifactsToFanIn(t *testing.T) {
	pipeline := parseParallelTestGraph(t, `{
  "start":"fanout",
  "nodes":[
    {
      "id":"fanout","type":"parallel","workspace":"shared","max_parallel":2,
      "prompt":"write your artifact","llm_provider":"openai","llm_model":"gpt-shared",
      "edges":[{"to":"join"}],
      "branches":[
        {"id":"left","artifacts":["left.txt"]},
        {"id":"right","artifacts":["right.txt"]}
      ]
    },
    {"id":"join","type":"parallel.fan_in","prompt":"expose all artifacts","edges":[{"to":"success"}]}
  ]
}`)
	workdir := t.TempDir()
	root := t.TempDir()
	backend := &artifactCaptureBackend{artifacts: map[string]string{"left": "left.txt", "right": "right.txt"}}
	registry := NewRegistry()
	config := CodergenConfig{Backend: backend, DefaultModel: "gpt-fan-in"}
	registry.Register("codergen", NewCodergenHandler(config))
	registry.Register("parallel.fan_in", NewFanInHandler(config))
	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, workdir, backend)

	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, artifact := range []string{"left.txt", "right.txt"} {
		raw, err := os.ReadFile(filepath.Join(workdir, artifact))
		if err != nil || strings.TrimSuffix(artifact, ".txt") != string(raw) {
			t.Fatalf("shared artifact %s = %q, %v", artifact, raw, err)
		}
	}
	if inventory, err := readWorktreeInventory(root); err != nil || len(inventory) != 0 {
		t.Fatalf("shared worktree inventory = %#v, %v", inventory, err)
	}
	turns := backend.snapshot()
	join := turns[len(turns)-1]
	if join.NodeID != "join" || !strings.Contains(join.Parts[0].Text, filepath.Join(workdir, "left.txt")) || !strings.Contains(join.Parts[0].Text, filepath.Join(workdir, "right.txt")) {
		t.Fatalf("fan-in prompt = %q", join.Parts[0].Text)
	}
}

func TestParallelRunnerStopInterruptsActiveAndQueuedBranches(t *testing.T) {
	repo := newGitTestRepository(t)
	root := t.TempDir()
	pipeline := parallelRunnerGraph([]string{"left", "right"}, 1)
	registry := NewRegistry()
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	registry.Register("codergen", HandlerFunc(func(_ graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
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
	evidence, err := readBranchResults(filepath.Join(root, "stages", "000001-fanout", "branches.json"))
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
	nodes := make([]graph.Node, 0, len(branchIDs)+4)
	nodes = append(nodes, startNode("start", "fanout"))
	parallel := &graph.ParallelNode{NodeBase: graph.NodeBase{ID: "fanout"}}
	parallel.MaxParallel = jsonschema.Optional[int]{Present: true, Value: maxParallel}
	parallel.Branches = graph.LegacyParallelBranches(branchIDs...)
	nodes = append(nodes, parallel)
	for _, branchID := range branchIDs {
		nodes = append(nodes, customNode(branchID, "task", []graph.Edge{{To: "join"}}, 0))
	}
	nodes = append(nodes,
		&graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}, Edges: []graph.Edge{{To: "done"}}},
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

func parseParallelTestGraph(t *testing.T, document string) graph.Graph {
	t.Helper()
	pipeline, err := graph.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := lint.Validate(*pipeline); lint.HasErrors(diagnostics) {
		t.Fatalf("lint diagnostics = %#v", diagnostics)
	}
	return *pipeline
}

type artifactCaptureBackend struct {
	mu        sync.Mutex
	turns     []harness.CodergenTurn
	artifacts map[string]string
}

func (b *artifactCaptureBackend) Run(turn harness.CodergenTurn) (harness.Outcome, *harness.Error) {
	b.mu.Lock()
	b.turns = append(b.turns, turn)
	b.mu.Unlock()
	if artifact := b.artifacts[turn.NodeID]; artifact != "" {
		if err := os.WriteFile(filepath.Join(turn.Workdir, artifact), []byte(turn.NodeID), 0o644); err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
	}
	return harness.Outcome{Notes: "completed " + turn.NodeID}, nil
}

func (*artifactCaptureBackend) RunSupervisor(harness.SupervisorTurn) (harness.Verdict, *harness.Error) {
	return harness.Verdict{}, nil
}

func (*artifactCaptureBackend) Steer([]harness.ContentPart) harness.SteerStatus {
	return harness.SteerNotActive
}

func (*artifactCaptureBackend) InterruptAll() {}

func (*artifactCaptureBackend) Bindings() map[string]harness.ThreadBinding { return nil }

func (*artifactCaptureBackend) SetBindingOpened(harness.BindingOpened) {}

func (b *artifactCaptureBackend) snapshot() []harness.CodergenTurn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]harness.CodergenTurn(nil), b.turns...)
}
