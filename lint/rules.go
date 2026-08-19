package lint

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tylergannon/tractor/graph"
)

func (a *analysis) startTarget() []Diagnostic {
	node, exists := a.byID[a.graph.Start]
	if exists {
		if _, supervisor := node.(*graph.SupervisorNode); !supervisor {
			return nil
		}
	}
	return []Diagnostic{diagnostic("start_target", SeverityError,
		fmt.Sprintf("start target %q must name an existing walk node", a.graph.Start), a.graph.Start)}
}

func (a *analysis) terminalReachable() []Diagnostic {
	if _, exists := a.byID[a.graph.Start]; !exists {
		return nil
	}
	seen := map[string]struct{}{a.graph.Start: {}}
	queue := []string{a.graph.Start}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, record := range a.out[from] {
			to := record.edge.To
			if to == graph.Success {
				return nil
			}
			if graph.IsPseudoTarget(to) {
				continue
			}
			if _, exists := a.byID[to]; !exists {
				continue
			}
			if _, exists := seen[to]; exists {
				continue
			}
			seen[to] = struct{}{}
			queue = append(queue, to)
		}
	}
	return []Diagnostic{diagnostic("terminal_reachable", SeverityError,
		"no success pseudo-target is reachable from start", a.graph.Start)}
}

func (a *analysis) reachability() []Diagnostic {
	start, exists := a.byID[a.graph.Start]
	if !exists {
		return nil
	}
	if _, supervisor := start.(*graph.SupervisorNode); supervisor {
		return nil
	}
	reachable := map[string]struct{}{a.graph.Start: {}}
	queue := []string{a.graph.Start}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, edge := range a.out[from] {
			if graph.IsPseudoTarget(edge.edge.To) {
				continue
			}
			if _, exists := a.byID[edge.edge.To]; !exists {
				continue
			}
			if _, seen := reachable[edge.edge.To]; seen {
				continue
			}
			reachable[edge.edge.To] = struct{}{}
			queue = append(queue, edge.edge.To)
		}
	}
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, supervisor := node.(*graph.SupervisorNode); supervisor {
			continue
		}
		if _, exists := reachable[node.Base().ID]; !exists {
			diagnostics = append(diagnostics, diagnostic("reachability", SeverityError,
				"node is unreachable from start", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) edgeTargetExists() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		for _, edge := range routingEdges(node) {
			if _, exists := a.byID[edge.To]; !exists && !graph.IsPseudoTarget(edge.To) {
				diagnostics = append(diagnostics, edgeDiagnostic("edge_target_exists",
					fmt.Sprintf("edge target %q does not exist", edge.To), node.Base().ID, edge.To))
			}
		}
	}
	return diagnostics
}

func (a *analysis) edgeTargetUnique() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		seen := map[string]struct{}{}
		for _, edge := range graph.ChoiceEdges(node) {
			if _, duplicate := seen[edge.To]; duplicate {
				diagnostics = append(diagnostics, edgeDiagnostic("edge_target_unique",
					fmt.Sprintf("choice target %q appears more than once", edge.To), node.Base().ID, edge.To))
				continue
			}
			seen[edge.To] = struct{}{}
		}
	}
	return diagnostics
}

func (a *analysis) deadEnd() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		empty := false
		switch node := node.(type) {
		case *graph.CodergenNode:
			empty = len(node.Edges) == 0
		case *graph.FanInNode:
			empty = len(node.Edges) == 0
		case *graph.ParallelNode:
			empty = len(node.Branches) == 0
		}
		if empty {
			diagnostics = append(diagnostics, diagnostic("dead_end", SeverityError,
				"walk node must have at least one routing target", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) parallelFanIn() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if block.unresolved {
			continue
		}
		if !block.converged {
			diagnostics = append(diagnostics, diagnostic("parallel_fan_in", SeverityError,
				"every branch must converge on one fan-in without first reaching exit or a dead region", block.node.ID))
		}
	}
	return diagnostics
}

func (a *analysis) branchDisjoint() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if !block.converged {
			continue
		}
		for i := range block.branches {
			for j := i + 1; j < len(block.branches); j++ {
				for _, id := range intersection(block.branches[i].nodes, block.branches[j].nodes) {
					diagnostics = append(diagnostics, diagnostic("branch_disjoint", SeverityError,
						fmt.Sprintf("node belongs to branches %q and %q", block.branches[i].root, block.branches[j].root), id))
				}
			}
		}
	}
	return diagnostics
}

