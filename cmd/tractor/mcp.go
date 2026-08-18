package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/tylergannon/tractor/engine"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/lint"
)

const tractorMCPVersion = "0.1.0"

const (
	gracefulRunStopTimeout = 500 * time.Millisecond
	forcedRunStopTimeout   = time.Second
)

type tractorMCPServer struct {
	mu   sync.Mutex
	runs map[string]*managedRun
}

type managedRun struct {
	mu sync.Mutex

	id         string
	pipeline   string
	workdir    string
	logsRoot   string
	stdoutPath string
	stderrPath string
	command    *exec.Cmd
	status     string
	startedAt  time.Time
	finishedAt time.Time
	exitCode   *int
	failure    string
	done       chan struct{}
	stopAt     time.Time
}

type emptyInput struct{}

type schemaOutput struct {
	Schema string `json:"schema" jsonschema:"Current Tractor pipeline JSON Schema."`
}

type pipelineInput struct {
	PipelinePath string `json:"pipeline_path" jsonschema:"Pipeline JSON, YAML, or YML file. Relative paths resolve from workdir."`
	Workdir      string `json:"workdir,omitempty" jsonschema:"Pipeline workspace. Defaults to the MCP server working directory."`
}

type validationOutput struct {
	Valid    bool     `json:"valid"`
	Warnings []string `json:"warnings"`
}

type startRunInput struct {
	PipelinePath string `json:"pipeline_path" jsonschema:"Pipeline JSON, YAML, or YML file. Relative paths resolve from workdir."`
	Workdir      string `json:"workdir,omitempty" jsonschema:"Pipeline workspace. Defaults to the MCP server working directory."`
	LogsRoot     string `json:"logs_root,omitempty" jsonschema:"Run log directory. Relative paths resolve from workdir. Defaults to .tractor/runs/<run-id>."`
	Resume       bool   `json:"resume,omitempty" jsonschema:"Resume from the checkpoint in logs_root."`
}

type startRunOutput struct {
	RunID      string   `json:"run_id"`
	PID        int      `json:"pid"`
	Status     string   `json:"status"`
	Pipeline   string   `json:"pipeline_path"`
	Workdir    string   `json:"workdir"`
	LogsRoot   string   `json:"logs_root"`
	Warnings   []string `json:"warnings"`
	StdoutPath string   `json:"stdout_path"`
	StderrPath string   `json:"stderr_path"`
}

type runIDInput struct {
	RunID string `json:"run_id" jsonschema:"Run identifier returned by start_run."`
}

