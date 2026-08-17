// Package schema contains the one Codex app-server request model Tractor
// generates and consumes.
package schema

//go:generate rm -rf json
//go:generate codex app-server generate-json-schema --out json
//go:generate cp json/v2/TurnStartParams.json TurnStartParams.json
//go:generate go tool go-jsonschema --only-models --package schema --struct-name-from-title --capitalization ID --tags json --output types_gen.go json/v2/TurnStartParams.json
//go:generate rm -rf json
