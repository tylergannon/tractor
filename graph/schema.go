//go:build jsonschema

package graph

import (
	"encoding/json"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
)

func (Graph) Schema() json.RawMessage   { panic("not implemented") }
func (Graph) ValidateJSON([]byte) error { panic("not implemented") }
func (Graph) ValidateYAML([]byte) error { panic("not implemented") }

var _ = jsonschema.NewJSONSchemaMethod(
	Graph.Schema,
	jsonschema.WithInterface(
		Graph{}.Nodes,
		jsonschema.Discriminator("type"),
		jsonschema.Impl("start", (*StartNode)(nil)),
		jsonschema.Impl("exit", (*ExitNode)(nil)),
		jsonschema.Impl("codergen", (*CodergenNode)(nil)),
		jsonschema.Impl("parallel", (*ParallelNode)(nil)),
		jsonschema.Impl("parallel.fan_in", (*FanInNode)(nil)),
		jsonschema.Impl("tool", (*ToolNode)(nil)),
		jsonschema.Impl("__custom__", (*CustomNode)(nil)),
	),
)
