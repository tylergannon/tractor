package lint_test

import (
	"strings"
	"testing"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/lint"
)

func TestParallelForFanInUsesConvergenceAnalysis(t *testing.T) {
	owner, err := lint.ParallelForFanIn(validParallel(), "join")
	if err != nil {
		t.Fatal(err)
	}
	if owner.ID != "parallel" {
		t.Fatalf("owner = %q", owner.ID)
	}
}

func TestParallelForFanInRejectsMissingOrAmbiguousOwner(t *testing.T) {
	if _, err := lint.ParallelForFanIn(validParallel(), "missing"); err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("missing owner error = %v", err)
	}

	ambiguous := graph.Graph{Start: "first", Nodes: []graph.Node{
		&graph.ParallelNode{NodeBase: graph.NodeBase{ID: "first"}, Branches: graph.LegacyParallelBranches("left")},
		&graph.ParallelNode{NodeBase: graph.NodeBase{ID: "second"}, Branches: graph.LegacyParallelBranches("right")},
		codergen("left", edge("join")),
		codergen("right", edge("join")),
		fanIn("join"),
	}}
	if _, err := lint.ParallelForFanIn(ambiguous, "join"); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("ambiguous owner error = %v", err)
	}
}
