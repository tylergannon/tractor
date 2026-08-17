package engine

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/lint"
)

type parallelHandler struct {
	runner *Runner
	state  *engineState
	store  *runStore
}

func (h *parallelHandler) Execute(node graph.Node, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph) (harness.Outcome, *harness.Error) {
	parallel, ok := node.(*graph.ParallelNode)
	if !ok {
		return harness.Outcome{}, terminalError(fmt.Sprintf("parallel handler cannot execute node type %s", node.NodeType()))
	}
	limit := 4
	if parallel.MaxParallel.Present {
		limit = parallel.MaxParallel.Value
	}
	if limit <= 0 {
		return harness.Outcome{}, terminalError("max_parallel must be positive")
	}

	join, err := parallelFanIn(*pipeline, parallel.ID)
	if err != nil {
		return harness.Outcome{}, terminalError(err.Error())
	}
	snapshot := h.state.snapshotCounters()
	succeeded := false
	defer func() {
		if !succeeded {
			h.state.restoreCounters(snapshot)
		}
	}()
	if scope.Stop.IsSet() {
		return harness.Outcome{}, interruptedError("stopped by operator")
	}
	parallelStarted := time.Now()
	if err := h.store.appendTimeline(timelineEvent{
		"type":         "ParallelStarted",
		"branch_count": len(offered),
	}); err != nil {
		return harness.Outcome{}, terminalError(err.Error())
	}

	indexOffset, err := segmentIndexOffset(h.store.root)
	if err != nil {
		return harness.Outcome{}, terminalError(err.Error())
	}
	frozen, err := freezeGitWorkspaceWithStop(scope.Workdir, scope.Stop)
	if err != nil {
		return harness.Outcome{}, parallelExecutionError(err)
	}
	branchIDs := make([]string, len(offered))
	for index, edge := range offered {
		branchIDs[index] = edge.To
	}
	worktrees, err := createBranchWorktreesWithStop(frozen, h.store.root, branchIDs, scope.Stop)
	if err != nil {
		return harness.Outcome{}, parallelExecutionError(err)
	}

	results, eventErr := h.runBranches(worktrees, join.ID, limit)
	segmentErr := attributeBranchSegments(h.store.root, indexOffset, results)
	if err := writeJSON(filepath.Join(scope.StageDir, "branches.json"), results); err != nil {
		return harness.Outcome{}, terminalError(fmt.Sprintf("write branch evidence: %v", err))
	}
	if eventErr != nil {
		return harness.Outcome{}, terminalError(eventErr.Error())
	}
	if segmentErr != nil {
		return harness.Outcome{}, terminalError(segmentErr.Error())
	}
	if err := h.store.appendTimeline(timelineEvent{
		"type":         "ParallelCompleted",
		"duration":     time.Since(parallelStarted).String(),
		"branch_count": len(results),
	}); err != nil {
		return harness.Outcome{}, terminalError(err.Error())
	}
	for _, result := range results {
		if result.Error != nil {
			return harness.Outcome{}, result.Error
		}
	}

	succeeded = true
	return harness.Outcome{Next: join.ID, Notes: fmt.Sprintf("%d branches converged", len(results))}, nil
}

func parallelExecutionError(err error) *harness.Error {
	if errors.Is(err, errGitStopped) {
		return interruptedError("stopped by operator")
	}
	return terminalError(err.Error())
}

