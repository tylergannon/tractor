package graph

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const representative = `{
  "name":"all_fields",
  "goal":"exercise parsing",
  "defaults":{
    "max_retries":2,
    "fidelity":"full",
    "timeout":"15m",
    "llm_model":"default-model",
    "llm_provider":"openai",
    "reasoning_effort":"high"
  },
  "nodes":[
    {"id":"start","type":"start","edges":[{"to":"code"}]},
    {"id":"code","type":"codergen","label":"Code","prompt":"work","llm_model":"override","thread_id":"shared","max_visits":3,"edges":[{"to":"tool"}]},
    {"id":"tool","type":"tool","tool_command":"go test ./...","on_fail":"code","edges":[{"to":"parallel"},{"to":"code","condition":"failed"}]},
    {"id":"parallel","type":"parallel","max_parallel":3,"edges":[{"to":"join"}]},
    {"id":"join","type":"parallel.fan_in","prompt":"choose","edges":[{"to":"done"}]},
    {"id":"extension","type":"notify.slack","max_retries":0,"custom":{"channel":"dev","nested":{"enabled":true},"order":[1,2]},"edges":[{"to":"done"}]},
    {"id":"done","type":"exit"}
  ]
}`

func TestParseRepresentativeGraphAndResolveDefaults(t *testing.T) {
	graph, err := Parse([]byte(representative))
	if err != nil {
		t.Fatal(err)
	}
	if graph.Name != "all_fields" || graph.Goal != "exercise parsing" || len(graph.Nodes) != 7 {
		t.Fatalf("graph = %#v", graph)
	}

	code := mustNode[*CodergenNode](t, graph, "code")
	if code.DisplayLabel() != "Code" || code.LLMModel.Value != "override" || code.ThreadKey(code.ID) != "shared" {
		t.Fatalf("codergen = %#v", code)
	}
	if !code.MaxRetries.Present || code.MaxRetries.Value != 2 || code.FidelityValue() != "full" || code.Timeout.Value != "15m" {
		t.Fatalf("codergen defaults = %#v", code.LLMNodeFields)
	}

	tool := mustNode[*ToolNode](t, graph, "tool")
	if !tool.Timeout.Present || tool.Timeout.Value != "15m" {
		t.Fatalf("tool timeout = %#v", tool.Timeout)
	}
	parallel := mustNode[*ParallelNode](t, graph, "parallel")
	if parallel.MaxParallelValue() != 3 {
		t.Fatalf("max_parallel = %d", parallel.MaxParallelValue())
	}
	join := mustNode[*FanInNode](t, graph, "join")
	if join.LLMModel.Value != "default-model" || join.LLMProvider.Value != "openai" || join.ReasoningEffort.Value != "high" {
		t.Fatalf("fan-in defaults = %#v", join.LLMNodeFields)
	}
	custom := mustNode[*CustomNode](t, graph, "extension")
	if custom.NodeType() != "notify.slack" || custom.MaxRetries.Value != 0 {
		t.Fatalf("custom = %#v", custom)
	}
	wantCustom := map[string]any{
		"channel": "dev",
		"nested":  map[string]any{"enabled": true},
		"order":   []any{float64(1), float64(2)},
	}
	if !reflect.DeepEqual(custom.Custom.Values(), wantCustom) {
		t.Fatalf("custom payload = %#v", custom.Custom.Values())
	}
	if edges := mustNode[*ExitNode](t, graph, "done").Edges; edges == nil || len(edges) != 0 {
		t.Fatalf("default edges = %#v", edges)
	}
}

func TestParseAcceptsEveryNodeShape(t *testing.T) {
	tests := []string{
		`{"id":"s","type":"start"}`,
		`{"id":"e","type":"exit"}`,
		`{"id":"c","type":"codergen","prompt":"p","max_retries":0,"fidelity":"none","thread_id":"t","timeout":"250ms","llm_model":"m","llm_provider":"p","reasoning_effort":"low"}`,
		`{"id":"p","type":"parallel","max_parallel":4}`,
		`{"id":"f","type":"parallel.fan_in","prompt":"p"}`,
		`{"id":"t","type":"tool","tool_command":"true","on_fail":"e","timeout":"2h"}`,
		`{"id":"x","type":"example.handler","max_retries":1,"custom":{"value":42}}`,
	}
	for _, node := range tests {
		document := `{"nodes":[` + node + `]}`
		if _, err := Parse([]byte(document)); err != nil {
			t.Errorf("Parse(%s): %v", node, err)
		}
	}
}

