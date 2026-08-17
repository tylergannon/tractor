package graph

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
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
	seen := make(map[string]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		id := node.Base().ID
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("parse pipeline: duplicate node ID %q", id)
		}
		seen[id] = struct{}{}
		if node.Base().Edges == nil {
			node.Base().Edges = []Edge{}
		}
	}
	graph.applyDefaults()
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
		case *CustomNode:
			inherit(&current.MaxRetries, g.Defaults.MaxRetries)
			if current.Custom.value == nil {
				current.Custom.value = map[string]any{}
			}
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