type runStatusOutput struct {
	RunID        string `json:"run_id"`
	PID          int    `json:"pid"`
	Status       string `json:"status"`
	Pipeline     string `json:"pipeline_path"`
	Workdir      string `json:"workdir"`
	LogsRoot     string `json:"logs_root"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Failure      string `json:"failure,omitempty"`
	CurrentNode  string `json:"current_node,omitempty"`
	NextNode     string `json:"next_node,omitempty"`
	LastStage    string `json:"last_stage,omitempty"`
	LastResponse string `json:"last_response,omitempty"`
	StderrTail   string `json:"stderr_tail,omitempty"`
}

type steerRunInput struct {
	RunID string `json:"run_id" jsonschema:"Run identifier returned by start_run."`
	Text  string `json:"text" jsonschema:"Instruction to deliver to the currently active steerable agent turn."`
}

type steerRunOutput struct {
	Accepted   bool   `json:"accepted"`
	HTTPStatus int    `json:"http_status"`
	Message    string `json:"message"`
}

type stopRunOutput struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

func newMCPCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve Tractor tools over MCP stdio",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, stopSignals := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stopSignals()
			return runTractorMCP(ctx)
		},
	}
}

func runTractorMCP(ctx context.Context) error {
	mcpServer, state := newTractorMCPServer()
	listenErr := server.NewStdioServer(mcpServer).Listen(ctx, os.Stdin, os.Stdout)
	return errors.Join(listenErr, state.shutdownRuns())
}

func newTractorMCPServer() (*server.MCPServer, *tractorMCPServer) {
	state := &tractorMCPServer{runs: make(map[string]*managedRun)}
	mcpServer := server.NewMCPServer(
		"tractor",
		tractorMCPVersion,
		server.WithInstructions("Pipeline definitions are files. Read the current schema only when authoring or changing a pipeline, validate before starting, and use the returned run_id for later operations. Runs belong to this stdio session and are stopped when it closes."),
		server.WithToolCapabilities(false),
		server.WithInputSchemaValidation(),
		server.WithOutputSchemaValidation(),
	)

	mcpServer.AddTool(mcp.NewTool("get_pipeline_schema",
		mcp.WithDescription("Return the current pipeline JSON Schema generated by Tractor's graph type."),
		mcp.WithInputSchema[emptyInput](), mcp.WithOutputSchema[schemaOutput](),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Get pipeline schema"),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
	), mcp.NewStructuredToolHandler(func(context.Context, mcp.CallToolRequest, emptyInput) (schemaOutput, error) {
		return schemaOutput{Schema: string(graph.Graph{}.Schema())}, nil
	}))

	mcpServer.AddTool(mcp.NewTool("validate_pipeline",
		mcp.WithDescription("Parse and validate a pipeline file with Tractor's current graph parser and runtime lint rules."),
		mcp.WithInputSchema[pipelineInput](), mcp.WithOutputSchema[validationOutput](),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Validate pipeline"),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
	), mcp.NewStructuredToolHandler(state.validatePipeline))

	mcpServer.AddTool(mcp.NewTool("start_run",
		mcp.WithDescription("Start or resume a Tractor pipeline asynchronously and return a run ID immediately."),
		mcp.WithInputSchema[startRunInput](), mcp.WithOutputSchema[startRunOutput](),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Start Tractor run"),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
	), mcp.NewStructuredToolHandler(state.startRun))

	mcpServer.AddTool(mcp.NewTool("get_run_status",
		mcp.WithDescription("Read process and checkpoint status for a run started by this MCP server."),
		mcp.WithInputSchema[runIDInput](), mcp.WithOutputSchema[runStatusOutput](),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Get Tractor run status"),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
	), mcp.NewStructuredToolHandler(state.getRunStatus))

	mcpServer.AddTool(mcp.NewTool("steer_run",
		mcp.WithDescription("Send one text instruction to the active steerable agent turn in a Tractor run."),
		mcp.WithInputSchema[steerRunInput](), mcp.WithOutputSchema[steerRunOutput](),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Steer Tractor run"),
		mcp.WithDestructiveHintAnnotation(false), mcp.WithOpenWorldHintAnnotation(false),
	), mcp.NewStructuredToolHandler(state.steerRun))

	mcpServer.AddTool(mcp.NewTool("stop_run",
		mcp.WithDescription("Request interruption of a running Tractor process started by this MCP server."),
		mcp.WithInputSchema[runIDInput](), mcp.WithOutputSchema[stopRunOutput](),
		mcp.WithDeferLoading(true), mcp.WithTitleAnnotation("Stop Tractor run"),
		mcp.WithDestructiveHintAnnotation(true), mcp.WithOpenWorldHintAnnotation(false),
	), mcp.NewStructuredToolHandler(state.stopRun))

	return mcpServer, state
}

func (s *tractorMCPServer) validatePipeline(_ context.Context, _ mcp.CallToolRequest, input pipelineInput) (validationOutput, error) {
	_, warnings, err := loadAndValidatePipeline(input.PipelinePath, input.Workdir)
	if err != nil {
		return validationOutput{}, err
	}
	return validationOutput{Valid: true, Warnings: warnings}, nil
}

func (s *tractorMCPServer) startRun(_ context.Context, _ mcp.CallToolRequest, input startRunInput) (startRunOutput, error) {
	pipelinePath, warnings, err := loadAndValidatePipeline(input.PipelinePath, input.Workdir)
	if err != nil {
		return startRunOutput{}, err
	}
	workdir, err := resolveWorkdir(input.Workdir)
	if err != nil {
		return startRunOutput{}, err
	}
	runID, err := newMCPRunID()
	if err != nil {
		return startRunOutput{}, err
	}
	logsRoot, err := resolveLogsRoot(input.LogsRoot, workdir, runID)
	if err != nil {
		return startRunOutput{}, err
	}
	if input.Resume {
		if _, err := engine.LoadCheckpoint(logsRoot); err != nil {
			return startRunOutput{}, err
		}
	} else if err := requireFreshLogsRoot(logsRoot); err != nil {
		return startRunOutput{}, err
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return startRunOutput{}, fmt.Errorf("create logs root: %w", err)
	}

	stdoutPath := filepath.Join(logsRoot, "mcp-stdout.log")
	stderrPath := filepath.Join(logsRoot, "mcp-stderr.log")
	logFlags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if input.Resume {
		logFlags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	stdout, err := os.OpenFile(stdoutPath, logFlags, 0o644)
	if err != nil {
		return startRunOutput{}, fmt.Errorf("open run stdout: %w", err)
	}
	stderr, err := os.OpenFile(stderrPath, logFlags, 0o644)
	if err != nil {
		_ = stdout.Close()
		return startRunOutput{}, fmt.Errorf("open run stderr: %w", err)
	}

	executable, err := os.Executable()
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return startRunOutput{}, fmt.Errorf("resolve Tractor executable: %w", err)
	}
	args := []string{"run", pipelinePath, "--workdir", workdir, "--logs", logsRoot}
	if input.Resume {
		args = append(args, "--resume")
	}
	command := exec.Command(executable, args...)
	command.Dir = workdir
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return startRunOutput{}, fmt.Errorf("start Tractor run: %w", err)
	}

	run := &managedRun{
		id: runID, pipeline: pipelinePath, workdir: workdir, logsRoot: logsRoot,
		stdoutPath: stdoutPath, stderrPath: stderrPath, command: command,
		status: "RUNNING", startedAt: time.Now().UTC(), done: make(chan struct{}),
	}
	s.mu.Lock()
	s.runs[runID] = run
	s.mu.Unlock()
	go waitForRun(run, stdout, stderr)

	return startRunOutput{
		RunID: runID, PID: command.Process.Pid, Status: "RUNNING",
		Pipeline: pipelinePath, Workdir: workdir, LogsRoot: logsRoot,
		Warnings: warnings, StdoutPath: stdoutPath, StderrPath: stderrPath,
	}, nil
}

func waitForRun(run *managedRun, stdout, stderr *os.File) {
	err := run.command.Wait()
	_ = stdout.Close()
	_ = stderr.Close()

	run.mu.Lock()
	defer run.mu.Unlock()
	exitCode := run.command.ProcessState.ExitCode()
	run.exitCode = &exitCode
	run.finishedAt = time.Now().UTC()
	if run.status == "STOPPING" {
		run.status = "STOPPED"
	} else if err != nil {
		run.status = "FAILED"
	} else {
		run.status = "COMPLETED"
	}
	if err != nil {
		run.failure = err.Error()
	}
	close(run.done)
}

func (s *tractorMCPServer) getRunStatus(_ context.Context, _ mcp.CallToolRequest, input runIDInput) (runStatusOutput, error) {
	run, err := s.lookupRun(input.RunID)
	if err != nil {
		return runStatusOutput{}, err
	}
	return snapshotRun(run), nil
}

func snapshotRun(run *managedRun) runStatusOutput {
	run.mu.Lock()
	result := runStatusOutput{
		RunID: run.id, PID: run.command.Process.Pid, Status: run.status,
		Pipeline: run.pipeline, Workdir: run.workdir, LogsRoot: run.logsRoot,
		StartedAt: run.startedAt.Format(time.RFC3339Nano), ExitCode: run.exitCode,
		Failure: run.failure,
	}
	if !run.finishedAt.IsZero() {
		result.FinishedAt = run.finishedAt.Format(time.RFC3339Nano)
	}
	stderrPath := run.stderrPath
	logsRoot := run.logsRoot
	run.mu.Unlock()

	if _, err := os.Stat(filepath.Join(logsRoot, "checkpoint.json")); err == nil {
		if checkpoint, err := engine.LoadCheckpoint(logsRoot); err == nil {
			result.CurrentNode = checkpoint.CurrentNode
			result.NextNode = checkpoint.NextNode
			result.LastStage = checkpoint.LastStage
			result.LastResponse = checkpoint.LastResponse
		}
	}
	result.StderrTail = readTail(stderrPath, 4096)
	return result
}

func (s *tractorMCPServer) steerRun(ctx context.Context, _ mcp.CallToolRequest, input steerRunInput) (steerRunOutput, error) {
	run, err := s.lookupRun(input.RunID)
	if err != nil {
		return steerRunOutput{}, err
	}
	if strings.TrimSpace(input.Text) == "" {
		return steerRunOutput{}, errors.New("steering text must not be empty")
	}
	run.mu.Lock()
	if run.status != "RUNNING" {
		status := run.status
		run.mu.Unlock()
		return steerRunOutput{
			Accepted: false, HTTPStatus: http.StatusConflict,
			Message: "run is " + status + "; there is no active steerable turn",
		}, nil
	}
	logsRoot := run.logsRoot
	run.mu.Unlock()

	controlSocket, err := engine.LoadControlSocket(logsRoot)
	if err != nil {
		return steerRunOutput{}, err
	}
	body, err := json.Marshal([]harness.ContentPart{{Type: harness.ContentPartText, Text: input.Text}})
	if err != nil {
		return steerRunOutput{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", controlSocket)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://tractor/steer", bytes.NewReader(body))
	if err != nil {
		return steerRunOutput{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return steerRunOutput{}, fmt.Errorf("send steering instruction: %w", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return steerRunOutput{Accepted: true, HTTPStatus: response.StatusCode, Message: "steering accepted"}, nil
	case http.StatusConflict:
		return steerRunOutput{Accepted: false, HTTPStatus: response.StatusCode, Message: "run has no active steerable turn"}, nil
	default:
		return steerRunOutput{}, fmt.Errorf("steering endpoint returned HTTP %d", response.StatusCode)
	}
}

func (s *tractorMCPServer) stopRun(_ context.Context, _ mcp.CallToolRequest, input runIDInput) (stopRunOutput, error) {
	run, err := s.lookupRun(input.RunID)
	if err != nil {
		return stopRunOutput{}, err
	}
	status, err := requestRunStop(run)
	if err != nil {
		return stopRunOutput{}, err
	}
	return stopRunOutput{RunID: run.id, Status: status}, nil
}

func requestRunStop(run *managedRun) (string, error) {
	run.mu.Lock()
	switch run.status {
	case "STOPPING":
		stopAt := run.stopAt
		run.mu.Unlock()
		remaining := time.Until(stopAt.Add(gracefulRunStopTimeout))
		if remaining > 0 && waitForRunDone(run, remaining) {
			return currentRunStatus(run), nil
		}
		if waitForRunDone(run, 0) {
			return currentRunStatus(run), nil
		}
		if err := forceRunStop(run); err != nil {
			return "", err
		}
		if !waitForRunDone(run, forcedRunStopTimeout) {
			return "", errors.New("timed out waiting for Tractor run to stop")
		}
		return currentRunStatus(run), nil
	case "RUNNING":
	case "COMPLETED", "FAILED", "STOPPED":
		status := run.status
		run.mu.Unlock()
		return status, nil
	default:
		status := run.status
		run.mu.Unlock()
		return "", fmt.Errorf("run has unknown status %q", status)
	}
	if err := run.command.Process.Signal(os.Interrupt); err != nil {
		done := run.done
		run.mu.Unlock()
		if !errors.Is(err, os.ErrProcessDone) {
			return "", fmt.Errorf("interrupt Tractor run: %w", err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		run.mu.Lock()
		status := run.status
		run.mu.Unlock()
		return status, nil
	}
	run.status = "STOPPING"
	run.stopAt = time.Now()
	run.mu.Unlock()
	return "STOPPING", nil
}

func currentRunStatus(run *managedRun) string {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.status
}

func waitForRunDone(run *managedRun, timeout time.Duration) bool {
	if timeout <= 0 {
		select {
		case <-run.done:
			return true
		default:
			return false
		}
	}
	select {
	case <-run.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func forceRunStop(run *managedRun) error {
	pid := run.command.Process.Pid
	if err := run.command.Process.Signal(syscall.SIGSTOP); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("freeze Tractor run %s before forced stop: %w", run.id, err)
	}
	if err := killProcessGroup(pid); err != nil {
		return fmt.Errorf("kill Tractor run %s process group: %w", run.id, err)
	}
	return nil
}

func killProcessGroup(processGroup int) error {
	err := syscall.Kill(-processGroup, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (s *tractorMCPServer) shutdownRuns() error {
	s.mu.Lock()
	runs := make([]*managedRun, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, run)
	}
	s.mu.Unlock()

	var shutdownErrs []error
	for _, run := range runs {
		run.mu.Lock()
		if run.status != "RUNNING" {
			run.mu.Unlock()
			continue
		}
		err := run.command.Process.Signal(os.Interrupt)
		if err == nil {
			run.status = "STOPPING"
			run.stopAt = time.Now()
		}
		run.mu.Unlock()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			shutdownErrs = append(shutdownErrs, err)
		}
	}
	if waitForAllRuns(runs, gracefulRunStopTimeout) {
		return errors.Join(shutdownErrs...)
	}
	for _, run := range runs {
		if waitForRunDone(run, 0) {
			continue
		}
		if err := forceRunStop(run); err != nil {
			shutdownErrs = append(shutdownErrs, err)
		}
	}
	if !waitForAllRuns(runs, forcedRunStopTimeout) {
		shutdownErrs = append(shutdownErrs, errors.New("timed out waiting for Tractor runs to stop"))
	}
	return errors.Join(shutdownErrs...)
}

func waitForAllRuns(runs []*managedRun, timeout time.Duration) bool {
	allDone := make(chan struct{})
	go func() {
		for _, run := range runs {
			<-run.done
		}
		close(allDone)
	}()
	select {
	case <-allDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *tractorMCPServer) lookupRun(runID string) (*managedRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		return nil, fmt.Errorf("unknown run ID %q", runID)
	}
	return run, nil
}

func loadAndValidatePipeline(path, workdir string) (string, []string, error) {
	resolvedWorkdir, err := resolveWorkdir(workdir)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil, errors.New("pipeline_path must not be empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(resolvedWorkdir, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve pipeline path: %w", err)
	}
	pipeline, _, err := loadPipeline([]string{path}, "", false, "", false)
	if err != nil {
		return "", nil, err
	}
	diagnostics, err := cliValidator().ValidateOrError(*pipeline)
	if err != nil {
		return "", nil, err
	}
	warnings := make([]string, 0)
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != lint.SeverityError {
			warnings = append(warnings, fmt.Sprintf("%s %s: %s", diagnostic.Severity, diagnostic.Rule, diagnostic.Message))
		}
	}
	return path, warnings, nil
}

func resolveWorkdir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	return absoluteDirectory(path)
}

func resolveLogsRoot(path, workdir, runID string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(workdir, ".tractor", "runs", runID)
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve logs root: %w", err)
	}
	return path, nil
}

func requireFreshLogsRoot(path string) error {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect logs root: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("logs root %q is not empty; set resume to continue it", path)
	}
	return nil
}

func newMCPRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func readTail(path string, limit int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	offset := max(info.Size()-int64(limit), 0)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return ""
	}
	return string(raw)
}