func TestParsePreservesAuthoredLabelAndPromptPresence(t *testing.T) {
	document := `{
  "nodes":[
    {"id":"omitted","type":"codergen"},
    {"id":"empty","type":"codergen","label":"","prompt":""}
  ]
}`
	graph, err := Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	omitted := mustNode[*CodergenNode](t, graph, "omitted")
	if omitted.Label.Present || omitted.Prompt.Present {
		t.Fatalf("omitted fields decoded as present: label=%#v prompt=%#v", omitted.Label, omitted.Prompt)
	}
	if omitted.DisplayLabel() != "omitted" || omitted.PromptValue(omitted.DisplayLabel()) != "omitted" {
		t.Fatalf("omitted defaults = label %q prompt %q", omitted.DisplayLabel(), omitted.PromptValue(omitted.DisplayLabel()))
	}
	empty := mustNode[*CodergenNode](t, graph, "empty")
	if !empty.Label.Present || empty.Label.Value != "" || !empty.Prompt.Present || empty.Prompt.Value != "" {
		t.Fatalf("explicit empty presence lost: label=%#v prompt=%#v", empty.Label, empty.Prompt)
	}
	if empty.DisplayLabel() != "" || empty.PromptValue(empty.DisplayLabel()) != "" {
		t.Fatalf("explicit empty defaults = label %q prompt %q", empty.DisplayLabel(), empty.PromptValue(empty.DisplayLabel()))
	}
	if err := (Graph{}).ValidateJSON([]byte(document)); err != nil {
		t.Fatalf("schema rejected valid presence variants: %v", err)
	}
}

func TestParseRejectsFieldsFromOtherNodeTypes(t *testing.T) {
	tests := []string{
		`{"id":"n","type":"start","prompt":"no"}`,
		`{"id":"n","type":"exit","custom":{}}`,
		`{"id":"n","type":"codergen","tool_command":"no"}`,
		`{"id":"n","type":"parallel","timeout":"1s"}`,
		`{"id":"n","type":"parallel.fan_in","max_parallel":2}`,
		`{"id":"n","type":"tool","tool_command":"true","prompt":"no"}`,
		`{"id":"n","type":"custom.kind","custom":{},"timeout":"1s"}`,
		`{"id":"n","type":"tool","tool_command":"true","custom":{}}`,
	}
	for _, node := range tests {
		document := `{"nodes":[` + node + `]}`
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("cross-type field admitted: %s", node)
		}
	}
}

func TestParseRejectsDuplicateObjectMembersAtEveryLevel(t *testing.T) {
	tests := map[string]string{
		"top level":     `{"nodes":[],"nodes":[]}`,
		"defaults":      `{"defaults":{"timeout":"1s","timeout":"2s"},"nodes":[]}`,
		"node":          `{"nodes":[{"id":"a","id":"b","type":"exit"}]}`,
		"edge":          `{"nodes":[{"id":"a","type":"start","edges":[{"to":"b","to":"c"}]}]}`,
		"custom nested": `{"nodes":[{"id":"a","type":"x","custom":{"nested":{"v":1,"v":2}}}]}`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(document))
			if err == nil || !strings.Contains(err.Error(), "duplicate object member") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseRejectsDuplicateAndInvalidNodeIDs(t *testing.T) {
	duplicate := `{"nodes":[{"id":"same","type":"start"},{"id":"same","type":"exit"}]}`
	if _, err := Parse([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate node ID") {
		t.Fatalf("duplicate error = %v", err)
	}

	for _, id := range []string{"", "1bad", "bad-name", "bad name"} {
		document := `{"nodes":[{"id":` + quoted(id) + `,"type":"exit"}]}`
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("invalid ID %q admitted", id)
		}
	}
}

func TestParseRejectsNullEverywhere(t *testing.T) {
	tests := []string{
		`{"name":null,"nodes":[]}`,
		`{"defaults":{"timeout":null},"nodes":[]}`,
		`{"nodes":[null]}`,
		`{"nodes":[{"id":"a","type":"exit","label":null}]}`,
		`{"nodes":[{"id":"a","type":"start","edges":[null]}]}`,
		`{"nodes":[{"id":"a","type":"custom.kind","custom":{"nested":[1,null]}}]}`,
	}
	for _, document := range tests {
		if _, err := Parse([]byte(document)); err == nil || !strings.Contains(err.Error(), "null is not allowed") {
			t.Errorf("null admitted or wrong error for %s: %v", document, err)
		}
	}
}

func TestParseRejectsWrongTypesAndUnknownFields(t *testing.T) {
	tests := []string{
		`[]`,
		`{"name":1,"nodes":[]}`,
		`{"nodes":{}}`,
		`{"defaults":{"max_retries":"one"},"nodes":[]}`,
		`{"nodes":[{"id":1,"type":"exit"}]}`,
		`{"nodes":[{"id":"a","type":"start","edges":"b"}]}`,
		`{"nodes":[{"id":"a","type":"start","edges":[{"to":2}]}]}`,
		`{"unknown":true,"nodes":[]}`,
		`{"defaults":{"unknown":true},"nodes":[]}`,
		`{"nodes":[{"id":"a","type":"exit","unknown":true}]}`,
		`{"nodes":[{"id":"a","type":"start","edges":[{"to":"b","unknown":true}]}]}`,
	}
	for _, document := range tests {
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("invalid document admitted: %s", document)
		}
	}
}

