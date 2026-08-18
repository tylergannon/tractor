package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	mcpRunStateVersion = 1
	mcpRunStateEnv     = "TRACTOR_MCP_STATE_DIR"
)

type mcpRunStore struct {
	dir string
}

type mcpRunRecord struct {
	Version    int        `json:"version"`
	ID         string     `json:"run_id"`
	PID        int        `json:"pid"`
	Status     string     `json:"status"`
	Pipeline   string     `json:"pipeline_path"`
	Workdir    string     `json:"workdir"`
	LogsRoot   string     `json:"logs_root"`
	StdoutPath string     `json:"stdout_path"`
	StderrPath string     `json:"stderr_path"`
	Resume     bool       `json:"resume"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Failure    string     `json:"failure,omitempty"`
	StopAt     *time.Time `json:"stop_at,omitempty"`
}

type mcpInstance struct {
	path string
}

type mcpInstanceRecord struct {
	PID            int       `json:"pid"`
	Version        string    `json:"version"`
	StartedAt      time.Time `json:"started_at"`
	DetachedRuns   bool      `json:"detached_runs"`
	ExecutablePath string    `json:"executable_path"`
}

func defaultMCPRunStore() (*mcpRunStore, error) {
	if configured := strings.TrimSpace(os.Getenv(mcpRunStateEnv)); configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return nil, fmt.Errorf("resolve MCP state directory: %w", err)
		}
		return &mcpRunStore{dir: absolute}, nil
	}
	if xdgState := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdgState != "" {
		return &mcpRunStore{dir: filepath.Join(xdgState, "tractor", "mcp-runs")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory for MCP state: %w", err)
	}
	return &mcpRunStore{dir: filepath.Join(home, ".local", "state", "tractor", "mcp-runs")}, nil
}

func (s *mcpRunStore) create(record mcpRunRecord) error {
	if err := validateMCPRunID(record.ID); err != nil {
		return err
	}
	return s.withLock(record.ID, func() error {
		if _, err := os.Stat(s.recordPath(record.ID)); err == nil {
			return fmt.Errorf("run ID %q already exists", record.ID)
		} else if !os.IsNotExist(err) {
			return err
		}
		return s.writeUnlocked(record)
	})
}

func (s *mcpRunStore) load(runID string) (mcpRunRecord, error) {
	if err := validateMCPRunID(runID); err != nil {
		return mcpRunRecord{}, err
	}
	var record mcpRunRecord
	err := s.withLock(runID, func() error {
		var err error
		record, err = s.readUnlocked(runID)
		return err
	})
	return record, err
}

func (s *mcpRunStore) update(runID string, change func(*mcpRunRecord) error) (mcpRunRecord, error) {
	if err := validateMCPRunID(runID); err != nil {
		return mcpRunRecord{}, err
	}
	var record mcpRunRecord
	err := s.withLock(runID, func() error {
		var err error
		record, err = s.readUnlocked(runID)
		if err != nil {
			return err
		}
		if err := change(&record); err != nil {
			return err
		}
		return s.writeUnlocked(record)
	})
	return record, err
}

func (s *mcpRunStore) withLock(runID string, action func() error) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create MCP state directory: %w", err)
	}
	lock, err := os.OpenFile(s.lockPath(runID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open MCP run lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock MCP run state: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return action()
}

func (s *mcpRunStore) readUnlocked(runID string) (mcpRunRecord, error) {
	raw, err := os.ReadFile(s.recordPath(runID))
	if os.IsNotExist(err) {
		return mcpRunRecord{}, fmt.Errorf("unknown run ID %q", runID)
	}
	if err != nil {
		return mcpRunRecord{}, fmt.Errorf("read MCP run state: %w", err)
	}
	var record mcpRunRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return mcpRunRecord{}, fmt.Errorf("decode MCP run state: %w", err)
	}
	if record.Version != mcpRunStateVersion || record.ID != runID {
		return mcpRunRecord{}, fmt.Errorf("invalid MCP run state for %q", runID)
	}
	return record, nil
}

func (s *mcpRunStore) writeUnlocked(record mcpRunRecord) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MCP run state: %w", err)
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(s.dir, ".run-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary MCP run state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary MCP run state: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary MCP run state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary MCP run state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary MCP run state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.recordPath(record.ID)); err != nil {
		return fmt.Errorf("replace MCP run state: %w", err)
	}
	return nil
}

func (s *mcpRunStore) recordPath(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

func (s *mcpRunStore) lockPath(runID string) string {
	return filepath.Join(s.dir, runID+".lock")
}

func registerMCPInstance(store *mcpRunStore) (*mcpInstance, error) {
	dir := filepath.Join(filepath.Dir(store.dir), "mcp-instances")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create MCP instance directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve MCP executable: %w", err)
	}
	record := mcpInstanceRecord{
		PID: os.Getpid(), Version: tractorMCPVersion, StartedAt: time.Now().UTC(),
		DetachedRuns: true, ExecutablePath: executable,
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	path := filepath.Join(dir, fmt.Sprintf("%d.json", record.PID))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, fmt.Errorf("write MCP instance record: %w", err)
	}
	return &mcpInstance{path: path}, nil
}

func (i *mcpInstance) remove() {
	_ = os.Remove(i.path)
}

func validateMCPRunID(runID string) error {
	if len(runID) != 32 {
		return fmt.Errorf("invalid run ID %q", runID)
	}
	for _, character := range runID {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return fmt.Errorf("invalid run ID %q", runID)
		}
	}
	return nil
}

func newMCPRunnerCommand() *cobra.Command {
	var stateDir string
	var runID string
	command := &cobra.Command{
		Use:    "mcp-runner",
		Short:  "Run one MCP-started pipeline independently",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runDetachedMCPRun(command, &mcpRunStore{dir: stateDir}, runID)
		},
	}
	command.Flags().StringVar(&stateDir, "state-dir", "", "MCP run state directory")
	command.Flags().StringVar(&runID, "run-id", "", "MCP run identifier")
	_ = command.MarkFlagRequired("state-dir")
	_ = command.MarkFlagRequired("run-id")
	return command
}

func runDetachedMCPRun(command *cobra.Command, store *mcpRunStore, runID string) error {
	shouldRun := true
	record, err := store.update(runID, func(record *mcpRunRecord) error {
		if record.PID != 0 && record.PID != os.Getpid() {
			return fmt.Errorf("run %s belongs to process %d", record.ID, record.PID)
		}
		record.PID = os.Getpid()
		switch record.Status {
		case "STARTING", "RUNNING":
			record.Status = "RUNNING"
		case "STOPPING":
			finishedAt := time.Now().UTC()
			exitCode := 130
			record.Status = "STOPPED"
			record.FinishedAt = &finishedAt
			record.ExitCode = &exitCode
			shouldRun = false
		case "COMPLETED", "FAILED", "STOPPED":
			shouldRun = false
		default:
			return fmt.Errorf("run %s has unknown status %q", record.ID, record.Status)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !shouldRun {
		return nil
	}
	pipeline, _, loadErr := loadPipeline([]string{record.Pipeline}, "", false, "", false)
	runErr := loadErr
	if runErr == nil {
		runErr = runPipeline(command, *pipeline, record.Workdir, record.LogsRoot, record.Resume)
	}

	_, persistErr := store.update(runID, func(record *mcpRunRecord) error {
		finishedAt := time.Now().UTC()
		record.FinishedAt = &finishedAt
		exitCode := 0
		switch {
		case record.Status == "STOPPING":
			record.Status = "STOPPED"
			exitCode = 130
		case runErr != nil:
			record.Status = "FAILED"
			exitCode = 1
		default:
			record.Status = "COMPLETED"
		}
		record.ExitCode = &exitCode
		if runErr != nil {
			record.Failure = runErr.Error()
		}
		return nil
	})
	return persistErr
}

func (s *mcpRunStore) refresh(record mcpRunRecord) (mcpRunRecord, error) {
	if !isActiveRunStatus(record.Status) || record.PID == 0 || isProcessAlive(record.PID) {
		return record, nil
	}
	return s.update(record.ID, func(current *mcpRunRecord) error {
		if !isActiveRunStatus(current.Status) || current.PID == 0 || isProcessAlive(current.PID) {
			return nil
		}
		finishedAt := time.Now().UTC()
		current.FinishedAt = &finishedAt
		exitCode := -1
		current.ExitCode = &exitCode
		if current.Status == "STOPPING" {
			current.Status = "STOPPED"
		} else {
			current.Status = "FAILED"
			current.Failure = "detached Tractor runner exited without recording a result"
		}
		return nil
	})
}

func isActiveRunStatus(status string) bool {
	return status == "STARTING" || status == "RUNNING" || status == "STOPPING"
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

func processOwnsRun(pid int, runID string) error {
	output, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "command=").Output()
	if err != nil {
		return fmt.Errorf("inspect Tractor runner process %d: %w", pid, err)
	}
	command := string(output)
	if !strings.Contains(command, "mcp-runner") ||
		(!strings.Contains(command, "--run-id "+runID) && !strings.Contains(command, "--run-id="+runID)) {
		return fmt.Errorf("process %d no longer belongs to Tractor run %s", pid, runID)
	}
	return nil
}
