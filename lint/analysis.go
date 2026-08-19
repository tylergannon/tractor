package lint

import (
	"fmt"
	"slices"

	"github.com/tylergannon/tractor/graph"
)

type edgeRecord struct {
	from string
	edge graph.Edge
}

type analysis struct {
	graph graph.Graph
	opts  options
	byID  map[string]graph.Node
	out   map[string][]edgeRecord
	in    map[string][]edgeRecord

	parallelReady bool
	parallels     []*parallelBlock
}

func newAnalysis(g graph.Graph, opts options) *analysis {
	a := &analysis{
		graph: g,
		opts:  opts,
		byID:  make(map[string]graph.Node, len(g.Nodes)),
		out:   make(map[string][]edgeRecord, len(g.Nodes)),
		in:    make(map[string][]edgeRecord, len(g.Nodes)),
	}
	for _, node := range g.Nodes {
		a.byID[node.Base().ID] = node
	}
	for _, node := range g.Nodes {
		from := node.Base().ID
		for _, edge := range routingEdges(node) {
			record := edgeRecord{from: from, edge: edge}
			a.out[from] = append(a.out[from], record)
			a.in[edge.To] = append(a.in[edge.To], record)
		}
	}
	return a
}

func (a *analysis) builtInDiagnostics() []Diagnostic {
	rules := []func() []Diagnostic{
		a.startTarget,
		a.terminalReachable,
		a.reachability,
		a.edgeTargetExists,
		a.edgeTargetUnique,
		a.deadEnd,
		a.parallelFanIn,
		a.branchDisjoint,
		a.noNestedParallel,
		a.fanInSingleParallel,
		a.parallelThreadDisjoint,
		a.threadBranchBoundary,
		a.fanInEntry,
		a.branchEntry,
		a.maxVisitsPositive,
		a.maxParallelPositive,
		a.maxRetriesNonnegative,
		a.edgeConditionMissing,
		a.supervisesValid,
		a.supervisorNotTargeted,
		a.supervisorCycle,
		a.fidelityValid,
		a.threadIDCollision,
		a.threadHarnessConsistent,
		a.fanInMaxVisits,
		a.branchRootMaxVisits,
		a.promptOnLLMNodes,
	}
	var diagnostics []Diagnostic
	for _, rule := range rules {
		diagnostics = append(diagnostics, rule()...)
	}
	return diagnostics
}

type branchAnalysis struct {
	root        string
	nodes       map[string]struct{}
	firstFanIn  map[string]struct{}
	hitTerminal bool
	unresolved  bool
}

type parallelBlock struct {
	node       *graph.ParallelNode
	branches   []branchAnalysis
	candidate  string
	unresolved bool
	converged  bool
	disjoint   bool
	union      map[string]struct{}
}

func (a *analysis) parallelBlocks() []*parallelBlock {
	if a.parallelReady {
		return a.parallels
	}
	a.parallelReady = true
	for _, node := range a.graph.Nodes {
		parallel, ok := node.(*graph.ParallelNode)
		if !ok {
			continue
		}
		a.parallels = append(a.parallels, a.analyzeParallel(parallel))
	}
	return a.parallels
}

func (a *analysis) analyzeParallel(node *graph.ParallelNode) *parallelBlock {
	block := &parallelBlock{node: node, disjoint: true, union: map[string]struct{}{}}
	for _, target := range node.BranchIDs() {
		block.branches = append(block.branches, a.walkToFirstFanIn(target))
	}

	if len(block.branches) > 0 {
		candidate := ""
		candidateOK := true
		resolvedBranches := 0
		for _, branch := range block.branches {
			if branch.unresolved {
				block.unresolved = true
				continue
			}
			resolvedBranches++
			if len(branch.firstFanIn) != 1 {
				candidateOK = false
				break
			}
			for fanIn := range branch.firstFanIn {
				if candidate == "" {
					candidate = fanIn
				} else if candidate != fanIn {
					candidateOK = false
				}
			}
		}
		if candidateOK && resolvedBranches > 0 {
			block.candidate = candidate
		}
	}

	if block.candidate != "" && !block.unresolved {
		block.converged = true
		for i := range block.branches {
			branch := &block.branches[i]
			if branch.hitTerminal || branch.unresolved ||
				!a.allNodesReachFanIn(branch.nodes, block.candidate) {
				block.converged = false
			}
			for id := range branch.nodes {
				block.union[id] = struct{}{}
			}
		}
	}

	if block.converged {
		for i := range block.branches {
			for j := i + 1; j < len(block.branches); j++ {
				if setsIntersect(block.branches[i].nodes, block.branches[j].nodes) {
					block.disjoint = false
				}
			}
		}
	}
	return block
}

