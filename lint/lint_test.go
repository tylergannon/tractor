package lint_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/lint"
)

func TestEveryBuiltInRule(t *testing.T) {
	tests := []struct {
		rule     string
		severity lint.Severity
		graph    func() graph.Graph
		options  lint.Options
	}{
		{"start_node", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes = g.Nodes[1:]
			return g
		}, lint.Options{}},
		{"start_single_outgoing", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[0].Base().Edges = nil
			return g
		}, lint.Options{}},
		{"terminal_node", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes = g.Nodes[:2]
			return g
		}, lint.Options{}},
		{"reachability", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes = append(g.Nodes, codergen("orphan", edge("exit")))
			return g
		}, lint.Options{}},
		{"edge_target_exists", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].Base().Edges = []graph.Edge{edge("missing")}
			return g
		}, lint.Options{}},
		{"start_no_incoming", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].Base().Edges = []graph.Edge{edge("start"), edgeWithCondition("exit", "done")}
			return g
		}, lint.Options{}},
		{"exit_no_outgoing", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[2].Base().Edges = []graph.Edge{edge("work")}
			return g
		}, lint.Options{}},
		{"dead_end", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].Base().Edges = nil
			return g
		}, lint.Options{}},
		{"tool_routing", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1] = &graph.ToolNode{NodeBase: graph.NodeBase{ID: "work", Edges: []graph.Edge{edge("exit"), edge("exit"), edge("exit")}}, ToolCommand: "true", OnFail: "exit"}
			return g
		}, lint.Options{}},
		{"parallel_fan_in", lint.SeverityError, malformedSCCParallel, lint.Options{}},
		{"branch_disjoint", lint.SeverityError, overlappingParallel, lint.Options{}},
		{"no_nested_parallel", lint.SeverityError, nestedParallel, lint.Options{}},
		{"fan_in_single_parallel", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes = append(g.Nodes, fanIn("unowned", edge("exit")))
			return g
		}, lint.Options{}},
		{"parallel_thread_disjoint", lint.SeverityError, func() graph.Graph {
			g := validParallel()
			llm(g, "left").ThreadID = set("shared")
			llm(g, "right").ThreadID = set("shared")
			return g
		}, lint.Options{}},
		{"thread_branch_boundary", lint.SeverityError, func() graph.Graph {
			g := validParallel()
			llm(g, "left").ThreadID = set("shared")
			llm(g, "join").ThreadID = set("shared")
			return g
		}, lint.Options{}},
		{"fan_in_entry", lint.SeverityError, directFanInParallel, lint.Options{}},
		{"branch_entry", lint.SeverityError, externalBranchEntry, lint.Options{}},
		{"max_visits_positive", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].Base().MaxVisits = set(0)
			return g
		}, lint.Options{}},
		{"max_parallel_positive", lint.SeverityError, func() graph.Graph {
			g := validParallel()
			g.Nodes[1].(*graph.ParallelNode).MaxParallel = set(0)
			return g
		}, lint.Options{}},
		{"max_retries_nonnegative", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].(*graph.CodergenNode).MaxRetries = set(-1)
			return g
		}, lint.Options{}},
		{"edge_condition_missing", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes = append(g.Nodes, codergen("alternate", edge("exit")))
			g.Nodes[1].Base().Edges = []graph.Edge{edge("alternate"), edgeWithCondition("exit", "done")}
			return g
		}, lint.Options{}},
		{"type_known", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1] = &graph.CustomNode{NodeBase: graph.NodeBase{ID: "work", Edges: []graph.Edge{edge("exit")}}, Type: "review"}
			return g
		}, lint.Options{}},
		{"fidelity_valid", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].(*graph.CodergenNode).Fidelity = set("summary")
			return g
		}, lint.Options{}},
		{"thread_id_collision", lint.SeverityError, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].(*graph.CodergenNode).ThreadID = set("exit")
			return g
		}, lint.Options{}},
		{"thread_harness_consistent", lint.SeverityError, sharedThreadLinear, lint.Options{
			ResolveHarness: func(provider, _ string) (string, error) { return provider, nil },
		}},
		{"fan_in_max_visits", lint.SeverityWarning, func() graph.Graph {
			g := validParallel()
			g.Nodes[4].Base().MaxVisits = set(2)
			return g
		}, lint.Options{}},
		{"branch_root_max_visits", lint.SeverityWarning, func() graph.Graph {
			g := validParallel()
			g.Nodes[2].Base().MaxVisits = set(2)
			return g
		}, lint.Options{}},
		{"prompt_on_llm_nodes", lint.SeverityWarning, func() graph.Graph {
			g := validLinear()
			g.Nodes[1].(*graph.CodergenNode).Prompt = jsonschema.Optional[string]{}
			return g
		}, lint.Options{}},
	}

	covered := make([]string, 0, len(tests))
	for _, test := range tests {
		t.Run(test.rule, func(t *testing.T) {
			diagnostics := lint.New(test.options).Validate(test.graph())
			finding, ok := findDiagnostic(diagnostics, test.rule)
			if !ok {
				t.Fatalf("missing %s in %#v", test.rule, diagnostics)
			}
			if finding.Severity != test.severity {
				t.Fatalf("%s severity = %q, want %q", test.rule, finding.Severity, test.severity)
			}
		})
		covered = append(covered, test.rule)
	}
	if len(covered) != 28 {
		t.Fatalf("covered %d built-in rules, want 28", len(covered))
	}
}

