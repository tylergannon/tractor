// Command schemafix applies lexical constraints not expressed by Go types.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

const (
	schemaPath      = "jsonschema/Graph.json"
	idPattern       = `^[A-Za-z_][A-Za-z0-9_]*$`
	durationPattern = `^[0-9]+(ms|s|m|h|d)$`
)

func main() {
	check := flag.Bool("check", false, "fail instead of writing when normalization is needed")
	flag.Parse()
	if err := fixSchema(*check); err != nil {
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
	root["required"] = []any{"start", "nodes"}
	properties := object(root["properties"])
	nodes := object(properties["nodes"])
	items := object(nodes["items"])
	options, ok := items["anyOf"].([]any)
	if !ok || len(options) != 5 {
		return fmt.Errorf("unexpected node union shape")
	}
	for _, raw := range options {
		option := object(raw)
		props := object(option["properties"])
		typeName, _ := object(props["type"])["const"].(string)
		required := []any{"type", "id"}
		switch typeName {
		case "parallel":
			required = append(required, "branches")
		case "tool":
			required = append(required, "tool_command", "on_success")
		case "supervisor":
			required = append(required, "prompt", "supervises")
		}
		option["required"] = required
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
				id["not"] = map[string]any{"enum": []any{"success", "failure"}}
			}
			if timeout, ok := props["timeout"].(map[string]any); ok {
				timeout["pattern"] = durationPattern
			}
			if interval, ok := props["interval"].(map[string]any); ok {
				interval["pattern"] = durationPattern
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
