package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

func TestFanInHandlerLoadsEvidenceAndDelegatesExactTurn(t *testing.T) {
	stageDir := prepareFanInStage(t, []BranchResult{
		{
			BranchID: "branch-a", Outcome: &harness.Outcome{Notes: "built A"}, Notes: "built A", Path: []string{"branch-a"},
			Workdir: "/worktrees/a", StageDirs: []string{"/logs/stages/a"}, Segments: []string{"/logs/events/a.jsonl"},
		},
		{
			BranchID: "branch-b", Outcome: &harness.Outcome{Notes: "built B"}, Notes: "built B", Path: []string{"branch-b"},
			Workdir: "/worktrees/b", StageDirs: []string{"/logs/stages/b"}, Segments: []string{"/logs/events/b.jsonl"},
		},
	})
	backend := &captureBackend{outcome: harness.Outcome{Next: "accept", Notes: "selected A"}}
	join := &graph.FanInNode{
		NodeBase: graph.NodeBase{ID: "join"},
		LLMNodeFields: graph.LLMNodeFields{
			Prompt: optional("Compare candidates for $goal"), LLMModel: optional("claude-opus-4-6"),
			ReasoningEffort: optional("medium"), Fidelity: optional("full"), ThreadID: optional("judge"),
			Timeout: optional(graph.Duration("5s")),
		},
	}
	pipeline := fanInGraph(join)
	offered := []graph.Edge{{To: "accept", Condition: "A candidate is ready"}, {To: "retry", Condition: "More work is needed"}}

	outcome, runErr := NewFanInHandler(CodergenConfig{Backend: backend, DefaultModel: "system-model"}).Execute(
		join, offered, ExecutionScope{Workdir: "/main", StageDir: stageDir, RunLog: filepath.Join(stageDir, "events.jsonl"), Goal: "release", Stop: NewStopSignal()}, pipeline,
	)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !reflect.DeepEqual(outcome, backend.outcome) {
		t.Fatalf("outcome = %#v", outcome)
	}
	prompt := "Compare candidates for release\n\n" +
		"Branch ID: branch-a\nNotes: built A\nWorktree: /worktrees/a\n\n" +
		"Branch ID: branch-b\nNotes: built B\nWorktree: /worktrees/b"
	assertTextFile(t, filepath.Join(stageDir, "prompt.md"), prompt)
	assertTextFile(t, filepath.Join(stageDir, "response.md"), "---\nnext: accept\n---\nselected A")
	if len(backend.turns) != 1 {
		t.Fatalf("backend turns = %d", len(backend.turns))
	}
	turn := backend.turns[0]
	if turn.NodeID != "join" || turn.Workdir != "/main" || turn.Model != "claude-opus-4-6" || turn.Provider != "anthropic" ||
		turn.ReasoningEffort != "medium" || turn.Fidelity != harness.FidelityFull || turn.ThreadKey != "judge" || turn.Timeout != 5*time.Second {
		t.Fatalf("fan-in turn = %#v", turn)
	}
	if !reflect.DeepEqual(turn.Parts, []harness.ContentPart{{Type: harness.ContentPartText, Text: prompt}}) {
		t.Fatalf("turn parts = %#v", turn.Parts)
	}
	wantSchema := `{"type":"object","properties":{"next":{"type":"string","enum":["accept","retry"],"description":"Choose the next stage. A candidate is ready: accept; More work is needed: retry"},"notes":{"type":"string","description":"Your account of this stage."}},"required":["next","notes"],"additionalProperties":false}`
	if string(turn.OutputSchema) != wantSchema {
		t.Fatalf("schema = %s", turn.OutputSchema)
	}
}

func TestFanInHandlerUsesDefaultPrompt(t *testing.T) {
	stageDir := prepareFanInStage(t, []BranchResult{{
		BranchID: "branch-a", Outcome: &harness.Outcome{Notes: "done"}, Notes: "done", Path: []string{"branch-a"},
		Workdir: "/worktrees/a", StageDirs: []string{}, Segments: []string{},
	}})
	join := &graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}, LLMNodeFields: graph.LLMNodeFields{Prompt: optional("")}}
	outcome, runErr := NewFanInHandler(CodergenConfig{DefaultModel: "gpt-5.3-codex"}).Execute(
		join, []graph.Edge{{To: "accept"}}, ExecutionScope{Workdir: "/main", StageDir: stageDir, Stop: NewStopSignal()}, fanInGraph(join),
	)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if outcome.Next != "" || outcome.Notes != "[Simulated] Stage completed: join" {
		t.Fatalf("outcome = %#v", outcome)
	}
	assertTextFile(t, filepath.Join(stageDir, "prompt.md"),
		"Evaluate the results of the parallel branches.\n\nBranch ID: branch-a\nNotes: done\nWorktree: /worktrees/a")
}

