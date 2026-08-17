package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

func TestToolHandlerRunsShellInWorkdirAndCombinesOutput(t *testing.T) {
	workdir := t.TempDir()
	stageDir := t.TempDir()
	node := toolNode("pwd; printf 'stdout-line\n'; printf 'stderr-line\n' >&2", "done")

	outcome, runErr := toolHandler(node, node.Edges, toolScope(workdir, stageDir, nil), nil)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if outcome.Next != "done" || !strings.Contains(outcome.Notes, "exit 0:") || !strings.Contains(outcome.Notes, "stderr-line") {
		t.Fatalf("outcome = %#v", outcome)
	}
	log := readToolLog(t, stageDir)
	for _, want := range []string{workdir, "stdout-line", "stderr-line"} {
		if !strings.Contains(log, want) {
			t.Fatalf("tool.log %q does not contain %q", log, want)
		}
	}
}

func TestNewRegistryIncludesToolHandler(t *testing.T) {
	handler, runErr := NewRegistry().Resolve(toolNode("true", "done"))
	if runErr != nil || handler == nil {
		t.Fatalf("handler = %#v, error = %#v", handler, runErr)
	}
}

func TestToolHandlerExitCodeRouting(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		edges       []graph.Edge
		onFail      string
		wantNext    string
		wantMessage string
	}{
		{name: "one edge success", command: "exit 0", edges: []graph.Edge{{To: "done"}}, wantNext: "done"},
		{name: "one edge assertion failure", command: "exit 7", edges: []graph.Edge{{To: "done"}}, wantMessage: "exited 7"},
		{name: "one edge advisory failure", command: "exit 7", edges: []graph.Edge{{To: "done"}}, onFail: "done", wantNext: "done"},
		{name: "two edge success", command: "exit 0", edges: []graph.Edge{{To: "passed"}, {To: "failed"}}, onFail: "failed", wantNext: "passed"},
		{name: "two edge failure", command: "exit 9", edges: []graph.Edge{{To: "passed"}, {To: "failed"}}, onFail: "failed", wantNext: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := &graph.ToolNode{
				NodeBase:    graph.NodeBase{ID: "tool", Edges: test.edges},
				ToolCommand: test.command,
				OnFail:      test.onFail,
			}
			stageDir := t.TempDir()
			outcome, runErr := toolHandler(node, test.edges, toolScope(t.TempDir(), stageDir, nil), nil)
			if test.wantMessage != "" {
				if runErr == nil || runErr.Category != harness.ErrorTerminal || !strings.Contains(runErr.Message, test.wantMessage) {
					t.Fatalf("error = %#v", runErr)
				}
				return
			}
			if runErr != nil || outcome.Next != test.wantNext {
				t.Fatalf("outcome = %#v; error = %#v", outcome, runErr)
			}
			if !strings.HasPrefix(outcome.Notes, "exit ") {
				t.Fatalf("notes = %q", outcome.Notes)
			}
		})
	}
}

func TestToolHandlerTimeoutInterruptsPromptly(t *testing.T) {
	node := toolNode("sleep 10", "done")
	node.Timeout = jsonschema.Optional[graph.Duration]{Present: true, Value: "50ms"}
	started := time.Now()
	_, runErr := toolHandler(node, node.Edges, toolScope(t.TempDir(), t.TempDir(), nil), nil)
	assertPromptInterruption(t, started, runErr, "timed out")
}

func TestToolHandlerStopInterruptsPromptly(t *testing.T) {
	stop := NewStopSignal()
	node := toolNode("(sleep 0.2; touch descendant-ran) & printf 'started\n'; wait", "done")
	stageDir := t.TempDir()
	workdir := t.TempDir()
	type response struct {
		outcome harness.Outcome
		err     *harness.Error
	}
	finished := make(chan response, 1)
	started := time.Now()
	go func() {
		outcome, runErr := toolHandler(node, node.Edges, toolScope(workdir, stageDir, stop), nil)
		finished <- response{outcome: outcome, err: runErr}
	}()
	waitForToolOutput(t, filepath.Join(stageDir, "tool.log"), "started")
	stop.Stop()
	select {
	case result := <-finished:
		if result.outcome != (harness.Outcome{}) {
			t.Fatalf("outcome = %#v", result.outcome)
		}
		assertPromptInterruption(t, started, result.err, "stopped by operator")
	case <-time.After(2 * time.Second):
		t.Fatal("tool handler did not return promptly after stop")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workdir, "descendant-ran")); !os.IsNotExist(err) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}

