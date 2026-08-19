// Package graph defines and parses Tractor pipeline documents.
package graph

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
)

//go:generate go tool gen-jsonschema gen --pretty --validate --formats=both
//go:generate go run ./internal/schemafix

// Graph is a complete pipeline definition.
type Graph struct {
	// Name is the pipeline's display name.
	Name string `json:"name,omitzero"`

	// Goal is the pipeline objective exposed to prompt expansion.
	Goal string `json:"goal,omitzero"`

	// Defaults contains file-level defaults for node fields.
	Defaults Defaults `json:"defaults,omitzero"`

	// Start names the walk node where execution begins.
	Start string `json:"start"`

	// Nodes is the graph. Each node carries its outgoing edges.
	Nodes []Node `json:"nodes"`
}

// Defaults contains the six fields that may be inherited by nodes.
type Defaults struct {
	MaxRetries      jsonschema.Optional[int]      `json:"max_retries,omitzero"`
	Fidelity        jsonschema.Optional[string]   `json:"fidelity,omitzero"`
	Timeout         jsonschema.Optional[Duration] `json:"timeout,omitzero"`
	LLMModel        jsonschema.Optional[string]   `json:"llm_model,omitzero"`
	LLMProvider     jsonschema.Optional[string]   `json:"llm_provider,omitzero"`
	ReasoningEffort jsonschema.Optional[string]   `json:"reasoning_effort,omitzero"`
}

// Duration is an integer followed by ms, s, m, h, or d.
type Duration string

