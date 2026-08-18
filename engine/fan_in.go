package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/lint"
)

// BranchResult is the durable evidence produced for one parallel branch.
type BranchResult struct {
	BranchID  string           `json:"branch_id"`
	Outcome   *harness.Outcome `json:"outcome,omitempty"`
	Error     *harness.Error   `json:"error,omitempty"`
	Notes     string           `json:"notes"`
	Path      []string         `json:"path"`
	Workdir   string           `json:"workdir"`
	StageDirs []string         `json:"stage_dirs"`
	Segments  []string         `json:"segments"`
}

// FanInHandler evaluates branch evidence through the codergen path.
type FanInHandler struct {
	codergen *CodergenHandler
}

// NewFanInHandler constructs a fan-in handler with the codergen configuration.
func NewFanInHandler(config CodergenConfig) *FanInHandler {
	return &FanInHandler{codergen: NewCodergenHandler(config)}
}

// Execute loads the owning parallel's evidence and runs the fan-in turn.
func (h *FanInHandler) Execute(node graph.Node, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph) (harness.Outcome, *harness.Error) {
	join, ok := node.(*graph.FanInNode)
	if !ok {
		return harness.Outcome{}, terminalError(fmt.Sprintf("fan-in handler cannot execute node type %s", node.NodeType()))
	}
	owner, err := lint.ParallelForFanIn(*pipeline, join.ID)
	if err != nil {
		return harness.Outcome{}, terminalError(err.Error())
	}
	evidencePath := filepath.Join(filepath.Dir(scope.StageDir), "latest", owner.ID, "branches.json")
	results, err := readBranchResults(evidencePath)
	if err != nil {
		return harness.Outcome{}, terminalError(fmt.Sprintf("read branch evidence for %s: %v", owner.ID, err))
	}
	branchRoots := make(map[string]struct{}, len(owner.Branches))
	for _, branch := range owner.Branches {
		branchRoots[branch] = struct{}{}
	}
	for _, result := range results {
		if _, ok := branchRoots[result.BranchID]; !ok {
			return harness.Outcome{}, terminalError(fmt.Sprintf("branch evidence for %s names unknown branch %q", owner.ID, result.BranchID))
		}
	}

	prompt := ""
	if join.Prompt.Present {
		prompt = join.Prompt.Value
	}
	if prompt == "" {
		prompt = "Evaluate the results of the parallel branches."
	}
	prompt = expandPrompt(prompt, scope.Goal)
	prompt += "\n\n" + renderBranchResults(results)
	return h.codergen.executeTurn(join, &join.LLMNodeFields, offered, scope, pipeline, prompt)
}

func readBranchResults(path string) ([]BranchResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var results []BranchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("branches.json must contain at least one branch result")
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if _, duplicate := seen[result.BranchID]; duplicate {
			return nil, fmt.Errorf("duplicate branch_id %q", result.BranchID)
		}
		seen[result.BranchID] = struct{}{}
	}
	return results, nil
}

func renderBranchResults(results []BranchResult) string {
	var rendered strings.Builder
	for index, result := range results {
		if index > 0 {
			rendered.WriteString("\n\n")
		}
		fmt.Fprintf(&rendered, "Branch ID: %s\nNotes: %s\nWorktree: %s", result.BranchID, result.Notes, result.Workdir)
	}
	return rendered.String()
}
