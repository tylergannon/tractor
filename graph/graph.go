// Package graph defines and parses Tractor pipeline documents.
package graph

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
)

//go:generate go tool gen-jsonschema gen --pretty --validate
//go:generate go run ./internal/schemafix

// Graph is a complete pipeline definition.
type Graph struct {
	// Name is the pipeline's display name.
	Name string `json:"name,omitzero"`

	// Goal is the pipeline objective exposed to prompt expansion.
	Goal string `json:"goal,omitzero"`

	// Defaults contains file-level defaults for node fields.
	Defaults Defaults `json:"defaults,omitzero"`

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
	ID        string                      `json:"id"`
	Label     jsonschema.Optional[string] `json:"label,omitzero"`
	Edges     []Edge                      `json:"edges,omitzero"`
	MaxVisits jsonschema.Optional[int]    `json:"max_visits,omitzero"`
}

// DisplayLabel returns the configured label or the node ID.
func (n *NodeBase) DisplayLabel() string {
	if n.Label.Present {
		return n.Label.Value
	}
	return n.ID
}

// StartNode is the pipeline entry point.
type StartNode struct{ NodeBase }

func (*StartNode) isNode()           {}
func (n *StartNode) Base() *NodeBase { return &n.NodeBase }
func (*StartNode) NodeType() string  { return "start" }

// ExitNode is the pipeline terminal node.
type ExitNode struct{ NodeBase }

func (*ExitNode) isNode()           {}
func (n *ExitNode) Base() *NodeBase { return &n.NodeBase }
func (*ExitNode) NodeType() string  { return "exit" }

// CodergenNode runs an LLM task.
type CodergenNode struct {
	NodeBase
	LLMNodeFields
}

func (*CodergenNode) isNode()           {}
func (n *CodergenNode) Base() *NodeBase { return &n.NodeBase }
func (*CodergenNode) NodeType() string  { return "codergen" }

// FanInNode evaluates parallel branch evidence with an LLM turn.
type FanInNode struct {
	NodeBase
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
	OnFail      string                        `json:"on_fail,omitzero"`
	Timeout     jsonschema.Optional[Duration] `json:"timeout,omitzero"`
}

func (*ToolNode) isNode()           {}
func (n *ToolNode) Base() *NodeBase { return &n.NodeBase }
func (*ToolNode) NodeType() string  { return "tool" }

// ParallelNode concurrently walks each outgoing branch.
type ParallelNode struct {
	NodeBase
	MaxParallel jsonschema.Optional[int] `json:"max_parallel,omitzero"`
}

func (*ParallelNode) isNode()           {}
func (n *ParallelNode) Base() *NodeBase { return &n.NodeBase }
func (*ParallelNode) NodeType() string  { return "parallel" }

// CustomNode is the generic shape for a registered custom handler type.
type CustomNode struct {
	NodeBase
	Type       string                   `json:"type"`
	MaxRetries jsonschema.Optional[int] `json:"max_retries,omitzero"`
	Custom     OpaqueObject             `json:"custom,omitzero"`
}

func (*CustomNode) isNode()            {}
func (n *CustomNode) Base() *NodeBase  { return &n.NodeBase }
func (n *CustomNode) NodeType() string { return n.Type }

// OpaqueObject carries custom-handler configuration without interpretation.
// Its generated placeholder schema is opened by the deterministic schema
// normalization step because go-gen-jsonschema intentionally has no map type.
type OpaqueObject struct {
	value map[string]any
}

// Values returns the opaque object's decoded members.
func (o OpaqueObject) Values() map[string]any { return o.value }

func (o *OpaqueObject) UnmarshalJSON(data []byte) error {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.value = value
	return nil
}

func (o OpaqueObject) MarshalJSON() ([]byte, error) {
	if o.value == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(o.value)
}

// MaxParallelValue returns the explicit maximum or the system default.
func (n *ParallelNode) MaxParallelValue() int {
	if n.MaxParallel.Present {
		return n.MaxParallel.Value
	}
	return 4
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
