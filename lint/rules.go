package lint

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tylergannon/tractor/graph"
)

func (a *analysis) startNode() []Diagnostic {
	count := 0
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.StartNode); ok {
			count++
		}
	}
	if count == 1 {
		return nil
	}
	return []Diagnostic{diagnostic("start_node", SeverityError,
		fmt.Sprintf("pipeline must have exactly one start node; found %d", count), "")}
}

func (a *analysis) startSingleOutgoing() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.StartNode); ok && len(node.Base().Edges) != 1 {
			diagnostics = append(diagnostics, diagnostic("start_single_outgoing", SeverityError,
				fmt.Sprintf("start node must have exactly one outgoing edge; found %d", len(node.Base().Edges)), node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) terminalNode() []Diagnostic {
	count := 0
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.ExitNode); ok {
			count++
		}
	}
	if count == 1 {
		return nil
	}
	return []Diagnostic{diagnostic("terminal_node", SeverityError,
		fmt.Sprintf("pipeline must have exactly one exit node; found %d", count), "")}
}

func (a *analysis) reachability() []Diagnostic {
	startID := ""
	count := 0
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.StartNode); ok {
			startID = node.Base().ID
			count++
		}
	}
	if count != 1 {
		return nil
	}
	reachable := map[string]struct{}{startID: {}}
	queue := []string{startID}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, edge := range a.out[from] {
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
		for _, edge := range node.Base().Edges {
			if _, exists := a.byID[edge.To]; !exists {
				diagnostics = append(diagnostics, edgeDiagnostic("edge_target_exists",
					fmt.Sprintf("edge target %q does not exist", edge.To), node.Base().ID, edge.To))
			}
		}
	}
	return diagnostics
}

func (a *analysis) startNoIncoming() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.StartNode); ok && len(a.in[node.Base().ID]) > 0 {
			diagnostics = append(diagnostics, diagnostic("start_no_incoming", SeverityError,
				"start node must have no incoming edges", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) exitNoOutgoing() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, ok := node.(*graph.ExitNode); ok && len(node.Base().Edges) > 0 {
			diagnostics = append(diagnostics, diagnostic("exit_no_outgoing", SeverityError,
				"exit node must have no outgoing edges", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) deadEnd() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, terminal := node.(*graph.ExitNode); !terminal && len(node.Base().Edges) == 0 {
			diagnostics = append(diagnostics, diagnostic("dead_end", SeverityError,
				"non-terminal node must have at least one outgoing edge", node.Base().ID))
		}
	}
	return diagnostics
}

func (a *analysis) toolRouting() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		tool, ok := node.(*graph.ToolNode)
		if !ok {
			continue
		}
		count := len(tool.Edges)
		valid := count == 1 || count == 2
		if tool.OnFail != "" {
			found := false
			for _, edge := range tool.Edges {
				found = found || edge.To == tool.OnFail
			}
			valid = valid && found
		} else if count == 2 {
			valid = false
		}
		if !valid {
			diagnostics = append(diagnostics, diagnostic("tool_routing", SeverityError,
				"tool node must have one or two outgoing edges; two edges require on_fail naming an outgoing target", tool.ID))
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
		value := node.Base().MaxVisits
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
		_, codergen := node.(*graph.CodergenNode)
		_, fanIn := node.(*graph.FanInNode)
		if (!codergen && !fanIn) || len(node.Base().Edges) <= 1 {
			continue
		}
		for _, edge := range node.Base().Edges {
			if strings.TrimSpace(edge.Condition) == "" {
				diagnostics = append(diagnostics, edgeDiagnostic("edge_condition_missing",
					"choice edge condition must be non-empty", node.Base().ID, edge.To))
			}
		}
	}
	return diagnostics
}

func (a *analysis) typeKnown() []Diagnostic {
	var diagnostics []Diagnostic
	for _, node := range a.graph.Nodes {
		if _, known := a.opts.knownTypes[node.NodeType()]; !known {
			diagnostics = append(diagnostics, diagnostic("type_known", SeverityError,
				fmt.Sprintf("node type %q is not registered", node.NodeType()), node.Base().ID))
		}
	}
	return diagnostics
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
		if _, fanIn := node.(*graph.FanInNode); fanIn && node.Base().MaxVisits.Present {
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
		for _, edge := range parallel.Edges {
			if _, duplicate := seen[edge.To]; duplicate {
				continue
			}
			seen[edge.To] = struct{}{}
			root, exists := a.byID[edge.To]
			if exists && root.Base().MaxVisits.Present {
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
		if ok && !codergen.Prompt.Present && !codergen.Label.Present {
			diagnostics = append(diagnostics, diagnostic("prompt_on_llm_nodes", SeverityWarning,
				"codergen node should have an authored prompt or label", codergen.ID))
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
	case *graph.CustomNode:
		return node.MaxRetries.Value, node.MaxRetries.Present
	default:
		return 0, false
	}
}