func TestFanInHandlerRejectsMissingMalformedOrEmptyEvidenceBeforeBackend(t *testing.T) {
	valid := `[{"branch_id":"branch-a","outcome":{"notes":"ok"},"notes":"ok","path":["branch-a"],"workdir":"/wt/a","stage_dirs":[],"segments":[]}]`
	tests := []struct {
		name     string
		evidence *string
		want     string
	}{
		{name: "missing file", want: "no such file"},
		{name: "malformed JSON", evidence: new(`[{`), want: "unexpected end"},
		{name: "empty evidence", evidence: new(`[]`), want: "at least one"},
		{name: "trailing JSON", evidence: new(valid + `{}`), want: "invalid character"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stageDir := prepareFanInStageRaw(t, test.evidence)
			backend := &captureBackend{outcome: harness.Outcome{Notes: "must not run"}}
			join := &graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}}
			_, runErr := NewFanInHandler(CodergenConfig{Backend: backend, DefaultModel: "gpt-5.3-codex"}).Execute(
				join, []graph.Edge{{To: "accept"}}, ExecutionScope{Workdir: "/main", StageDir: stageDir, RunLog: filepath.Join(stageDir, "events.jsonl"), Stop: NewStopSignal()}, fanInGraph(join),
			)
			if runErr == nil || runErr.Category != harness.ErrorTerminal || !strings.Contains(runErr.Message, test.want) {
				t.Fatalf("error = %#v", runErr)
			}
			if len(backend.turns) != 0 {
				t.Fatal("backend ran with invalid evidence")
			}
			if _, err := os.Stat(filepath.Join(stageDir, "prompt.md")); !os.IsNotExist(err) {
				t.Fatalf("prompt exists with invalid evidence: %v", err)
			}
		})
	}
}

func TestFanInHandlerRejectsEvidenceForUnknownBranch(t *testing.T) {
	stageDir := prepareFanInStage(t, []BranchResult{{
		BranchID: "stranger", Outcome: &harness.Outcome{Notes: "done"}, Notes: "done", Path: []string{"stranger"},
		Workdir: "/worktrees/stranger", StageDirs: []string{}, Segments: []string{},
	}})
	join := &graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}}
	backend := &captureBackend{}
	_, runErr := NewFanInHandler(CodergenConfig{Backend: backend, DefaultModel: "gpt-5.3-codex"}).Execute(
		join, []graph.Edge{{To: "accept"}}, ExecutionScope{Workdir: "/main", StageDir: stageDir, RunLog: filepath.Join(stageDir, "events.jsonl"), Stop: NewStopSignal()}, fanInGraph(join),
	)
	if runErr == nil || !strings.Contains(runErr.Message, `unknown branch "stranger"`) || len(backend.turns) != 0 {
		t.Fatalf("error = %#v turns=%d", runErr, len(backend.turns))
	}
}

func TestFanInHandlerRejectsAmbiguousOwnerBeforeBackend(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), "stages", "000003-join")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	join := &graph.FanInNode{NodeBase: graph.NodeBase{ID: "join"}}
	pipeline := &graph.Graph{Nodes: []graph.Node{
		&graph.ParallelNode{NodeBase: graph.NodeBase{ID: "first"}, Branches: []string{"left"}},
		&graph.ParallelNode{NodeBase: graph.NodeBase{ID: "second"}, Branches: []string{"right"}},
		customNode("left", "task", []graph.Edge{{To: "join"}}, 0),
		customNode("right", "task", []graph.Edge{{To: "join"}}, 0),
		join,
	}}
	backend := &captureBackend{}
	_, runErr := NewFanInHandler(CodergenConfig{Backend: backend, DefaultModel: "gpt-5.3-codex"}).Execute(
		join, []graph.Edge{{To: "accept"}}, ExecutionScope{Workdir: "/main", StageDir: stageDir, RunLog: filepath.Join(stageDir, "events.jsonl"), Stop: NewStopSignal()}, pipeline,
	)
	if runErr == nil || !strings.Contains(runErr.Message, "found 2") || len(backend.turns) != 0 {
		t.Fatalf("error = %#v turns=%d", runErr, len(backend.turns))
	}
}

func fanInGraph(join *graph.FanInNode) *graph.Graph {
	return &graph.Graph{Defaults: graph.Defaults{LLMModel: optional("gpt-5.2")}, Nodes: []graph.Node{
		&graph.ParallelNode{NodeBase: graph.NodeBase{ID: "fanout"}, Branches: []string{"branch-a", "branch-b"}},
		customNode("branch-a", "task", []graph.Edge{{To: "join"}}, 0),
		customNode("branch-b", "task", []graph.Edge{{To: "join"}}, 0),
		join,
		exitNode("accept"),
		exitNode("retry"),
	}}
}

func prepareFanInStage(t *testing.T, results []BranchResult) string {
	t.Helper()
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	value := string(raw)
	return prepareFanInStageRaw(t, &value)
}

func prepareFanInStageRaw(t *testing.T, evidence *string) string {
	t.Helper()
	stages := filepath.Join(t.TempDir(), "stages")
	stageDir := filepath.Join(stages, "000002-join")
	parallelStage := filepath.Join(stages, "000001-fanout")
	if err := os.MkdirAll(filepath.Join(stages, "latest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if evidence == nil {
		return stageDir
	}
	if err := os.MkdirAll(parallelStage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parallelStage, "branches.json"), []byte(*evidence), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", filepath.Base(parallelStage)), filepath.Join(stages, "latest", "fanout")); err != nil {
		t.Fatal(err)
	}
	return stageDir
}
