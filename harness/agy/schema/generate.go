// Package schema contains the generated tolerant outer envelope for agy's
// observed stream-json protocol.
package schema

//go:generate go tool go-jsonschema --only-models --package schema --struct-name-from-title --capitalization ID --tags json --output types_gen.go protocol.json
