package lint

import (
	"fmt"

	"github.com/tylergannon/tractor/graph"
)

// ParallelForFanIn returns the sole converged parallel node that owns fanInID.
func ParallelForFanIn(g graph.Graph, fanInID string) (*graph.ParallelNode, error) {
	analysis := newAnalysis(g, options{})
	var owner *graph.ParallelNode
	count := 0
	for _, block := range analysis.parallelBlocks() {
		if block.converged && block.candidate == fanInID {
			owner = block.node
			count++
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("fan-in %q must have exactly one owning parallel node; found %d", fanInID, count)
	}
	return owner, nil
}