func (a *analysis) noNestedParallel() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if !block.converged {
			continue
		}
		for _, node := range a.graph.Nodes {
			if _, inside := block.union[node.Base().ID]; !inside {
				continue
			}
			if _, nested := node.(*graph.ParallelNode); nested {
				diagnostics = append(diagnostics, diagnostic("no_nested_parallel", SeverityError,
					fmt.Sprintf("parallel node is nested inside branches of %q", block.node.ID), node.Base().ID))
			}
		}
	}
	return diagnostics
}

func (a *analysis) fanInSingleParallel() []Diagnostic {
	owners := map[string][]string{}
	for _, block := range a.parallelBlocks() {
		if block.candidate != "" {
			owners[block.candidate] = append(owners[block.candidate], block.node.ID)
		}
	}
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.FanInNode); !ok {
			continue
		}
		count := len(owners[node.Base().ID])
		if count != 1 {
			diagnostics = append(diagnostics, diagnostic("fan_in_single_parallel", SeverityError,
				fmt.Sprintf("fan-in must belong to exactly one parallel node; found %d", count), node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) parallelThreadDisjoint() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if !block.converged || !block.disjoint {
			continue
		}
		keys := make([]map[string]string, len(block.branches))
		for i, branch := range block.branches {
			keys[i] = a.threadNodes(branch.nodes)
		}
		for i := range keys {
			for j := i + 1; j < len(keys); j++ {
				common := make([]string, 0)
				for key := range keys[i] {
					if _, exists := keys[j][key]; exists {
						common = append(common, key)
					}
				}
				slices.Sort(common)
				for _, key := range common {
					diagnostics = append(diagnostics, diagnostic("parallel_thread_disjoint", SeverityError,
						fmt.Sprintf("thread key %q is shared across concurrent branches by %q and %q", key, keys[i][key], keys[j][key]), keys[i][key]))
				}
			}
		}
	}
	return diagnostics
}

func (a *analysis) threadBranchBoundary() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if !block.converged || !block.disjoint {
			continue
		}
		for _, branch := range block.branches {
			seenKeys := map[string]struct{}{}
			for _, inside := range a.graph.Nodes {
				if _, member := branch.nodes[inside.Base().ID]; !member {
					continue
				}
				key, reusable := a.reusableThread(inside)
				if !reusable {
					continue
				}
				if _, seen := seenKeys[key]; seen {
					continue
				}
				seenKeys[key] = struct{}{}
				for _, outside := range a.graph.Nodes {
					if _, member := branch.nodes[outside.Base().ID]; member {
						continue
					}
					outsideKey, outsideReusable := a.reusableThread(outside)
					if outsideReusable && outsideKey == key {
						diagnostics = append(diagnostics, diagnostic("thread_branch_boundary", SeverityError,
							fmt.Sprintf("thread key %q crosses the branch worktree boundary between %q and %q", key, inside.Base().ID, outside.Base().ID), inside.Base().ID))
						break
					}
				}
			}
		}
	}
	return diagnostics
}

func (a *analysis) fanInEntry() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if !block.converged {
			continue
		}
		for _, incoming := range a.in[block.candidate] {
			if _, inside := block.union[incoming.from]; !inside {
				diagnostics = append(diagnostics, edgeDiagnostic("fan_in_entry",
					fmt.Sprintf("fan-in %q may only be entered from its branches", block.candidate), incoming.from, block.candidate))
			}
		}
	}
	return diagnostics
}

func (a *analysis) branchEntry() []Diagnostic {
	var diagnostics []Diagnostic
	for _, block := range a.parallelBlocks() {
		if !block.converged || !block.disjoint {
			continue
		}
		for _, branch := range block.branches {
			for _, node := range a.graph.Nodes {
				id := node.Base().ID
				if _, member := branch.nodes[id]; !member {
					continue
				}
				for _, incoming := range a.in[id] {
					_, inside := branch.nodes[incoming.from]
					fromParallel := id == branch.root && incoming.from == block.node.ID
					if !inside && !fromParallel {
						diagnostics = append(diagnostics, edgeDiagnostic("branch_entry",
							fmt.Sprintf("branch node %q may only be entered from its own branch", id), incoming.from, id))
					}
				}
			}
		}
	}
	return diagnostics
}

func (a *analysis) maxVisitsPositive() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		value := graph.MaxVisits(node)
		if value.Present && value.Value <= 0 {
			diagnostics = append(diagnostics, diagnostic("max_visits_positive", SeverityError,
				"max_visits must be positive", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) maxParallelPositive() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		parallel, ok := node.(*graph.ParallelNode)
		if ok && parallel.MaxParallel.Present && parallel.MaxParallel.Value <= 0 {
			diagnostics = append(diagnostics, diagnostic("max_parallel_positive", SeverityError,
				"max_parallel must be positive", parallel.ID))
		}
	}
	return diagnostics
}

