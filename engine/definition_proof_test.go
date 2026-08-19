package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

func TestDefinitionProofPipelineWithTenWorkNodesCompletes(t *testing.T) {
	const workNodes = 10
	nodes := make([]graph.Node, 0, workNodes+2)
	nodes = append(nodes, startNode("start", "work-01"))
	for index := 1; index <= workNodes; index++ {
		next := "done"
		if index < workNodes {
			next = fmt.Sprintf("work-%02d", index+1)
		}
		nodes = append(nodes, customNode(fmt.Sprintf("work-%02d", index), "proof", []graph.Edge{{To: next}}, 0))
	}
	nodes = append(nodes, exitNode("done"))

	registry := NewRegistry()
	var executed []string
	registry.Register("codergen", HandlerFunc(func(node graph.Node, _ []graph.Edge, _ ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		executed = append(executed, node.Base().ID)
		return harness.Outcome{Notes: "completed " + node.Base().ID}, nil
	}))
	root := shortProofTempDir(t)
	result, err := newTestRunner(t, testGraph(nodes...), registry, root, nil).Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(executed) != workNodes || executed[0] != "work-01" || executed[workNodes-1] != "work-10" {
		t.Fatalf("executed nodes = %v", executed)
	}
	checkpoint := mustCheckpoint(t, root)
	if len(checkpoint.CompletedNodes) != workNodes || checkpoint.CurrentNode != "work-10" || checkpoint.NextNode != graph.Success {
		t.Fatalf("final checkpoint = %#v", checkpoint)
	}
	for _, nodeID := range executed {
		if checkpoint.NodeVisits[nodeID] != 1 || checkpoint.NodeAttempts[nodeID] != 1 {
			t.Fatalf("counters for %s: visits=%d attempts=%d", nodeID, checkpoint.NodeVisits[nodeID], checkpoint.NodeAttempts[nodeID])
		}
	}
}