func TestToolHandlerRejectsExhaustedMechanicalRoute(t *testing.T) {
	tests := []struct {
		name    string
		command string
		offered []graph.Edge
		want    string
	}{
		{name: "success route", command: "exit 0", offered: []graph.Edge{{To: "failed"}}, want: "passed"},
		{name: "failure route", command: "exit 3", offered: []graph.Edge{{To: "passed"}}, want: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := &graph.ToolNode{
				NodeBase:    graph.NodeBase{ID: "tool", Edges: []graph.Edge{{To: "passed"}, {To: "failed"}}},
				ToolCommand: test.command,
				OnFail:      "failed",
			}
			_, runErr := toolHandler(node, test.offered, toolScope(t.TempDir(), t.TempDir(), nil), nil)
			if runErr == nil || runErr.Category != harness.ErrorTerminal ||
				runErr.Message != "exit-code route "+test.want+" has exhausted its visit budget" {
				t.Fatalf("error = %#v", runErr)
			}
		})
	}
}

func TestToolHandlerRejectsInvalidInputs(t *testing.T) {
	t.Run("empty command", func(t *testing.T) {
		node := toolNode(" ", "done")
		_, runErr := toolHandler(node, node.Edges, toolScope(t.TempDir(), t.TempDir(), nil), nil)
		if runErr == nil || runErr.Category != harness.ErrorTerminal || !strings.Contains(runErr.Message, "no tool_command") {
			t.Fatalf("error = %#v", runErr)
		}
	})

	t.Run("invalid timeout", func(t *testing.T) {
		node := toolNode("true", "done")
		node.Timeout = jsonschema.Optional[graph.Duration]{Present: true, Value: "soon"}
		_, runErr := toolHandler(node, node.Edges, toolScope(t.TempDir(), t.TempDir(), nil), nil)
		if runErr == nil || runErr.Category != harness.ErrorTerminal || !strings.Contains(runErr.Message, "parse tool timeout") {
			t.Fatalf("error = %#v", runErr)
		}
	})

	t.Run("already stopped", func(t *testing.T) {
		stop := NewStopSignal()
		stop.Stop()
		node := toolNode("touch should-not-exist", "done")
		workdir := t.TempDir()
		_, runErr := toolHandler(node, node.Edges, toolScope(workdir, t.TempDir(), stop), nil)
		if runErr == nil || runErr.Category != harness.ErrorInterrupted {
			t.Fatalf("error = %#v", runErr)
		}
		if _, err := os.Stat(filepath.Join(workdir, "should-not-exist")); !os.IsNotExist(err) {
			t.Fatalf("command ran after stop: %v", err)
		}
	})
}

func toolNode(command, target string) *graph.ToolNode {
	return &graph.ToolNode{
		NodeBase:    graph.NodeBase{ID: "tool", Edges: []graph.Edge{{To: target}}},
		ToolCommand: command,
	}
}

func toolScope(workdir, stageDir string, stop *StopSignal) ExecutionScope {
	if stop == nil {
		stop = NewStopSignal()
	}
	return ExecutionScope{Workdir: workdir, StageDir: stageDir, Stop: stop}
}

func readToolLog(t *testing.T, stageDir string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(stageDir, "tool.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func waitForToolOutput(t *testing.T, path, text string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(contents), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s did not contain %q", path, text)
}

func assertPromptInterruption(t *testing.T, started time.Time, runErr *harness.Error, message string) {
	t.Helper()
	if runErr == nil || runErr.Category != harness.ErrorInterrupted || !strings.Contains(runErr.Message, message) {
		t.Fatalf("error = %#v", runErr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("interruption took %s", elapsed)
	}
}
