package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tylergannon/tractor/engine"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

const slowPipeline = `{"name":"slow","start":"wait","nodes":[{"id":"wait","type":"tool","tool_command":"sleep 30","on_success":"success"}]}`

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
	wantDestructive := map[string]bool{
		"get_pipeline_schema": false,
		"get_run_status":      false,
		"start_run":           true,
		"steer_run":           true,
		"stop_run":            true,
		"validate_pipeline":   false,
	}
	for _, tool := range listed.Tools {
		gotNames = append(gotNames, tool.Name)
		if !tool.DeferLoading {
			t.Errorf("tool %q is not marked for deferred loading", tool.Name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != wantDestructive[tool.Name] {
			t.Errorf("tool %q destructive hint = %v, want %t", tool.Name, tool.Annotations.DestructiveHint, wantDestructive[tool.Name])
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
	if checkpoint.CurrentNode != "done" || checkpoint.NextNode != graph.Success {
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
	toolPIDPath := filepath.Join(t.TempDir(), "tool.pid")
	pipeline := strings.Replace(slowPipeline, `"sleep 30"`, strconv.Quote("echo $$ > "+toolPIDPath+"; sleep 30"), 1)
	start := startMCPRun(t, ctx, session, pipeline)
	waitForFile(t, filepath.Join(start.LogsRoot, "manifest.json"))
	waitForFile(t, toolPIDPath)
	toolPIDBytes, err := os.ReadFile(toolPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	toolPID, err := strconv.Atoi(strings.TrimSpace(string(toolPIDBytes)))
	if err != nil {
		t.Fatal(err)
	}
	startedShutdown := time.Now()
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedShutdown); elapsed >= 2*time.Second {
		t.Fatalf("MCP shutdown took %s, exceeding the client grace period", elapsed)
	}

	deadline := time.Now().Add(10 * time.Second)
	for processExists(start.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("Tractor child process %d survived MCP shutdown", start.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if processExists(toolPID) {
		t.Fatalf("tool process %d survived MCP shutdown", toolPID)
	}
}

func TestSteerRunForwardsAcceptedInstruction(t *testing.T) {
	socketRoot, err := os.MkdirTemp("", "tractor-mcp-steer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketPath := filepath.Join(socketRoot, "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan []harness.ContentPart, 1)
	httpServer := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() { _ = request.Body.Close() }()
		if request.Method != http.MethodPost || request.URL.Path != "/steer" || request.Header.Get("Content-Type") != "application/json" {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		var parts []harness.ContentPart
		if err := json.NewDecoder(request.Body).Decode(&parts); err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- parts
		response.WriteHeader(http.StatusOK)
	})}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	logsRoot := t.TempDir()
	manifest, err := json.Marshal(map[string]any{"control_socket": socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsRoot, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	state := &tractorMCPServer{runs: map[string]*managedRun{
		"run": {id: "run", logsRoot: logsRoot, status: "RUNNING"},
	}}
	output, err := state.steerRun(context.Background(), mcp.CallToolRequest{}, steerRunInput{
		RunID: "run",
		Text:  "continue carefully",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Accepted || output.HTTPStatus != http.StatusOK {
		t.Fatalf("steer output = %#v", output)
	}
	parts := <-received
	if len(parts) != 1 || parts[0].Type != harness.ContentPartText || parts[0].Text != "continue carefully" {
		t.Fatalf("steering parts = %#v", parts)
	}
}

func TestRepeatedStopForceKillsRunProcessGroup(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", "trap '' INT TERM; sleep 30 & wait")
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) })
	run := &managedRun{
		id: "force-stop", command: command, status: "RUNNING",
		startedAt: time.Now(), done: make(chan struct{}),
	}
	go waitForRun(run, stdout, stderr)

	if status, err := requestRunStop(run); err != nil || status != "STOPPING" {
		t.Fatalf("first stop = %q, %v", status, err)
	}
	status, err := requestRunStop(run)
	if err != nil {
		t.Fatal(err)
	}
	if status != "STOPPED" {
		t.Fatalf("second stop status = %q", status)
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.Signal(0)); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group still exists: %v", err)
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
