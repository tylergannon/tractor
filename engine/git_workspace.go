package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errGitStopped = errors.New("git operation stopped")

type gitWorkspaceSnapshot struct {
	repoRoot   string
	commit     string
	workdirRel string
}

type branchWorktree struct {
	BranchID string
	Path     string
	Workdir  string
}

type worktreeInventoryEntry struct {
	Path     string    `json:"path"`
	BranchID string    `json:"branch_id"`
	TS       time.Time `json:"ts"`
}

func freezeGitWorkspace(workdir string) (gitWorkspaceSnapshot, error) {
	return freezeGitWorkspaceWithStop(workdir, nil)
}

func freezeGitWorkspaceWithStop(workdir string, stop *StopSignal) (gitWorkspaceSnapshot, error) {
	inside, err := gitOutputWithStop(workdir, nil, nil, stop, "rev-parse", "--is-inside-work-tree")
	if errors.Is(err, errGitStopped) {
		return gitWorkspaceSnapshot{}, err
	}
	if err != nil || inside != "true" {
		return gitWorkspaceSnapshot{}, fmt.Errorf("parallel execution requires a git worktree")
	}
	repoRoot, err := gitOutputWithStop(workdir, nil, nil, stop, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitWorkspaceSnapshot{}, err
	}
	resolvedWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	workdirRel, err := filepath.Rel(repoRoot, resolvedWorkdir)
	if err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("map workspace path: %w", err)
	}
	head, err := gitOutputWithStop(repoRoot, nil, nil, stop, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("resolve workspace HEAD: %w", err)
	}

	indexDir, err := os.MkdirTemp("", "tractor-index-")
	if err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("create temporary git index directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(indexDir) }()
	indexPath := filepath.Join(indexDir, "index")
	indexEnv := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := gitOutputWithStop(repoRoot, indexEnv, nil, stop, "read-tree", head); err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("seed workspace snapshot: %w", err)
	}
	if _, err := gitOutputWithStop(repoRoot, indexEnv, nil, stop, "add", "-A", "--", "."); err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("stage workspace snapshot: %w", err)
	}
	tree, err := gitOutputWithStop(repoRoot, indexEnv, nil, stop, "write-tree")
	if err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("write workspace snapshot tree: %w", err)
	}

	identityEnv := []string{
		"GIT_AUTHOR_NAME=tractor",
		"GIT_AUTHOR_EMAIL=tractor@localhost",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=tractor",
		"GIT_COMMITTER_EMAIL=tractor@localhost",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	commit, err := gitOutputWithStop(repoRoot, identityEnv, strings.NewReader("tractor parallel workspace snapshot\n"), stop, "commit-tree", tree, "-p", head)
	if err != nil {
		return gitWorkspaceSnapshot{}, fmt.Errorf("commit workspace snapshot: %w", err)
	}
	return gitWorkspaceSnapshot{repoRoot: repoRoot, commit: commit, workdirRel: workdirRel}, nil
}

func createBranchWorktrees(snapshot gitWorkspaceSnapshot, logsRoot string, branchIDs []string) ([]branchWorktree, error) {
	return createBranchWorktreesWithStop(snapshot, logsRoot, branchIDs, nil)
}

func createBranchWorktreesWithStop(snapshot gitWorkspaceSnapshot, logsRoot string, branchIDs []string, stop *StopSignal) ([]branchWorktree, error) {
	if len(branchIDs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create logs directory: %w", err)
	}
	fanoutRoot, err := os.MkdirTemp(logsRoot, "worktrees-")
	if err != nil {
		return nil, fmt.Errorf("create fan-out directory: %w", err)
	}
	created := make([]branchWorktree, 0, len(branchIDs))
	for index, branchID := range branchIDs {
		path := filepath.Join(fanoutRoot, fmt.Sprintf("branch-%03d", index+1))
		entry := worktreeInventoryEntry{Path: path, BranchID: branchID, TS: time.Now().UTC()}
		if err := appendWorktreeInventory(logsRoot, entry); err != nil {
			return created, err
		}
		if _, err := gitOutputWithStop(snapshot.repoRoot, nil, nil, stop, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", path, snapshot.commit); err != nil {
			return created, fmt.Errorf("create worktree for branch %q: %w", branchID, err)
		}
		mappedWorkdir := filepath.Join(path, snapshot.workdirRel)
		if err := os.MkdirAll(mappedWorkdir, 0o755); err != nil {
			return created, fmt.Errorf("create branch workdir for %q: %w", branchID, err)
		}
		created = append(created, branchWorktree{BranchID: branchID, Path: path, Workdir: mappedWorkdir})
	}
	return created, nil
}

