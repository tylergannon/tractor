package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// codexCompatibleOutputSchema makes optional root properties compatible with
// Codex strict output schemas without changing the caller's validation schema.
func codexCompatibleOutputSchema(raw json.RawMessage) (map[string]any, map[string]struct{}, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, fmt.Errorf("decode output schema: %w", err)
	}
	rawProperties, exists := root["properties"]
	if !exists {
		return root, map[string]struct{}{}, nil
	}
	properties, ok := rawProperties.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("output schema properties is not an object")
	}

	required := make([]any, 0, len(properties))
	requiredSet := make(map[string]struct{}, len(properties))
	if rawRequired, exists := root["required"]; exists {
		values, ok := rawRequired.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("output schema required is not an array")
		}
		for _, value := range values {
			name, ok := value.(string)
			if !ok {
				return nil, nil, fmt.Errorf("output schema required contains a non-string value")
			}
			required = append(required, name)
			requiredSet[name] = struct{}{}
		}
	}

	optionalNames := make([]string, 0, len(properties))
	for name := range properties {
		if _, exists := requiredSet[name]; !exists {
			optionalNames = append(optionalNames, name)
		}
	}
	sort.Strings(optionalNames)
	optional := make(map[string]struct{}, len(optionalNames))
	for _, name := range optionalNames {
		property, ok := properties[name].(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("output schema property %q is not an object", name)
		}
		if err := makeNullable(property); err != nil {
			return nil, nil, fmt.Errorf("make output schema property %q nullable: %w", name, err)
		}
		required = append(required, name)
		optional[name] = struct{}{}
	}
	root["required"] = required
	return root, optional, nil
}

func makeNullable(property map[string]any) error {
	switch propertyType := property["type"].(type) {
	case string:
		if propertyType != "null" {
			property["type"] = []any{propertyType, "null"}
		}
	case []any:
		for _, value := range propertyType {
			if value == "null" {
				return addNullToEnum(property)
			}
		}
		property["type"] = append(propertyType, "null")
	default:
		return fmt.Errorf("property type is not a string or array")
	}
	return addNullToEnum(property)
}

func addNullToEnum(property map[string]any) error {
	rawEnum, exists := property["enum"]
	if !exists {
		return nil
	}
	values, ok := rawEnum.([]any)
	if !ok {
		return fmt.Errorf("property enum is not an array")
	}
	for _, value := range values {
		if value == nil {
			return nil
		}
	}
	property["enum"] = append(values, nil)
	return nil
}

func omitOptionalNulls(raw []byte, optional map[string]struct{}) ([]byte, error) {
	if len(optional) == 0 {
		return raw, nil
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	for name := range optional {
		if bytes.Equal(bytes.TrimSpace(result[name]), []byte("null")) {
			delete(result, name)
		}
	}
	return json.Marshal(result)
}
