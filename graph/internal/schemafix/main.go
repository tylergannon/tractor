// Command schemafix applies the three structural constraints that
// go-gen-jsonschema cannot express directly: lexical string patterns, an open
// opaque object, and a catch-all discriminated-union case.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

const (
	schemaPath      = "jsonschema/Graph.json"
	goPath          = "jsonschema_gen.go"
	idPattern       = `^[A-Za-z_][A-Za-z0-9_]*$`
	durationPattern = `^[0-9]+(ms|s|m|h|d)$`
)

var builtins = []any{"start", "exit", "codergen", "parallel", "parallel.fan_in", "tool"}

func main() {
	check := flag.Bool("check", false, "fail instead of writing when normalization is needed")
	flag.Parse()
	if err := fixSchema(*check); err != nil {
		panic(err)
	}
	if err := fixDecoder(*check); err != nil {
		panic(err)
	}
}

func fixSchema(check bool) error {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	root["required"] = []any{"nodes"}
	root["$defs"] = map[string]any{
		"nonNullJSON": nonNullJSONSchema(),
	}

	properties := object(root["properties"])
	nodes := object(properties["nodes"])
	items := object(nodes["items"])
	options, ok := items["anyOf"].([]any)
	if !ok || len(options) != 7 {
		return fmt.Errorf("unexpected node union shape")
	}
	for _, raw := range options {
		option := object(raw)
		props := object(option["properties"])
		typeSchema := object(props["type"])
		if _, custom := props["custom"]; custom {
			delete(typeSchema, "const")
			typeSchema["not"] = map[string]any{"enum": builtins}
			customSchema := object(props["custom"])
			customSchema["additionalProperties"] = map[string]any{"$ref": "#/$defs/nonNullJSON"}
			option["required"] = []any{"type", "id"}
		} else {
			required := []any{"type", "id"}
			if typeSchema["const"] == "tool" {
				required = append(required, "tool_command")
			}
			option["required"] = required
		}
	}
	walk(root)

	normalized, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	normalized = append(normalized, '\n')
	if check {
		if !bytes.Equal(data, normalized) {
			return fmt.Errorf("%s needs schema normalization", schemaPath)
		}
		return nil
	}
	return os.WriteFile(schemaPath, normalized, 0o644)
}

func nonNullJSONSchema() map[string]any {
	ref := map[string]any{"$ref": "#/$defs/nonNullJSON"}
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
			map[string]any{"type": "array", "items": ref},
			map[string]any{"type": "object", "additionalProperties": ref},
		},
	}
}

func walk(value any) {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			walk(item)
		}
	case map[string]any:
		if props, ok := current["properties"].(map[string]any); ok {
			if id, ok := props["id"].(map[string]any); ok {
				id["pattern"] = idPattern
			}
			if timeout, ok := props["timeout"].(map[string]any); ok {
				timeout["pattern"] = durationPattern
			}
			if effort, ok := props["reasoning_effort"].(map[string]any); ok {
				effort["enum"] = []any{"low", "medium", "high"}
			}
			if _, edge := props["to"]; edge {
				current["required"] = []any{"to"}
			}
		}
		for _, child := range current {
			walk(child)
		}
	}
}

func object(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		panic(fmt.Sprintf("expected object, got %T", value))
	}
	return object
}

func fixDecoder(check bool) error {
	data, err := os.ReadFile(goPath)
	if err != nil {
		return err
	}
	old := "\tdefault:\n\t\treturn nil, fmt.Errorf(\"unknown discriminator: %s\", discriminator)\n"
	replacement := "\tdefault:\n\t\tvar obj CustomNode\n\t\tif err = json.Unmarshal(data, &obj); err != nil {\n\t\t\treturn nil, err\n\t\t}\n\t\treturn &obj, nil\n"
	if strings.Count(string(data), old) == 0 && strings.Count(string(data), replacement) == 1 {
		return nil
	}
	if strings.Count(string(data), old) != 1 {
		return fmt.Errorf("unexpected generated discriminator switch")
	}
	data = []byte(strings.Replace(string(data), old, replacement, 1))
	if check {
		return fmt.Errorf("%s needs custom-node decoder normalization", goPath)
	}
	return os.WriteFile(goPath, data, 0o644)
}