func TestDefinitionProofRunnerCodergenChoiceSchemaRoutesThroughHarnessBackend(t *testing.T) {
	root := shortProofTempDir(t)
	workdir := t.TempDir()
	adapter := &proofAdapter{name: "chooser", run: func(input harness.RunTurnInput) (harness.Result, *harness.Error) {
		var schema struct {
			Properties struct {
				Next struct {
					Enum []string `json:"enum"`
				} `json:"next"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties bool     `json:"additionalProperties"`
		}
		if err := json.Unmarshal(input.OutputSchema, &schema); err != nil {
			return nil, terminalError("decode choice schema: " + err.Error())
		}
		if !reflect.DeepEqual(schema.Properties.Next.Enum, []string{"left", "right"}) ||
			!reflect.DeepEqual(schema.Required, []string{"next", "notes"}) || schema.AdditionalProperties {
			return nil, terminalError(fmt.Sprintf("unexpected choice schema: %s", input.OutputSchema))
		}
		return harness.Result{"next": "right", "notes": "the scripted reviewer chose right"}, nil
	}}
	backend, backendErr := harness.NewHarnessBackend(
		root,
		map[string]harness.HarnessAdapter{"scripted": adapter},
		map[string]string{"proof": "scripted"},
		nil,
	)
	if backendErr != nil {
		t.Fatal(backendErr)
	}
	pipeline := testGraph(
		startNode("start", "choose"),
		&graph.CodergenNode{
			NodeBase: graph.NodeBase{ID: "choose"},
			Edges: []graph.Edge{
				{To: "left", Condition: "choose the left result"},
				{To: "right", Condition: "choose the right result"},
			},
			LLMNodeFields: graph.LLMNodeFields{Prompt: optional("Select the better result")},
		},
		&graph.ToolNode{NodeBase: graph.NodeBase{ID: "left"}, ToolCommand: "true", OnSuccess: graph.Success},
		&graph.ToolNode{NodeBase: graph.NodeBase{ID: "right"}, ToolCommand: "true", OnSuccess: graph.Success},
	)
	registry := NewRegistry()
	registry.Register("codergen", NewCodergenHandler(CodergenConfig{
		Backend:                backend,
		DefaultModel:           "proof-model",
		DefaultProvider:        "proof",
		DefaultReasoningEffort: "high",
	}))
	runner := newTestRunnerWithWorkdir(t, pipeline, registry, root, workdir, backend)
	result, err := runner.Run()
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.CurrentNode != "right" || !reflect.DeepEqual(checkpoint.CompletedNodes, []string{"choose", "right"}) {
		t.Fatalf("choice checkpoint = %#v", checkpoint)
	}
	inputs := adapter.inputsSnapshot()
	if len(inputs) != 1 || inputs[0].SessionID == "" || inputs[0].Workdir != workdir || inputs[0].Parts[0].Text != "Select the better result" {
		t.Fatalf("adapter inputs = %#v", inputs)
	}
}

func TestDefinitionProofSideEffectReplaysAcrossResume(t *testing.T) {
	root := shortProofTempDir(t)
	workdir := t.TempDir()
	sends := filepath.Join(t.TempDir(), "sends.jsonl")
	pipeline := testGraph(
		startNode("start", "send"),
		customNode("send", "send", []graph.Edge{{To: "done"}}, 0),
		exitNode("done"),
	)

	firstScope := ExecutionScope{}
	firstRegistry := NewRegistry()
	firstRegistry.Register("codergen", HandlerFunc(func(_ graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		firstScope = scope
		if err := appendProofLine(sends, map[string]string{"delivery": "sent", "workdir": scope.Workdir}); err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
		return harness.Outcome{}, terminalError("failed after send")
	}))
	first := newTestRunnerWithWorkdir(t, pipeline, firstRegistry, root, workdir, nil)
	if result, err := first.Run(); err != nil || result.Status != RunFailed || result.FailureReason != "failed after send" {
		t.Fatalf("first result=%#v err=%v", result, err)
	}

	secondScope := ExecutionScope{}
	secondRegistry := NewRegistry()
	secondRegistry.Register("codergen", HandlerFunc(func(_ graph.Node, _ []graph.Edge, scope ExecutionScope, _ *graph.Graph) (harness.Outcome, *harness.Error) {
		secondScope = scope
		if err := appendProofLine(sends, map[string]string{"delivery": "sent", "workdir": scope.Workdir}); err != nil {
			return harness.Outcome{}, terminalError(err.Error())
		}
		return harness.Outcome{Notes: "send completed on replay"}, nil
	}))
	resumed, err := ResumeRunner(pipeline, secondRegistry, RunnerConfig{
		LogsRoot: root,
		Workdir:  workdir,
		Validate: func(graph.Graph) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, runErr := resumed.Run(); runErr != nil || result.Status != RunCompleted {
		t.Fatalf("resume result=%#v err=%v", result, runErr)
	}

	lines := readProofLines(t, sends)
	if len(lines) != 2 || lines[0]["delivery"] != "sent" || lines[1]["delivery"] != "sent" {
		t.Fatalf("observable sends = %#v", lines)
	}
	if firstScope.Workdir != secondScope.Workdir || firstScope.Goal != secondScope.Goal || firstScope.Stop == nil || secondScope.Stop == nil {
		t.Fatalf("execution scopes differ beyond stage identity: first=%#v second=%#v", firstScope, secondScope)
	}
	if firstScope.StageDir == secondScope.StageDir {
		t.Fatalf("replay reused stage directory %q", firstScope.StageDir)
	}
	checkpoint := mustCheckpoint(t, root)
	if checkpoint.NodeVisits["send"] != 1 || checkpoint.NodeAttempts["send"] != 2 {
		t.Fatalf("replay counters = visits %d attempts %d", checkpoint.NodeVisits["send"], checkpoint.NodeAttempts["send"])
	}
}

func TestDefinitionProofParallelReplayUsesFreshWorktreeAndReboundSession(t *testing.T) {
	repo := newGitTestRepository(t)
	root := shortProofTempDir(t)
	sends := filepath.Join(t.TempDir(), "sends.jsonl")
	parallel := &graph.ParallelNode{
		NodeBase:    graph.NodeBase{ID: "fanout"},
		Branches:    graph.LegacyParallelBranches("send", "other"),
		MaxParallel: jsonschema.Optional[int]{Present: true, Value: 2},
	}
	pipeline := testGraph(
		startNode("start", "fanout"),
		parallel,
		&graph.CodergenNode{
			NodeBase: graph.NodeBase{ID: "send"},
			Edges:    []graph.Edge{{To: "join"}},
			LLMNodeFields: graph.LLMNodeFields{
				Prompt:   optional("perform the observable send"),
				Fidelity: optional("full"),
			},
		},
		&graph.ToolNode{NodeBase: graph.NodeBase{ID: "other"}, ToolCommand: "true", OnSuccess: "join"},
		&graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}, Edges: []graph.Edge{{To: "done"}}},
		exitNode("done"),
	)

	firstAdapter := &proofAdapter{name: "first", run: func(input harness.RunTurnInput) (harness.Result, *harness.Error) {
		marker := filepath.Join(input.Workdir, "delivery.marker")
		_, markerErr := os.Stat(marker)
		if err := appendProofLine(sends, map[string]string{
			"session":        input.SessionID,
			"workdir":        input.Workdir,
			"marker_present": fmt.Sprint(markerErr == nil),
		}); err != nil {
			return nil, terminalError(err.Error())
		}
		if err := os.WriteFile(marker, []byte("sent"), 0o644); err != nil {
			return nil, terminalError(err.Error())
		}
		return nil, terminalError("branch failed after send")
	}}
	firstBackend := newProofHarnessBackend(t, root, firstAdapter, nil)
	firstRegistry := replayProofRegistry(firstBackend)
	first := newTestRunnerWithWorkdir(t, pipeline, firstRegistry, root, repo, firstBackend)
	if result, err := first.Run(); err != nil || result.Status != RunFailed || result.FailureReason != "branch failed after send" {
		t.Fatalf("first result=%#v err=%v", result, err)
	}
	failedCheckpoint := mustCheckpoint(t, root)
	staleBinding, ok := failedCheckpoint.Sessions["send"]
	if !ok || staleBinding.SessionID == "" || staleBinding.Workdir == "" {
		t.Fatalf("failed checkpoint binding = %#v", failedCheckpoint.Sessions)
	}
	if raw, err := os.ReadFile(filepath.Join(staleBinding.Workdir, "delivery.marker")); err != nil || string(raw) != "sent" {
		t.Fatalf("failed branch marker = %q err=%v", raw, err)
	}

	secondAdapter := &proofAdapter{name: "second", run: func(input harness.RunTurnInput) (harness.Result, *harness.Error) {
		marker := filepath.Join(input.Workdir, "delivery.marker")
		_, markerErr := os.Stat(marker)
		if err := appendProofLine(sends, map[string]string{
			"session":        input.SessionID,
			"workdir":        input.Workdir,
			"marker_present": fmt.Sprint(markerErr == nil),
		}); err != nil {
			return nil, terminalError(err.Error())
		}
		if err := os.WriteFile(marker, []byte("sent again"), 0o644); err != nil {
			return nil, terminalError(err.Error())
		}
		return harness.Result{"notes": "send completed on replay"}, nil
	}}
	secondBackend := newProofHarnessBackend(t, root, secondAdapter, failedCheckpoint.Sessions)
	secondRegistry := replayProofRegistry(secondBackend)
	resumed, err := ResumeRunner(pipeline, secondRegistry, RunnerConfig{
		LogsRoot: root,
		Workdir:  repo,
		Validate: func(graph.Graph) error { return nil },
		Backend:  secondBackend,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, runErr := resumed.Run(); runErr != nil || result.Status != RunCompleted {
		t.Fatalf("resume result=%#v err=%v", result, runErr)
	}

	lines := readProofLines(t, sends)
	if len(lines) != 2 {
		t.Fatalf("observable sends = %#v", lines)
	}
	if lines[0]["marker_present"] != "false" || lines[1]["marker_present"] != "false" {
		t.Fatalf("branch-local marker carried into a replay: %#v", lines)
	}
	if lines[0]["workdir"] == lines[1]["workdir"] || lines[0]["session"] == lines[1]["session"] {
		t.Fatalf("replay did not replace worktree and session: %#v", lines)
	}
	if got := secondAdapter.createdSessions(); !reflect.DeepEqual(got, []string{"second-1"}) {
		t.Fatalf("resume session creations = %v", got)
	}
	finalBinding := secondBackend.Bindings()["send"]
	if finalBinding.SessionID != "second-1" || finalBinding.Workdir != lines[1]["workdir"] {
		t.Fatalf("rebound session = %#v, sends=%#v", finalBinding, lines)
	}
}

func replayProofRegistry(backend harness.CodergenBackend) *Registry {
	registry := NewRegistry()
	config := CodergenConfig{
		Backend:                backend,
		DefaultModel:           "proof-model",
		DefaultProvider:        "proof",
		DefaultReasoningEffort: "high",
	}
	registry.Register("codergen", NewCodergenHandler(config))
	registry.Register("parallel.fan_in", HandlerFunc(func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
		return harness.Outcome{Notes: "fan-in completed"}, nil
	}))
	return registry
}

func newProofHarnessBackend(t *testing.T, root string, adapter harness.HarnessAdapter, bindings map[string]harness.ThreadBinding) *harness.HarnessBackend {
	t.Helper()
	backend, err := harness.NewHarnessBackend(
		root,
		map[string]harness.HarnessAdapter{"scripted": adapter},
		map[string]string{"proof": "scripted"},
		bindings,
	)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

type proofAdapter struct {
	name string
	run  func(harness.RunTurnInput) (harness.Result, *harness.Error)

	mu      sync.Mutex
	next    int
	created []string
	inputs  []harness.RunTurnInput
}

func (a *proofAdapter) CreateSession(_, _ string) (string, *harness.Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	sessionID := fmt.Sprintf("%s-%d", a.name, a.next)
	a.created = append(a.created, sessionID)
	return sessionID, nil
}

func (a *proofAdapter) RunTurn(input harness.RunTurnInput, _ harness.OnEvent) (harness.Result, *harness.Error) {
	a.mu.Lock()
	a.inputs = append(a.inputs, input)
	run := a.run
	a.mu.Unlock()
	return run(input)
}

func (*proofAdapter) Steer(string, []harness.ContentPart)   {}
func (*proofAdapter) Interrupt(string)                      {}
func (*proofAdapter) Compact(string, string) *harness.Error { return nil }

func (a *proofAdapter) createdSessions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.created...)
}

func (a *proofAdapter) inputsSnapshot() []harness.RunTurnInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]harness.RunTurnInput(nil), a.inputs...)
}

func appendProofLine(path string, value map[string]string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readProofLines(t *testing.T, path string) []map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close proof log: %v", err)
		}
	}()
	var lines []map[string]string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line map[string]string
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func shortProofTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tractor-proof-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove proof temp dir: %v", err)
		}
	})
	return dir
}

var _ harness.HarnessAdapter = (*proofAdapter)(nil)

func TestDefinitionProofExecutionScopeHasNoReplayFlag(t *testing.T) {
	typeOfScope := reflect.TypeFor[ExecutionScope]()
	fields := make([]string, typeOfScope.NumField())
	for index := range typeOfScope.NumField() {
		fields[index] = typeOfScope.Field(index).Name
	}
	if got := strings.Join(fields, ","); got != "Workdir,StageDir,RunLog,Goal,Stop" {
		t.Fatalf("ExecutionScope exposes replay metadata: %s", got)
	}
}