// Parse converts d to a time.Duration. Days are fixed 24-hour periods.
func (d Duration) Parse() (time.Duration, error) {
	s := string(d)
	var number string
	switch {
	case strings.HasSuffix(s, "ms"):
		number = strings.TrimSuffix(s, "ms")
	case len(s) > 0 && strings.ContainsRune("smhd", rune(s[len(s)-1])):
		number = s[:len(s)-1]
	default:
		return 0, fmt.Errorf("parse duration %q: expected integer followed by ms, s, m, h, or d", s)
	}
	if number == "" {
		return 0, fmt.Errorf("parse duration %q: expected integer followed by ms, s, m, h, or d", s)
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("parse duration %q: expected integer followed by ms, s, m, h, or d", s)
		}
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseInt(number, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return parsed, nil
}

// Edge is an outgoing transition owned by its origin node.
type Edge struct {
	To        string `json:"to"`
	Condition string `json:"condition,omitzero"`
}

// Node is a pipeline stage.
type Node interface {
	isNode()
	Base() *NodeBase
	NodeType() string
}

// NodeBase contains fields admitted on every node type.
type NodeBase struct {
	ID    string                      `json:"id"`
	Label jsonschema.Optional[string] `json:"label,omitzero"`
}

// DisplayLabel returns the configured label or the node ID.
func (n *NodeBase) DisplayLabel() string {
	if n.Label.Present {
		return n.Label.Value
	}
	return n.ID
}

// CodergenNode runs an LLM task.
type CodergenNode struct {
	NodeBase
	Edges     []Edge                   `json:"edges,omitzero"`
	MaxVisits jsonschema.Optional[int] `json:"max_visits,omitzero"`
	LLMNodeFields
	synthesized bool
}

func (*CodergenNode) isNode()           {}
func (n *CodergenNode) Base() *NodeBase { return &n.NodeBase }
func (*CodergenNode) NodeType() string  { return "codergen" }

// IsSynthesized reports whether the node was resolved from a structured
// parallel branch.
func (n *CodergenNode) IsSynthesized() bool { return n.synthesized }

// FanInNode evaluates parallel branch evidence with an LLM turn.
type FanInNode struct {
	NodeBase
	Edges     []Edge                   `json:"edges,omitzero"`
	MaxVisits jsonschema.Optional[int] `json:"max_visits,omitzero"`
	LLMNodeFields
}

func (*FanInNode) isNode()           {}
func (n *FanInNode) Base() *NodeBase { return &n.NodeBase }
func (*FanInNode) NodeType() string  { return "parallel.fan_in" }

// LLMNodeFields are shared by codergen and parallel fan-in nodes.
type LLMNodeFields struct {
	Prompt          jsonschema.Optional[string]   `json:"prompt,omitzero"`
	MaxRetries      jsonschema.Optional[int]      `json:"max_retries,omitzero"`
	Fidelity        jsonschema.Optional[string]   `json:"fidelity,omitzero"`
	ThreadID        jsonschema.Optional[string]   `json:"thread_id,omitzero"`
	Timeout         jsonschema.Optional[Duration] `json:"timeout,omitzero"`
	LLMModel        jsonschema.Optional[string]   `json:"llm_model,omitzero"`
	LLMProvider     jsonschema.Optional[string]   `json:"llm_provider,omitzero"`
	ReasoningEffort jsonschema.Optional[string]   `json:"reasoning_effort,omitzero"`
}

// CodergenOverride selectively replaces fields inherited from a parallel
// node's Codergen configuration. Every field is optional by design.
type CodergenOverride struct {
	Label           jsonschema.Optional[string]   `json:"label,omitzero"`
	Prompt          jsonschema.Optional[string]   `json:"prompt,omitzero"`
	MaxRetries      jsonschema.Optional[int]      `json:"max_retries,omitzero"`
	MaxVisits       jsonschema.Optional[int]      `json:"max_visits,omitzero"`
	Fidelity        jsonschema.Optional[string]   `json:"fidelity,omitzero"`
	ThreadID        jsonschema.Optional[string]   `json:"thread_id,omitzero"`
	Timeout         jsonschema.Optional[Duration] `json:"timeout,omitzero"`
	LLMModel        jsonschema.Optional[string]   `json:"llm_model,omitzero"`
	LLMProvider     jsonschema.Optional[string]   `json:"llm_provider,omitzero"`
	ReasoningEffort jsonschema.Optional[string]   `json:"reasoning_effort,omitzero"`
}

// PromptValue returns a non-empty prompt or falls back to label.
func (n *LLMNodeFields) PromptValue(label string) string {
	if n.Prompt.Present && n.Prompt.Value != "" {
		return n.Prompt.Value
	}
	return label
}

// ToolNode executes one shell command.
type ToolNode struct {
	NodeBase
	ToolCommand string                        `json:"tool_command"`
	OnSuccess   string                        `json:"on_success"`
	OnError     jsonschema.Optional[string]   `json:"on_error,omitzero"`
	Timeout     jsonschema.Optional[Duration] `json:"timeout,omitzero"`
	MaxVisits   jsonschema.Optional[int]      `json:"max_visits,omitzero"`
}

func (*ToolNode) isNode()           {}
func (n *ToolNode) Base() *NodeBase { return &n.NodeBase }
func (*ToolNode) NodeType() string  { return "tool" }

// WorkspacePolicy controls whether parallel branches receive separate Git
// worktrees or run together in the caller's workspace.
type WorkspacePolicy string

const (
	WorkspaceIsolated WorkspacePolicy = "isolated"
	WorkspaceShared   WorkspacePolicy = "shared"
)

// ParallelBranch is either a legacy branch-root reference or a synthesized
// Codergen branch with declared output artifacts.
type ParallelBranch struct {
	ID        string                                `json:"id"`
	Artifacts []string                              `json:"artifacts"`
	Codergen  jsonschema.Optional[CodergenOverride] `json:"codergen,omitzero"`
	legacy    bool
}

// LegacyParallelBranch constructs an existing-style branch-root reference.
func LegacyParallelBranch(id string) ParallelBranch {
	return ParallelBranch{ID: id, legacy: true}
}

// LegacyParallelBranches constructs existing-style branch-root references.
func LegacyParallelBranches(ids ...string) []ParallelBranch {
	branches := make([]ParallelBranch, len(ids))
	for index, id := range ids {
		branches[index] = LegacyParallelBranch(id)
	}
	return branches
}

// IsLegacy reports whether the branch was authored as a string reference.
func (b ParallelBranch) IsLegacy() bool { return b.legacy }

// ParallelNode concurrently walks each outgoing branch.
type ParallelNode struct {
	NodeBase
	Branches    []ParallelBranch                     `json:"branches"`
	Edges       []Edge                               `json:"edges,omitzero"`
	MaxParallel jsonschema.Optional[int]             `json:"max_parallel,omitzero"`
	MaxVisits   jsonschema.Optional[int]             `json:"max_visits,omitzero"`
	Workspace   jsonschema.Optional[WorkspacePolicy] `json:"workspace,omitzero"`
	LLMNodeFields
}

func (*ParallelNode) isNode()           {}
func (n *ParallelNode) Base() *NodeBase { return &n.NodeBase }
func (*ParallelNode) NodeType() string  { return "parallel" }

// SupervisorNode observes declared nodes and coaches them outside the walk.
type SupervisorNode struct {
	NodeBase
	Prompt          string                        `json:"prompt"`
	Supervises      []string                      `json:"supervises"`
	Interval        jsonschema.Optional[Duration] `json:"interval,omitzero"`
	Timeout         jsonschema.Optional[Duration] `json:"timeout,omitzero"`
	LLMModel        jsonschema.Optional[string]   `json:"llm_model,omitzero"`
	LLMProvider     jsonschema.Optional[string]   `json:"llm_provider,omitzero"`
	ReasoningEffort jsonschema.Optional[string]   `json:"reasoning_effort,omitzero"`
}

func (*SupervisorNode) isNode()           {}
func (n *SupervisorNode) Base() *NodeBase { return &n.NodeBase }
func (*SupervisorNode) NodeType() string  { return "supervisor" }

// MaxParallelValue returns the explicit maximum or the system default.
func (n *ParallelNode) MaxParallelValue() int {
	if n.MaxParallel.Present {
		return n.MaxParallel.Value
	}
	return 4
}

// WorkspacePolicyValue returns the explicit workspace policy or the
// compatibility-preserving isolated default.
func (n *ParallelNode) WorkspacePolicyValue() WorkspacePolicy {
	if n.Workspace.Present {
		return n.Workspace.Value
	}
	return WorkspaceIsolated
}

// BranchIDs returns branch roots in authored order.
func (n *ParallelNode) BranchIDs() []string {
	ids := make([]string, len(n.Branches))
	for index, branch := range n.Branches {
		ids[index] = branch.ID
	}
	return ids
}

// Branch returns branch metadata by ID.
func (n *ParallelNode) Branch(id string) (ParallelBranch, bool) {
	for _, branch := range n.Branches {
		if branch.ID == id {
			return branch, true
		}
	}
	return ParallelBranch{}, false
}

// IntervalValue returns the explicit patrol interval or the system default.
func (n *SupervisorNode) IntervalValue() Duration {
	if n.Interval.Present {
		return n.Interval.Value
	}
	return "60s"
}

const (
	// Success is the terminal pseudo-target that completes a run.
	Success = "success"
	// Failure is the terminal pseudo-target that deliberately fails a run.
	Failure = "failure"
)

// IsPseudoTarget reports whether target names a terminal pseudo-target.
func IsPseudoTarget(target string) bool { return target == Success || target == Failure }

// RoutingTargets returns the authored routing targets for a walk node.
func RoutingTargets(node Node) []string {
	switch node := node.(type) {
	case *CodergenNode:
		return edgeTargets(node.Edges)
	case *FanInNode:
		return edgeTargets(node.Edges)
	case *ToolNode:
		targets := []string{node.OnSuccess}
		if node.OnError.Present {
			targets = append(targets, node.OnError.Value)
		}
		return targets
	case *ParallelNode:
		return node.BranchIDs()
	default:
		return nil
	}
}

// ChoiceEdges returns the authored choice edges for a chooser node.
func ChoiceEdges(node Node) []Edge {
	switch node := node.(type) {
	case *CodergenNode:
		return node.Edges
	case *FanInNode:
		return node.Edges
	default:
		return nil
	}
}

// MaxVisits returns a node's optional visit budget.
func MaxVisits(node Node) jsonschema.Optional[int] {
	switch node := node.(type) {
	case *CodergenNode:
		return node.MaxVisits
	case *FanInNode:
		return node.MaxVisits
	case *ToolNode:
		return node.MaxVisits
	case *ParallelNode:
		return node.MaxVisits
	default:
		return jsonschema.Optional[int]{}
	}
}

func edgeTargets(edges []Edge) []string {
	targets := make([]string, len(edges))
	for i, edge := range edges {
		targets[i] = edge.To
	}
	return targets
}

// ThreadKey returns the explicit thread ID or the node ID.
func (n *LLMNodeFields) ThreadKey(nodeID string) string {
	if n.ThreadID.Present {
		return n.ThreadID.Value
	}
	return nodeID
}

// FidelityValue returns the resolved fidelity or its system default.
func (n *LLMNodeFields) FidelityValue() string {
	if n.Fidelity.Present {
		return n.Fidelity.Value
	}
	return "compacted"
}
