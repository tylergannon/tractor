package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const outputSchemaURL = "urn:tractor:harness:output-schema"

// ResultValidator validates native harness output against one caller-supplied
// JSON Schema.
type ResultValidator struct {
	schema *jsonschema.Schema
}

// NewResultValidator compiles the exact supplied schema and verifies that its
// root describes a JSON object.
func NewResultValidator(rawSchema json.RawMessage) (*ResultValidator, *Error) {
	document, err := decodeOneJSON(rawSchema)
	if err != nil {
		return nil, terminalError(fmt.Sprintf("invalid output schema: %v", err))
	}
	object, ok := document.(map[string]any)
	if !ok {
		return nil, terminalError("output schema must be a JSON object")
	}
	if object["type"] != "object" {
		return nil, terminalError("output schema root type must be object")
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(outputSchemaURL, object); err != nil {
		return nil, terminalError(fmt.Sprintf("invalid output schema: %v", err))
	}
	compiled, err := compiler.Compile(outputSchemaURL)
	if err != nil {
		return nil, terminalError(fmt.Sprintf("invalid output schema: %v", err))
	}
	return &ResultValidator{schema: compiled}, nil
}

// Validate decodes exactly one JSON object and validates it without modifying
// or replacing the caller's schema.
func (v *ResultValidator) Validate(rawResult []byte) (Result, *Error) {
	if v == nil || v.schema == nil {
		return nil, terminalError("result validator is not initialized")
	}
	value, err := decodeOneJSON(rawResult)
	if err != nil {
		return nil, terminalError(fmt.Sprintf("invalid harness result: %v", err))
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, terminalError("harness result must be a JSON object")
	}
	if err := v.schema.Validate(object); err != nil {
		return nil, terminalError(fmt.Sprintf("harness result does not conform to output schema: %v", err))
	}
	return Result(object), nil
}

// ValidateResult compiles rawSchema and validates rawResult against it.
func ValidateResult(rawSchema json.RawMessage, rawResult []byte) (Result, *Error) {
	validator, err := NewResultValidator(rawSchema)
	if err != nil {
		return nil, err
	}
	return validator.Validate(rawResult)
}

func decodeOneJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}