func (h *parallelHandler) runBranches(worktrees []branchWorktree, joinID string, limit int) ([]BranchResult, error) {
	results := make([]BranchResult, len(worktrees))
	if limit > len(worktrees) {
		limit = len(worktrees)
	}
	jobs := make(chan int)
	var eventMu sync.Mutex
	var eventErr error
	var workers sync.WaitGroup
	workers.Add(limit)
	for range limit {
		go func() {
			defer workers.Done()
			for index := range jobs {
				worktree := worktrees[index]
				branchStarted := time.Now()
				if err := h.store.appendTimeline(timelineEvent{
					"type":   "ParallelBranchStarted",
					"branch": worktree.BranchID,
					"index":  index,
				}); err != nil {
					results[index] = BranchResult{
						BranchID:  worktree.BranchID,
						Error:     terminalError(err.Error()),
						Path:      []string{},
						Workdir:   worktree.Workdir,
						StageDirs: []string{},
						Segments:  []string{},
					}
					eventMu.Lock()
					eventErr = errors.Join(eventErr, err)
					eventMu.Unlock()
					continue
				}
				results[index] = h.runner.walkBranch(worktree.BranchID, joinID, worktree.Workdir, h.state, h.store)
				if err := h.store.appendTimeline(timelineEvent{
					"type":     "ParallelBranchCompleted",
					"branch":   worktree.BranchID,
					"index":    index,
					"duration": time.Since(branchStarted).String(),
				}); err != nil {
					eventMu.Lock()
					eventErr = errors.Join(eventErr, err)
					eventMu.Unlock()
				}
			}
		}()
	}
	for index := range worktrees {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return results, eventErr
}

func (r *Runner) walkBranch(currentID, joinID, workdir string, state *engineState, store *runStore) BranchResult {
	result := BranchResult{
		BranchID:  currentID,
		Path:      []string{},
		Workdir:   workdir,
		StageDirs: []string{},
		Segments:  []string{},
	}
	for currentID != joinID {
		node := r.nodes[currentID]
		if node == nil {
			result.Outcome = nil
			result.Error = terminalError(fmt.Sprintf("unknown node: %s", currentID))
			return result
		}
		if node.NodeType() == "exit" {
			result.Outcome = nil
			result.Error = terminalError(fmt.Sprintf("branch reached exit %s before fan-in %s", currentID, joinID))
			return result
		}
		if r.stop.IsSet() {
			result.Outcome = nil
			result.Error = interruptedError("stopped by operator")
			return result
		}

		result.Path = append(result.Path, currentID)
		execution, err := r.executeNode(node, state, store, workdir, result.BranchID)
		result.StageDirs = append(result.StageDirs, execution.stageDirs...)
		if execution.outcome.Notes != "" {
			result.Notes = execution.outcome.Notes
		}
		if err != nil {
			result.Outcome = nil
			result.Error = terminalError(err.Error())
			return result
		}
		if execution.runErr != nil {
			result.Outcome = nil
			result.Error = branchError(execution.runErr)
			return result
		}
		outcome := execution.outcome
		result.Outcome = &outcome
		currentID = execution.nextID
	}
	return result
}

func branchError(runErr *harness.Error) *harness.Error {
	if runErr.Category == harness.ErrorInterrupted {
		return &harness.Error{Category: harness.ErrorInterrupted, Message: runErr.Message}
	}
	return terminalError(runErr.Message)
}

func parallelFanIn(pipeline graph.Graph, parallelID string) (*graph.FanInNode, error) {
	var join *graph.FanInNode
	for _, node := range pipeline.Nodes {
		candidate, ok := node.(*graph.FanInNode)
		if !ok {
			continue
		}
		owner, err := lint.ParallelForFanIn(pipeline, candidate.ID)
		if err == nil && owner.ID == parallelID {
			if join != nil {
				return nil, fmt.Errorf("parallel node %q has multiple fan-in nodes", parallelID)
			}
			join = candidate
		}
	}
	if join == nil {
		return nil, fmt.Errorf("parallel node %q has no designated fan-in", parallelID)
	}
	return join, nil
}

func segmentIndexOffset(logsRoot string) (int64, error) {
	info, err := os.Stat(filepath.Join(logsRoot, "events", "index.jsonl"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect run-log index: %w", err)
	}
	return info.Size(), nil
}

func attributeBranchSegments(logsRoot string, offset int64, results []BranchResult) error {
	file, err := os.Open(filepath.Join(logsRoot, "events", "index.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open run-log index: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(offset, 0); err != nil {
		return fmt.Errorf("seek run-log index: %w", err)
	}

	branchByNode := make(map[string]int)
	for index, result := range results {
		for _, nodeID := range result.Path {
			branchByNode[nodeID] = index
		}
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry struct {
			NodeID string `json:"node_id"`
			Path   string `json:"path"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("decode run-log index: %w", err)
		}
		if index, ok := branchByNode[entry.NodeID]; ok {
			results[index].Segments = append(results[index].Segments, entry.Path)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read run-log index: %w", err)
	}
	return nil
}