func TestParallelConvergenceAllowsCycleWithRouteToFanIn(t *testing.T) {
	g := validParallel()
	left := g.Nodes[2].(*graph.CodergenNode)
	left.Edges = []graph.Edge{
		edgeWithCondition("left", "continue another iteration"),
		edgeWithCondition("join", "branch work is complete"),
	}
	assertNoErrors(t, lint.Validate(g))
}

func TestParallelConvergenceRejectsClosedSCCAndDeadEnd(t *testing.T) {
	tests := map[string]graph.Graph{
		"closed SCC": malformedSCCParallel(),
		"dead end": func() graph.Graph {
			g := validParallel()
			g.Nodes = append(g.Nodes, codergen("dead"))
			g.Nodes[2].Base().Edges = []graph.Edge{
				edgeWithCondition("join", "done"),
				edgeWithCondition("dead", "get stuck"),
			}
			return g
		}(),
	}
	for name, g := range tests {
		t.Run(name, func(t *testing.T) {
			if _, ok := findDiagnostic(lint.Validate(g), "parallel_fan_in"); !ok {
				t.Fatal("missing parallel_fan_in")
			}
		})
	}
}

func TestMissingBranchTargetDoesNotCascadeIntoParallelRules(t *testing.T) {
	g := validParallel()
	g.Nodes[1].Base().Edges[0].To = "missing"
	diagnostics := lint.Validate(g)
	if _, ok := findDiagnostic(diagnostics, "edge_target_exists"); !ok {
		t.Fatal("missing edge_target_exists")
	}
	for _, rule := range []string{
		"parallel_fan_in", "branch_disjoint", "no_nested_parallel",
		"parallel_thread_disjoint", "thread_branch_boundary", "fan_in_entry", "branch_entry",
	} {
		assertNoRule(t, diagnostics, rule)
	}
}

func TestThreadRulesUseResolvedReusableSessions(t *testing.T) {
	t.Run("none fidelity is excluded", func(t *testing.T) {
		g := validParallel()
		for _, id := range []string{"left", "right"} {
			fields := llm(g, id)
			fields.ThreadID = set("shared")
			fields.Fidelity = set("none")
		}
		diagnostics := lint.Validate(g)
		assertNoRule(t, diagnostics, "parallel_thread_disjoint")
		assertNoRule(t, diagnostics, "thread_branch_boundary")
	})

	t.Run("different providers may share one exact harness", func(t *testing.T) {
		g := sharedThreadLinear()
		validator := lint.New(lint.Options{ResolveHarness: func(_, _ string) (string, error) {
			return "shared-harness", nil
		}})
		assertNoRule(t, validator.Validate(g), "thread_harness_consistent")
	})

	t.Run("resolver failure blocks a shared thread", func(t *testing.T) {
		g := sharedThreadLinear()
		validator := lint.New(lint.Options{ResolveHarness: func(_, _ string) (string, error) {
			return "", errors.New("unroutable")
		}})
		if _, ok := findDiagnostic(validator.Validate(g), "thread_harness_consistent"); !ok {
			t.Fatal("missing thread_harness_consistent")
		}
	})
}