func (a *analysis) walkToFirstFanIn(root string) branchAnalysis {
	branch := branchAnalysis{
		root: root, nodes: map[string]struct{}{}, firstFanIn: map[string]struct{}{},
	}
	seen := map[string]struct{}{}
	stack := []string{root}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		if graph.IsPseudoTarget(id) {
			branch.hitTerminal = true
			continue
		}
		node, exists := a.byID[id]
		if !exists {
			branch.unresolved = true
			continue
		}
		switch node.(type) {
		case *graph.FanInNode:
			branch.firstFanIn[id] = struct{}{}
			continue
		}
		branch.nodes[id] = struct{}{}
		edges := a.out[id]
		for _, edge := range slices.Backward(edges) {
			if graph.IsPseudoTarget(edge.edge.To) {
				branch.hitTerminal = true
				continue
			}
			if _, exists := a.byID[edge.edge.To]; !exists {
				branch.unresolved = true
				continue
			}
			stack = append(stack, edge.edge.To)
		}
	}
	return branch
}

func routingEdges(node graph.Node) []graph.Edge {
	if edges := graph.ChoiceEdges(node); edges != nil {
		return edges
	}
	targets := graph.RoutingTargets(node)
	edges := make([]graph.Edge, len(targets))
	for i, target := range targets {
		edges[i] = graph.Edge{To: target}
	}
	return edges
}

func (a *analysis) allNodesReachFanIn(nodes map[string]struct{}, fanIn string) bool {
	reachable := map[string]struct{}{fanIn: {}}
	queue := []string{fanIn}
	for len(queue) > 0 {
		to := queue[0]
		queue = queue[1:]
		for _, incoming := range a.in[to] {
			from := incoming.from
			if _, allowed := nodes[from]; !allowed {
				continue
			}
			if _, seen := reachable[from]; seen {
				continue
			}
			reachable[from] = struct{}{}
			queue = append(queue, from)
		}
	}
	for id := range nodes {
		if _, exists := reachable[id]; !exists {
			return false
		}
	}
	return true
}

func setsIntersect(left, right map[string]struct{}) bool {
	if len(left) > len(right) {
		left, right = right, left
	}
	for value := range left {
		if _, exists := right[value]; exists {
			return true
		}
	}
	return false
}

func intersection(left, right map[string]struct{}) []string {
	values := make([]string, 0)
	for value := range left {
		if _, exists := right[value]; exists {
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return values
}

func (a *analysis) effectiveFidelity(fields *graph.LLMNodeFields) string {
	if fields.Fidelity.Present {
		return fields.Fidelity.Value
	}
	if a.graph.Defaults.Fidelity.Present {
		return a.graph.Defaults.Fidelity.Value
	}
	return "compacted"
}

func (a *analysis) reusableThread(node graph.Node) (string, bool) {
	fields, ok := llmFields(node)
	if !ok {
		return "", false
	}
	fidelity := a.effectiveFidelity(fields)
	if fidelity != "full" && fidelity != "compacted" {
		return "", false
	}
	return fields.ThreadKey(node.Base().ID), true
}

func (a *analysis) resolvedModel(fields *graph.LLMNodeFields) (provider, model string) {
	if fields.LLMProvider.Present {
		provider = fields.LLMProvider.Value
	} else if a.graph.Defaults.LLMProvider.Present {
		provider = a.graph.Defaults.LLMProvider.Value
	}
	if fields.LLMModel.Present {
		model = fields.LLMModel.Value
	} else if a.graph.Defaults.LLMModel.Present {
		model = a.graph.Defaults.LLMModel.Value
	}
	return provider, model
}

func llmFields(node graph.Node) (*graph.LLMNodeFields, bool) {
	switch node := node.(type) {
	case *graph.CodergenNode:
		return &node.LLMNodeFields, true
	case *graph.FanInNode:
		return &node.LLMNodeFields, true
	default:
		return nil, false
	}
}

func (a *analysis) threadHarness(node graph.Node) (string, error) {
	if a.opts.resolveHarness == nil {
		return "", fmt.Errorf("harness resolver is not configured")
	}
	fields, ok := llmFields(node)
	if !ok {
		return "", fmt.Errorf("node %q does not run an LLM turn", node.Base().ID)
	}
	provider, model := a.resolvedModel(fields)
	return a.opts.resolveHarness(provider, model)
}