func TestParseRequiresStrictSingleJSONDocument(t *testing.T) {
	for _, document := range []string{
		``,
		`{"nodes":[],}`,
		`{"nodes":[]}// comment`,
		`{"nodes":[]} {"nodes":[]}`,
	} {
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("non-strict JSON admitted: %q", document)
		}
	}
}

func TestParseYAMLAcceptsCommentsAndLiteralBlocks(t *testing.T) {
	document := `
name: yaml
nodes:
  - id: start
    type: start
    edges: [{to: work}]
  # Keep commands readable.
  - id: work
    type: tool
    tool_command: |
      printf '%s\n' first
      printf '%s\n' second
    edges: [{to: done}]
  - id: done
    type: exit
`
	pipeline, err := ParseYAML([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	tool := mustNode[*ToolNode](t, pipeline, "work")
	want := "printf '%s\\n' first\nprintf '%s\\n' second\n"
	if tool.ToolCommand != want {
		t.Fatalf("tool command = %q, want %q", tool.ToolCommand, want)
	}
}

func TestParseYAMLRetainsJSONWorkflowConstraints(t *testing.T) {
	tests := map[string]string{
		"duplicate key":      "nodes: []\nnodes: []\n",
		"multiple documents": "nodes: []\n---\nnodes: []\n",
		"null":               "name: null\nnodes: []\n",
		"unknown field":      "unknown: true\nnodes: []\n",
		"non-string key":     "1: value\nnodes: []\n",
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseYAML([]byte(document)); err == nil {
				t.Fatal("invalid YAML pipeline admitted")
			}
		})
	}
}

func TestDefaultsWhitelistAndRequiredFields(t *testing.T) {
	for _, field := range []string{"id", "type", "edges", "on_fail", "prompt", "tool_command", "thread_id", "max_parallel", "max_visits", "custom"} {
		document := `{"defaults":{` + quoted(field) + `:1},"nodes":[]}`
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("defaults admitted %q", field)
		}
	}
	if _, err := Parse([]byte(`{"nodes":[{"id":"tool","type":"tool"}]}`)); err == nil {
		t.Fatal("tool_command was not required inline")
	}
	if _, err := Parse([]byte(`{"defaults":{"tool_command":"true"},"nodes":[{"id":"tool","type":"tool"}]}`)); err == nil {
		t.Fatal("defaults satisfied or admitted tool_command")
	}
}