func TestRuntimeConfigurationControlsKnownValues(t *testing.T) {
	g := validLinear()
	g.Nodes[1] = &graph.CustomNode{NodeBase: graph.NodeBase{ID: "work", Edges: []graph.Edge{edge("exit")}}, Type: "review"}
	validator := lint.New(lint.Options{KnownCustomTypes: []string{"review"}})
	assertNoRule(t, validator.Validate(g), "type_known")

	g = validLinear()
	g.Nodes[1].(*graph.CodergenNode).Fidelity = set("snapshot")
	validator = lint.New(lint.Options{SupportedFidelity: []string{"snapshot"}})
	assertNoRule(t, validator.Validate(g), "fidelity_valid")
}

func TestPromptWarningUsesAuthoredPresence(t *testing.T) {
	tests := []struct {
		name   string
		prompt jsonschema.Optional[string]
		label  jsonschema.Optional[string]
		warn   bool
	}{
		{"both absent", jsonschema.Optional[string]{}, jsonschema.Optional[string]{}, true},
		{"explicit empty prompt", set(""), jsonschema.Optional[string]{}, false},
		{"explicit empty label", jsonschema.Optional[string]{}, set(""), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := validLinear()
			node := g.Nodes[1].(*graph.CodergenNode)
			node.Prompt = test.prompt
			node.Label = test.label
			_, warned := findDiagnostic(lint.Validate(g), "prompt_on_llm_nodes")
			if warned != test.warn {
				t.Fatalf("warned = %v, want %v", warned, test.warn)
			}
		})
	}
}

func TestDiagnosticsAreDeterministicAndCarryEdgeIdentity(t *testing.T) {
	g := validLinear()
	g.Nodes = append(g.Nodes, codergen("alternate", edge("exit")))
	g.Nodes[1].Base().Edges = []graph.Edge{edge("alternate"), edge("exit")}
	first := lint.Validate(g)
	second := lint.Validate(g)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("diagnostics differ:\n%#v\n%#v", first, second)
	}
	finding, ok := findDiagnostic(first, "edge_condition_missing")
	if !ok || finding.Edge == nil || *finding.Edge != (lint.EdgeRef{"work", "alternate"}) {
		t.Fatalf("edge diagnostic = %#v", finding)
	}
}

func TestValidateOrErrorAndCustomRule(t *testing.T) {
	validator := lint.New(lint.Options{})
	validDiagnostics, err := validator.ValidateOrError(validLinear(), namedRule{})
	if err != nil {
		t.Fatal(err)
	}
	if finding, ok := findDiagnostic(validDiagnostics, "custom_note"); !ok || finding.Severity != lint.SeverityInfo {
		t.Fatalf("custom diagnostic = %#v", validDiagnostics)
	}

	invalid := validLinear()
	invalid.Nodes[1].Base().Edges = nil
	diagnostics, err := validator.ValidateOrError(invalid)
	var validationError *lint.ValidationError
	if !errors.As(err, &validationError) || len(validationError.Diagnostics) != len(diagnostics) {
		t.Fatalf("error = %#v; diagnostics = %#v", err, diagnostics)
	}
	if !lint.HasErrors(diagnostics) {
		t.Fatal("HasErrors returned false")
	}
}

type namedRule struct{}

func (namedRule) Name() string { return "custom_note" }
func (namedRule) Apply(graph.Graph) []lint.Diagnostic {
	return []lint.Diagnostic{{Severity: lint.SeverityInfo, Message: "custom rule ran"}}
}

func validLinear() graph.Graph {
	return graph.Graph{Nodes: []graph.Node{
		&graph.StartNode{NodeBase: graph.NodeBase{ID: "start", Edges: []graph.Edge{edge("work")}}},
		codergen("work", edge("exit")),
		&graph.ExitNode{NodeBase: graph.NodeBase{ID: "exit"}},
	}}
}

func validParallel() graph.Graph {
	return graph.Graph{Nodes: []graph.Node{
		&graph.StartNode{NodeBase: graph.NodeBase{ID: "start", Edges: []graph.Edge{edge("parallel")}}},
		&graph.ParallelNode{NodeBase: graph.NodeBase{ID: "parallel", Edges: []graph.Edge{edge("left"), edge("right")}}},
		codergen("left", edge("join")),
		codergen("right", edge("join")),
		fanIn("join", edge("exit")),
		&graph.ExitNode{NodeBase: graph.NodeBase{ID: "exit"}},
	}}
}