func (a *analysis) maxRetriesNonnegative() []Diagnostic {
	var diagnostics []Diagnostic
	invalidDefault := a.graph.Defaults.MaxRetries.Present && a.graph.Defaults.MaxRetries.Value < 0
	if invalidDefault {
		diagnostics = append(diagnostics, diagnostic("max_retries_nonnegative", SeverityError,
			"defaults.max_retries must be nonnegative", ""))
	}
	for _, node := range a.graph.Nodes {
		value, present := maxRetries(node)
		if !present || value >= 0 {
			continue
		}
		if invalidDefault && value == a.graph.Defaults.MaxRetries.Value {
			continue
		}
		diagnostics = append(diagnostics, diagnostic("max_retries_nonnegative", SeverityError,
			"max_retries must be nonnegative", node.Base().ID))
	}
	return diagnostics
}

func (a *analysis) edgeConditionMissing() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		edges := graph.ChoiceEdges(node)
		if len(edges) <= 1 {
			continue
		}
		for _, edge := range edges {
			if strings.TrimSpace(edge.Condition) == "" {
				diagnostics = append(diagnostics, edgeDiagnostic("edge_condition_missing",
					"choice edge condition must be non-empty", node.Base().ID, edge.To))
			}
		}
	}
	return diagnostics
}

func (a *analysis) supervisesValid() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		supervisor, ok := node.(*graph.SupervisorNode)
		if !ok {
			continue
		}
		if len(supervisor.Supervises) == 0 {
			diagnostics = append(diagnostics, diagnostic("supervises_valid", SeverityError,
				"supervises must be non-empty", supervisor.ID))
		}
		seen := map[string]struct{}{}
		for _, target := range supervisor.Supervises {
			if target == supervisor.ID {
				diagnostics = append(diagnostics, diagnostic("supervises_valid", SeverityError,
					"a supervisor cannot supervise itself", supervisor.ID))
			}
			if _, duplicate := seen[target]; duplicate {
				diagnostics = append(diagnostics, diagnostic("supervises_valid", SeverityError,
					fmt.Sprintf("supervises target %q appears more than once", target), supervisor.ID))
			}
			if _, exists := a.byID[target]; !exists {
				diagnostics = append(diagnostics, diagnostic("supervises_valid", SeverityError,
					fmt.Sprintf("supervises target %q does not exist", target), supervisor.ID))
			}
			seen[target] = struct{}{}
		}
		interval, err := supervisor.IntervalValue().Parse()
		if err != nil || interval <= 0 {
			diagnostics = append(diagnostics, diagnostic("supervises_valid", SeverityError,
				"supervisor interval must be positive", supervisor.ID))
		}
	}
	return diagnostics
}

