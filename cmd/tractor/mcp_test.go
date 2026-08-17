package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tylergannon/tractor/engine"
	"github.com/tylergannon/tractor/graph"
)

const slowPipeline = `{"name":"slow","nodes":[{"id":"start","type":"start","edges":[{"to":"wait"}]},{"id":"wait","type":"tool","tool_command":"sleep 30","edges":[{"to":"done"}]},{"id":"done","type":"exit"}]}`

func TestMCPStdioListsCompactToolsAndServesCurrentGraphSchema(t *testing.T) {
	session, ctx := connectToTractorMCP(t)
	listed, err := session.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{
		"get_pipeline_schema",
		"get_run_status",
		"start_run",
		"steer_run",
		"stop_run",
		"validate_pipeline",
	}
	var gotNames []string
	for _, tool := range listed.Tools {
		gotNames = append(gotNames, tool.Name)
		if !tool.DeferLoading {
			t.Errorf("tool %q is not marked for deferred loading", tool.Name)
		}
		if tool.Name != "start_run" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 2_000 {
			t.Fatalf("start_run input schema is unexpectedly large: %d bytes", len(raw))
		}
		if bytes.Contains(raw, []byte("parallel.fan_in")) {
			t.Fatal("start_run input schema embeds the graph language")
		}
	}
	slices.Sort(gotNames)
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("tool names = %v, want %v", gotNames, wantNames)
	}

	result, err := callTool(ctx, session, "get_pipeline_schema", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("get_pipeline_schema returned an error: %#v", result.Content)
	}
	output := decodeStructured[schemaOutput](t, result.StructuredContent)
	if output.Schema != string(graph.Graph{}.Schema()) {
		t.Fatal("MCP schema differs from graph.Graph schema")
	}
}

func TestMCPStdioStartsAndObservesRealPipelineRun(t *testing.T) {
	session, ctx := connectToTractorMCP(t)
	workdir := t.TempDir()
	pipelinePath := filepath.Join(workdir, "pipeline.json")
	if err := os.WriteFile(pipelinePath, []byte(linearPipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	logsRoot := filepath.Join(workdir, "logs")

	validation, err := callTool(ctx, session, "validate_pipeline",
		map[string]any{
			"pipeline_path": pipelinePath,
			"workdir":       workdir,
		})
	if err != nil {
		t.Fatal(err)
	}
	if validation.IsError {
		t.Fatalf("validate_pipeline returned an error: %#v", validation.Content)
	}
	if output := decodeStructured[validationOutput](t, validation.StructuredContent); !output.Valid {
		t.Fatal("validate_pipeline did not report valid")
	}

	started, err := callTool(ctx, session, "start_run",
		map[string]any{
			"pipeline_path": pipelinePath,
			"workdir":       workdir,
			"logs_root":     logsRoot,
		})
	if err != nil {
		t.Fatal(err)
	}
	if started.IsError {
		t.Fatalf("start_run returned an error: %#v", started.Content)
	}
	start := decodeStructured[startRunOutput](t, started.StructuredContent)
	if start.RunID == "" || start.PID <= 0 || start.Status != "RUNNING" {
		t.Fatalf("start_run output = %#v", start)
	}

	deadline := time.Now().Add(10 * time.Second)
	var status runStatusOutput
	for {
		result, err := callTool(ctx, session, "get_run_status", map[string]any{"run_id": start.RunID})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("get_run_status returned an error: %#v", result.Content)
		}
		status = decodeStructured[runStatusOutput](t, result.StructuredContent)
		if status.Status != "RUNNING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish: %#v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Status != "COMPLETED" || status.ExitCode == nil || *status.ExitCode != 0 {
		t.Fatalf("terminal status = %#v", status)
	}
	checkpoint, err := engine.LoadCheckpoint(logsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.CurrentNode != "done" || checkpoint.NextNode != "" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
}

func TestMCPStdioSteersAndStopsRunningPipeline(t *testing.T) {
	session, ctx := connectToTractorMCP(t)
	start := startMCPRun(t, ctx, session, slowPipeline)
	waitForFile(t, filepath.Join(start.LogsRoot, "manifest.json"))

	steered, err := callTool(ctx, session, "steer_run", map[string]any{
		"run_id": start.RunID,
		"text":   "continue carefully",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steered.IsError {
		t.Fatalf("steer_run returned an error: %#v", steered.Content)
	}
	steer := decodeStructured[steerRunOutput](t, steered.StructuredContent)
	if steer.Accepted || steer.HTTPStatus != 409 {
		t.Fatalf("steer_run output = %#v", steer)
	}

	stopped, err := callTool(ctx, session, "stop_run", map[string]any{"run_id": start.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.IsError {
		t.Fatalf("stop_run returned an error: %#v", stopped.Content)
	}
	if output := decodeStructured[stopRunOutput](t, stopped.StructuredContent); output.Status != "STOPPING" {
		t.Fatalf("stop_run output = %#v", output)
	}
	status := waitForRunStatus(t, ctx, session, start.RunID)
	if status.Status != "STOPPED" {
		t.Fatalf("terminal status = %#v", status)
	}

	steered, err = callTool(ctx, session, "steer_run", map[string]any{
		"run_id": start.RunID,
		"text":   "too late",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steered.IsError {
		t.Fatalf("steer_run on stopped run returned an error: %#v", steered.Content)
	}
	steer = decodeStructured[steerRunOutput](t, steered.StructuredContent)
	if steer.Accepted || steer.HTTPStatus != 409 || !strings.Contains(steer.Message, "STOPPED") {
		t.Fatalf("steer_run on stopped run = %#v", steer)
	}
}

func TestMCPStdioShutdownStopsRunningPipeline(t *testing.T) {
	session, ctx := connectToTractorMCP(t)
	start := startMCPRun(t, ctx, session, slowPipeline)
	waitForFile(t, filepath.Join(start.LogsRoot, "manifest.json"))
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for processExists(start.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("Tractor child process %d survived MCP shutdown", start.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startMCPRun(t *testing.T, ctx context.Context, session *client.Client, pipeline string) startRunOutput {
	t.Helper()
	workdir := t.TempDir()
	pipelinePath := filepath.Join(workdir, "pipeline.json")
	if err := os.WriteFile(pipelinePath, []byte(pipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	started, err := callTool(ctx, session, "start_run", map[string]any{
		"pipeline_path": pipelinePath,
		"workdir":       workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.IsError {
		t.Fatalf("start_run returned an error: %#v", started.Content)
	}
	return decodeStructured[startRunOutput](t, started.StructuredContent)
}

func waitForRunStatus(t *testing.T, ctx context.Context, session *client.Client, runID string) runStatusOutput {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		result, err := callTool(ctx, session, "get_run_status", map[string]any{"run_id": runID})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("get_run_status returned an error: %#v", result.Content)
		}
		status := decodeStructured[runStatusOutput](t, result.StructuredContent)
		if status.Status != "RUNNING" && status.Status != "STOPPING" {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish: %#v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file did not appear: %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func connectToTractorMCP(t *testing.T) (*client.Client, context.Context) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "tractor")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Tractor: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	session, err := client.NewStdioMCPClient(binary, nil, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "tractor-test", Version: "1.0.0"}
	if _, err := session.Initialize(ctx, request); err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

func callTool(ctx context.Context, session *client.Client, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	return session.CallTool(ctx, request)
}

func decodeStructured[T any](t *testing.T, value any) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
