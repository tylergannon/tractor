package graph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"go.yaml.in/yaml/v4"
)

// Parse validates and decodes one pipeline document, then applies file-level
// defaults to the node types that admit each field.
func Parse(data []byte) (*Graph, error) {
	if err := preflight(data); err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}
	if err := (Graph{}).ValidateJSON(data); err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}

	var graph Graph
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}
	if err := finishGraph(&graph); err != nil {
		return nil, fmt.Errorf("parse pipeline: %w", err)
	}
	return &graph, nil
}

func finishGraph(graph *Graph) error {
	if err := graph.expandParallelBranches(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		id := node.Base().ID
		if IsPseudoTarget(id) {
			return fmt.Errorf("node ID %q is reserved for a terminal pseudo-target", id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate node ID %q", id)
		}
		seen[id] = struct{}{}
	}
	graph.applyDefaults()
	return nil
}

// UnmarshalJSON accepts both existing string branch roots and structured
// Codergen branches without weakening the generated object schema.
func (n *ParallelNode) UnmarshalJSON(data []byte) error {
	type alias ParallelNode
	var payload struct {
		*alias
		Branches []json.RawMessage `json:"branches"`
	}
	payload.alias = (*alias)(n)
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	branches := make([]ParallelBranch, len(payload.Branches))
	for index, raw := range payload.Branches {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil {
			branches[index] = LegacyParallelBranch(id)
			continue
		}
		if err := json.Unmarshal(raw, &branches[index]); err != nil {
			return fmt.Errorf("field branches[%d]: %w", index, err)
		}
	}
	n.Branches = branches
	return nil
}

func (g *Graph) expandParallelBranches() error {
	authored := make(map[string]struct{}, len(g.Nodes))
	for _, node := range g.Nodes {
		authored[node.Base().ID] = struct{}{}
	}
	var synthesized []Node
	for _, node := range g.Nodes {
		parallel, ok := node.(*ParallelNode)
		if !ok || len(parallel.Branches) == 0 {
			continue
		}
		legacy := parallel.Branches[0].IsLegacy()
		for _, branch := range parallel.Branches {
			if branch.IsLegacy() != legacy {
				return fmt.Errorf("parallel node %q cannot mix string and object branches", parallel.ID)
			}
		}
		if legacy {
			continue
		}
		if len(parallel.Edges) == 0 {
			return fmt.Errorf("parallel node %q with Codergen branches must declare edges", parallel.ID)
		}
		for _, branch := range parallel.Branches {
			if _, exists := authored[branch.ID]; exists {
				return fmt.Errorf("parallel node %q branch ID %q collides with a declared node", parallel.ID, branch.ID)
			}
			if err := validateArtifactPaths(parallel.ID, branch); err != nil {
				return err
			}
			resolved := resolveParallelCodergen(parallel, branch)
			synthesized = append(synthesized, resolved)
			authored[branch.ID] = struct{}{}
		}
	}
	g.Nodes = append(g.Nodes, synthesized...)
	return nil
}

func resolveParallelCodergen(parent *ParallelNode, branch ParallelBranch) *CodergenNode {
	resolved := &CodergenNode{
		NodeBase:      NodeBase{ID: branch.ID, Label: parent.Label},
		Edges:         append([]Edge(nil), parent.Edges...),
		LLMNodeFields: parent.LLMNodeFields,
		synthesized:   true,
	}
	if !branch.Codergen.Present {
		return resolved
	}
	override := branch.Codergen.Value
	overrideOptional(&resolved.Label, override.Label)
	overrideOptional(&resolved.Prompt, override.Prompt)
	overrideOptional(&resolved.MaxRetries, override.MaxRetries)
	overrideOptional(&resolved.MaxVisits, override.MaxVisits)
	overrideOptional(&resolved.Fidelity, override.Fidelity)
	overrideOptional(&resolved.ThreadID, override.ThreadID)
	overrideOptional(&resolved.Timeout, override.Timeout)
	overrideOptional(&resolved.LLMModel, override.LLMModel)
	overrideOptional(&resolved.LLMProvider, override.LLMProvider)
	overrideOptional(&resolved.ReasoningEffort, override.ReasoningEffort)
	return resolved
}

func overrideOptional[T any](destination *jsonschema.Optional[T], source jsonschema.Optional[T]) {
	if source.Present {
		*destination = source
	}
}

func validateArtifactPaths(parallelID string, branch ParallelBranch) error {
	seen := make(map[string]struct{}, len(branch.Artifacts))
	for _, artifact := range branch.Artifacts {
		cleaned := filepath.Clean(artifact)
		if strings.TrimSpace(artifact) == "" || filepath.IsAbs(artifact) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("parallel node %q branch %q has invalid artifact path %q", parallelID, branch.ID, artifact)
		}
		if _, duplicate := seen[cleaned]; duplicate {
			return fmt.Errorf("parallel node %q branch %q repeats artifact path %q", parallelID, branch.ID, artifact)
		}
		seen[cleaned] = struct{}{}
	}
	return nil
}

// ParseYAML validates and decodes one YAML pipeline using the generated
// JSON Schema contract.
func ParseYAML(data []byte) (*Graph, error) {
	if err := (Graph{}).ValidateYAML(data); err != nil {
		return nil, fmt.Errorf("parse pipeline YAML: %w", err)
	}

	var graph Graph
	if err := yaml.Load(data, &graph, yaml.WithV4Defaults()); err != nil {
		return nil, fmt.Errorf("parse pipeline YAML: %w", err)
	}
	if err := finishGraph(&graph); err != nil {
		return nil, fmt.Errorf("parse pipeline YAML: %w", err)
	}
	return &graph, nil
}

// NodeByID returns the node with id, if present.
func (g *Graph) NodeByID(id string) (Node, bool) {
	for _, node := range g.Nodes {
		if node.Base().ID == id {
			return node, true
		}
	}
	return nil, false
}

func (g *Graph) applyDefaults() {
	for _, node := range g.Nodes {
		switch current := node.(type) {
		case *CodergenNode:
			inheritLLM(&current.LLMNodeFields, g.Defaults)
		case *FanInNode:
			inheritLLM(&current.LLMNodeFields, g.Defaults)
		case *ToolNode:
			inherit(&current.Timeout, g.Defaults.Timeout)
		case *SupervisorNode:
			inherit(&current.Timeout, g.Defaults.Timeout)
			inherit(&current.LLMModel, g.Defaults.LLMModel)
			inherit(&current.LLMProvider, g.Defaults.LLMProvider)
			inherit(&current.ReasoningEffort, g.Defaults.ReasoningEffort)
		}
	}
}

func inheritLLM(fields *LLMNodeFields, defaults Defaults) {
	inherit(&fields.MaxRetries, defaults.MaxRetries)
	inherit(&fields.Fidelity, defaults.Fidelity)
	inherit(&fields.Timeout, defaults.Timeout)
	inherit(&fields.LLMModel, defaults.LLMModel)
	inherit(&fields.LLMProvider, defaults.LLMProvider)
	inherit(&fields.ReasoningEffort, defaults.ReasoningEffort)
}

func inherit[T any](destination *jsonschema.Optional[T], source jsonschema.Optional[T]) {
	if !destination.Present && source.Present {
		*destination = source
	}
}

func preflight(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("more than one JSON value")
		}
		return err
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null is not allowed")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		members := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			if _, duplicate := members[name]; duplicate {
				return fmt.Errorf("duplicate object member %q", name)
			}
			members[name] = struct{}{}
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	_, err = decoder.Token()
	return err
}