func appendWorktreeInventory(logsRoot string, entry worktreeInventoryEntry) error {
	file, err := os.OpenFile(filepath.Join(logsRoot, "worktrees.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open worktree inventory: %w", err)
	}
	encodeErr := json.NewEncoder(file).Encode(entry)
	closeErr := file.Close()
	if encodeErr != nil {
		return fmt.Errorf("append worktree inventory: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close worktree inventory: %w", closeErr)
	}
	return nil
}

func cleanupBranchWorktrees(workdir, logsRoot string) error {
	entries, err := readWorktreeInventory(logsRoot)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	repoRoot, err := gitOutput(workdir, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve git worktree for cleanup: %w", err)
	}

	var cleanupErrors []error
	parents := make(map[string]struct{})
	for _, entry := range entries {
		parents[filepath.Dir(entry.Path)] = struct{}{}
		if _, err := gitOutput(repoRoot, nil, nil, "worktree", "remove", "--force", entry.Path); err != nil {
			if _, statErr := os.Stat(entry.Path); os.IsNotExist(statErr) {
				continue
			}
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove branch worktree %q: %w", entry.Path, err))
		}
	}
	for parent := range parents {
		if err := os.Remove(parent); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove fan-out directory %q: %w", parent, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func readWorktreeInventory(logsRoot string) ([]worktreeInventoryEntry, error) {
	file, err := os.Open(filepath.Join(logsRoot, "worktrees.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open worktree inventory: %w", err)
	}
	defer func() { _ = file.Close() }()

	var entries []worktreeInventoryEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry worktreeInventoryEntry
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("decode worktree inventory: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read worktree inventory: %w", err)
	}
	return entries, nil
}

func gitOutput(dir string, extraEnv []string, stdin io.Reader, args ...string) (string, error) {
	return gitOutputWithStop(dir, extraEnv, stdin, nil, args...)
}

func gitOutputWithStop(dir string, extraEnv []string, stdin io.Reader, stop *StopSignal, args ...string) (string, error) {
	if stop != nil && stop.IsSet() {
		return "", errGitStopped
	}
	commandArgs := append([]string{"-C", dir}, args...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stop != nil {
		go func() {
			select {
			case <-stop.done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(cleanGitEnvironment(os.Environ()), extraEnv...)
	command.Stdin = stdin
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	trimmed := strings.TrimSpace(stdout.String())
	if stop != nil && stop.IsSet() {
		return "", errGitStopped
	}
	if err != nil {
		diagnostic := strings.TrimSpace(stderr.String())
		if diagnostic == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), diagnostic, err)
	}
	return trimmed, nil
}

func cleanGitEnvironment(environment []string) []string {
	local := map[string]struct{}{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_COMMON_DIR":                   {},
		"GIT_CONFIG":                       {},
		"GIT_CONFIG_COUNT":                 {},
		"GIT_CONFIG_PARAMETERS":            {},
		"GIT_DIR":                          {},
		"GIT_GRAFT_FILE":                   {},
		"GIT_IMPLICIT_WORK_TREE":           {},
		"GIT_INDEX_FILE":                   {},
		"GIT_NO_REPLACE_OBJECTS":           {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_PREFIX":                       {},
		"GIT_REPLACE_REF_BASE":             {},
		"GIT_SHALLOW_FILE":                 {},
		"GIT_WORK_TREE":                    {},
	}
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if _, found := local[name]; !found {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