func TestDurationSyntaxAndParsing(t *testing.T) {
	tests := map[string]time.Duration{
		"250ms": 250 * time.Millisecond,
		"900s":  900 * time.Second,
		"15m":   15 * time.Minute,
		"2h":    2 * time.Hour,
		"1d":    24 * time.Hour,
	}
	for value, want := range tests {
		document := `{"nodes":[{"id":"tool","type":"tool","tool_command":"true","timeout":` + quoted(value) + `}]}`
		if _, err := Parse([]byte(document)); err != nil {
			t.Errorf("duration %q rejected: %v", value, err)
		}
		got, err := Duration(value).Parse()
		if err != nil || got != want {
			t.Errorf("Duration(%q).Parse() = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"1.5s", "-1s", "1", "1 second", "1h30m", ""} {
		document := `{"nodes":[{"id":"tool","type":"tool","tool_command":"true","timeout":` + quoted(value) + `}]}`
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("duration %q admitted", value)
		}
		if _, err := Duration(value).Parse(); err == nil {
			t.Errorf("Duration(%q).Parse() succeeded", value)
		}
	}
	if _, err := Parse([]byte(`{"nodes":[{"id":"tool","type":"tool","tool_command":"true","timeout":250}]}`)); err == nil {
		t.Fatal("bare numeric duration admitted")
	}
}

func TestSchemaAndParserAgreeOnStructuralFixtures(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		accepts bool
	}{
		{name: "representative", input: representative, accepts: true},
		{name: "minimal", input: `{"nodes":[]}`, accepts: true},
		{name: "custom", input: `{"nodes":[{"id":"x","type":"my.type","custom":{"value":[1,true,"x"]}}]}`, accepts: true},
		{name: "unknown top", input: `{"nodes":[],"extra":1}`},
		{name: "cross type", input: `{"nodes":[{"id":"x","type":"tool","tool_command":"true","prompt":"no"}]}`},
		{name: "built-in cannot use custom shape", input: `{"nodes":[{"id":"x","type":"exit","custom":{}}]}`},
		{name: "invalid id", input: `{"nodes":[{"id":"bad-id","type":"exit"}]}`},
		{name: "invalid duration", input: `{"nodes":[{"id":"x","type":"tool","tool_command":"true","timeout":"soon"}]}`},
		{name: "nested null", input: `{"nodes":[{"id":"x","type":"my.type","custom":{"value":null}}]}`},
		{name: "missing required", input: `{"nodes":[{"id":"x","type":"tool"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schemaAccepts := (Graph{}).ValidateJSON([]byte(test.input)) == nil
			parserAccepts := func() bool { _, err := Parse([]byte(test.input)); return err == nil }()
			if schemaAccepts != test.accepts || parserAccepts != test.accepts {
				t.Fatalf("schema=%v parser=%v want=%v", schemaAccepts, parserAccepts, test.accepts)
			}
		})
	}
}

func TestGraphSchemaIsCommittedAndClosed(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal((Graph{}).Schema(), &root); err != nil {
		t.Fatal(err)
	}
	if root["additionalProperties"] != false {
		t.Fatal("top-level schema is not closed")
	}
	properties := root["properties"].(map[string]any)
	if got := root["required"]; !reflect.DeepEqual(got, []any{"nodes"}) {
		t.Fatalf("top-level required = %#v", got)
	}
	if properties["defaults"].(map[string]any)["additionalProperties"] != false {
		t.Fatal("defaults schema is not closed")
	}
	options := properties["nodes"].(map[string]any)["items"].(map[string]any)["anyOf"].([]any)
	if len(options) != 7 {
		t.Fatalf("node union has %d cases", len(options))
	}
	for _, raw := range options {
		if raw.(map[string]any)["additionalProperties"] != false {
			t.Fatal("node schema is not closed")
		}
	}
}

func TestGeneratedSchemaIsCurrent(t *testing.T) {
	temporary := t.TempDir()
	files := []string{"graph.go", "schema.go", "internal/schemafix/main.go"}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(temporary, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(temporary, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commands := [][]string{
		{"go", "tool", "gen-jsonschema", "gen", "--pretty", "--validate"},
		{"go", "run", "./internal/schemafix"},
	}
	for _, arguments := range commands {
		command := exec.Command(arguments[0], arguments[1:]...)
		command.Dir = temporary
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
	for _, name := range []string{"jsonschema/Graph.json", "jsonschema/Graph.json.sum", "jsonschema_gen.go"} {
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(temporary, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("generated artifact is stale: %s", name)
		}
	}
}

func mustNode[T Node](t *testing.T, graph *Graph, id string) T {
	t.Helper()
	node, ok := graph.NodeByID(id)
	if !ok {
		t.Fatalf("node %q not found", id)
	}
	result, ok := node.(T)
	if !ok {
		t.Fatalf("node %q is %T", id, node)
	}
	return result
}

func quoted(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