func (a *analysis) supervisorNotTargeted() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, supervisor := node.(*graph.SupervisorNode); !supervisor {
			continue
		}
		for _, incoming := range a.in[node.Base().ID] {
			diagnostics = append(diagnostics, edgeDiagnostic("supervisor_not_targeted",
				fmt.Sprintf("supervisor %q cannot be a routing target", node.Base().ID), incoming.from, node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) supervisorCycle() []Diagnostic {
	color := map[string]uint8{}
	var visit func(string) *Diagnostic
	visit = func(id string) *Diagnostic {
		color[id] = 1
		node, _ := a.byID[id].(*graph.SupervisorNode)
		for _, target := range node.Supervises {
			if _, supervisor := a.byID[target].(*graph.SupervisorNode); !supervisor {
				continue
			}
			if color[target] == 1 {
				finding := diagnostic("supervisor_cycle", SeverityError,
					fmt.Sprintf("supervision relation contains a cycle through %q", target), id)
				return &finding
			}
			if color[target] == 0 {
				if finding := visit(target); finding != nil {
					return finding
				}
			}
		}
		color[id] = 2
		return nil
	}
	for _, node := range a.graph.Nodes {
		if _, supervisor := node.(*graph.SupervisorNode); !supervisor || color[node.Base().ID] != 0 {
			continue
		}
		if finding := visit(node.Base().ID); finding != nil {
			return []Diagnostic{*finding}
		}
	}
	return nil
}

func (a *analysis) fidelityValid() []Diagnostic {
	var diagnostics []Diagnostic
	invalidDefault := false
	if a.graph.Defaults.Fidelity.Present {
		_, valid := a.opts.supportedFidelity[a.graph.Defaults.Fidelity.Value]
		invalidDefault = !valid
		if !valid {
			diagnostics = append(diagnostics, diagnostic("fidelity_valid", SeverityError,
				fmt.Sprintf("unsupported defaults.fidelity %q", a.graph.Defaults.Fidelity.Value), ""))
		}
	}
	for _, node := range a.graph.Nodes {
		fields, ok := llmFields(node)
		if !ok || !fields.Fidelity.Present {
			continue
		}
		if _, valid := a.opts.supportedFidelity[fields.Fidelity.Value]; valid {
			continue
		}
		if invalidDefault && fields.Fidelity.Value == a.graph.Defaults.Fidelity.Value {
			continue
		}
		diagnostics = append(diagnostics, diagnostic("fidelity_valid", SeverityError,
			fmt.Sprintf("unsupported fidelity %q", fields.Fidelity.Value), node.Base().ID))
	}
	return diagnostics
}

func (a *analysis) threadIDCollision() []Diagnostic {
	ids := make(map[string]struct{}, len(a.graph.Nodes))
	for _, node := range a.graph.Nodes {
		ids[node.Base().ID] = struct{}{}
	}
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		fields, ok := llmFields(node)
		if !ok || !fields.ThreadID.Present {
			continue
		}
		if _, collision := ids[fields.ThreadID.Value]; collision {
			diagnostics = append(diagnostics, diagnostic("thread_id_collision", SeverityError,
				fmt.Sprintf("explicit thread_id %q collides with a node ID", fields.ThreadID.Value), node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) threadHarnessConsistent() []Diagnostic {
	groups := map[string][]graph.Node{}
	var order []string
	for _, node := range a.graph.Nodes {
		key, reusable := a.reusableThread(node)
		if !reusable {
			continue
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], node)
	}
	var diagnostics []Diagnostic
	for _, key := range order {
		nodes := groups[key]
		if len(nodes) < 2 {
			continue
		}
		harnesses := map[string]struct{}{}
		resolutionFailed := false
		for _, node := range nodes {
			harness, err := a.threadHarness(node)
			if err != nil {
				diagnostics = append(diagnostics, diagnostic("thread_harness_consistent", SeverityError,
					fmt.Sprintf("cannot resolve harness for shared thread %q: %v", key, err), node.Base().ID))
				resolutionFailed = true
				break
			}
			harnesses[harness] = struct{}{}
		}
		if !resolutionFailed && len(harnesses) > 1 {
			diagnostics = append(diagnostics, diagnostic("thread_harness_consistent", SeverityError,
				fmt.Sprintf("nodes sharing thread key %q resolve to different harnesses", key), nodes[0].Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) fanInMaxVisits() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if fanIn, ok := node.(*graph.FanInNode); ok && fanIn.MaxVisits.Present {
			diagnostics = append(diagnostics, diagnostic("fan_in_max_visits", SeverityWarning,
				"max_visits on fan-in does not bound the parallel loop", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) branchRootMaxVisits() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		parallel, ok := node.(*graph.ParallelNode)
		if !ok {
			continue
		}
		seen := map[string]struct{}{}
		for _, target := range parallel.BranchIDs() {
			if _, duplicate := seen[target]; duplicate {
				continue
			}
			seen[target] = struct{}{}
			root, exists := a.byID[target]
			if exists && graph.MaxVisits(root).Present {
				diagnostics = append(diagnostics, diagnostic("branch_root_max_visits", SeverityWarning,
					fmt.Sprintf("max_visits on branch root silently shrinks later fan-outs from %q", parallel.ID), root.Base().ID))
			}
		}
	}
	return diagnostics
}

func (a *analysis) promptOnLLMNodes() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		codergen, ok := node.(*graph.CodergenNode)
		if ok && (!codergen.Prompt.Present || strings.TrimSpace(codergen.Prompt.Value) == "") {
			diagnostics = append(diagnostics, diagnostic("prompt_on_llm_nodes", SeverityWarning,
				"codergen node should have a non-empty prompt", codergen.ID))
		}
	}
	return diagnostics
}

func (a *analysis) threadNodes(nodes map[string]struct{}) map[string]string {
	keys := map[string]string{}
	for _, node := range a.graph.Nodes {
		if _, inside := nodes[node.Base().ID]; !inside {
			continue
		}
		if key, reusable := a.reusableThread(node); reusable {
			if _, exists := keys[key]; !exists {
				keys[key] = node.Base().ID
			}
		}
	}
	return keys
}

func maxRetries(node graph.Node) (int, bool) {
	switch node := node.(type) {
	case *graph.CodergenNode:
		return node.MaxRetries.Value, node.MaxRetries.Present
	case *graph.FanInNode:
		return node.MaxRetries.Value, node.MaxRetries.Present
	default:
		return 0, false
	}
}