func malformedSCCParallel() graph.Graph {
	g := validParallel()
	g.Nodes = append(g.Nodes, codergen("cycle", edge("cycle")))
	g.Nodes[2].Base().Edges = []graph.Edge{
		edgeWithCondition("join", "done"),
		edgeWithCondition("cycle", "get stuck"),
	}
	return g
}

func overlappingParallel() graph.Graph {
	g := validParallel()
	g.Nodes = append(g.Nodes, codergen("shared", edge("join")))
	g.Nodes[2].Base().Edges = []graph.Edge{edge("shared")}
	g.Nodes[3].Base().Edges = []graph.Edge{edge("shared")}
	return g
}

func nestedParallel() graph.Graph {
	g := validParallel()
	nested := &graph.ParallelNode{NodeBase: graph.NodeBase{ID: "nested", Edges: []graph.Edge{edge("join")}}}
	g.Nodes = append(g.Nodes, nested)
	g.Nodes[2].Base().Edges = []graph.Edge{edge("nested")}
	return g
}

func directFanInParallel() graph.Graph {
	g := validParallel()
	g.Nodes[1].Base().Edges = []graph.Edge{edge("join"), edge("join")}
	return g
}

func externalBranchEntry() graph.Graph {
	g := validParallel()
	pre := codergen("pre",
		edgeWithCondition("parallel", "fan out"),
		edgeWithCondition("left", "enter branch directly"),
	)
	g.Nodes[0].Base().Edges = []graph.Edge{edge("pre")}
	g.Nodes = append(g.Nodes, pre)
	return g
}

func sharedThreadLinear() graph.Graph {
	g := validLinear()
	first := g.Nodes[1].(*graph.CodergenNode)
	first.Edges = []graph.Edge{edge("second")}
	first.ThreadID = set("shared")
	first.LLMProvider = set("openai")
	second := codergen("second", edge("exit"))
	second.ThreadID = set("shared")
	second.LLMProvider = set("anthropic")
	g.Nodes = slices.Insert(g.Nodes, 2, graph.Node(second))
	return g
}

func codergen(id string, edges ...graph.Edge) *graph.CodergenNode {
	return &graph.CodergenNode{
		NodeBase:      graph.NodeBase{ID: id, Edges: edges},
		LLMNodeFields: graph.LLMNodeFields{Prompt: set("work")},
	}
}

func fanIn(id string, edges ...graph.Edge) *graph.FanInNode {
	return &graph.FanInNode{
		NodeBase:      graph.NodeBase{ID: id, Edges: edges},
		LLMNodeFields: graph.LLMNodeFields{Prompt: set("evaluate")},
	}
}

func llm(g graph.Graph, id string) *graph.LLMNodeFields {
	for _, node := range g.Nodes {
		if node.Base().ID != id {
			continue
		}
		switch node := node.(type) {
		case *graph.CodergenNode:
			return &node.LLMNodeFields
		case *graph.FanInNode:
			return &node.LLMNodeFields
		}
	}
	panic("missing LLM node " + id)
}

func edge(to string) graph.Edge { return graph.Edge{To: to} }
func edgeWithCondition(to, condition string) graph.Edge {
	return graph.Edge{To: to, Condition: condition}
}

func set[T any](value T) jsonschema.Optional[T] {
	return jsonschema.Optional[T]{Present: true, Value: value}
}

func findDiagnostic(diagnostics []lint.Diagnostic, rule string) (lint.Diagnostic, bool) {
	for _, diagnostic := range diagnostics {
		if diagnostic.Rule == rule {
			return diagnostic, true
		}
	}
	return lint.Diagnostic{}, false
}

func assertNoErrors(t *testing.T, diagnostics []lint.Diagnostic) {
	t.Helper()
	if lint.HasErrors(diagnostics) {
		t.Fatalf("unexpected errors: %#v", diagnostics)
	}
}

func assertNoRule(t *testing.T, diagnostics []lint.Diagnostic, rule string) {
	t.Helper()
	if diagnostic, ok := findDiagnostic(diagnostics, rule); ok {
		t.Fatalf("unexpected %s: %#v", rule, diagnostic)
	}
}
