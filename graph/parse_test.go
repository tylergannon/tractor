package graph

import (
	"encoding/json"
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
  "start":"code",
  "nodes":[
    {"id":"code","type":"codergen","label":"Code","prompt":"work","llm_model":"override","thread_id":"shared","max_visits":3,"edges":[{"to":"tool"}]},
    {"id":"tool","type":"tool","tool_command":"go test ./...","on_success":"parallel","on_error":"code"},
    {"id":"parallel","type":"parallel","max_parallel":3,"branches":["left","right"]},
    {"id":"left","type":"tool","tool_command":"true","on_success":"join"},
    {"id":"right","type":"tool","tool_command":"true","on_success":"join"},
    {"id":"join","type":"parallel.fan_in","prompt":"choose","edges":[{"to":"success"}]},
    {"id":"coach","type":"supervisor","prompt":"keep scope","supervises":["code","join"],"interval":"120s"}
  ]
}`

func TestParseRepresentativeGraphAndResolveDefaults(t *testing.T) {
	pipeline, err := Parse([]byte(representative))
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.Start != "code" || pipeline.Name != "all_fields" || len(pipeline.Nodes) != 7 {
		t.Fatalf("graph = %#v", pipeline)
	}
	code := mustNode[*CodergenNode](t, pipeline, "code")
	if code.DisplayLabel() != "Code" || code.LLMModel.Value != "override" || code.ThreadKey(code.ID) != "shared" {
		t.Fatalf("codergen = %#v", code)
	}
	if code.MaxRetries.Value != 2 || code.FidelityValue() != "full" || code.Timeout.Value != "15m" {
		t.Fatalf("codergen defaults = %#v", code.LLMNodeFields)
	}
	tool := mustNode[*ToolNode](t, pipeline, "tool")
	if tool.OnSuccess != "parallel" || !tool.OnError.Present || tool.OnError.Value != "code" || tool.Timeout.Value != "15m" {
		t.Fatalf("tool = %#v", tool)
	}
	parallel := mustNode[*ParallelNode](t, pipeline, "parallel")
	if parallel.MaxParallelValue() != 3 || !reflect.DeepEqual(parallel.Branches, []string{"left", "right"}) {
		t.Fatalf("parallel = %#v", parallel)
	}
	join := mustNode[*FanInNode](t, pipeline, "join")
	if join.LLMModel.Value != "default-model" || join.LLMProvider.Value != "openai" || join.ReasoningEffort.Value != "high" {
		t.Fatalf("fan-in defaults = %#v", join.LLMNodeFields)
	}
	coach := mustNode[*SupervisorNode](t, pipeline, "coach")
	if coach.IntervalValue() != "120s" || coach.Timeout.Value != "15m" || coach.LLMProvider.Value != "openai" {
		t.Fatalf("supervisor defaults = %#v", coach)
	}
}

func TestParseAcceptsEveryNodeShape(t *testing.T) {
	tests := []string{
		`{"id":"c","type":"codergen","prompt":"p","max_retries":0,"fidelity":"none","thread_id":"t","timeout":"250ms","llm_model":"m","llm_provider":"p","reasoning_effort":"low","edges":[{"to":"success"}]}`,
		`{"id":"p","type":"parallel","branches":["c"],"max_parallel":4}`,
		`{"id":"f","type":"parallel.fan_in","prompt":"p","edges":[{"to":"success"}]}`,
		`{"id":"t","type":"tool","tool_command":"true","on_success":"success","on_error":"failure","timeout":"2h"}`,
		`{"id":"s","type":"supervisor","prompt":"watch","supervises":["c"]}`,
	}
	for _, node := range tests {
		document := `{"start":"c","nodes":[` + node + `]}`
		if _, err := Parse([]byte(document)); err != nil {
			t.Errorf("Parse(%s): %v", node, err)
		}
	}
}

func TestParsePreservesOptionalPresence(t *testing.T) {
	document := `{"start":"omitted","nodes":[
    {"id":"omitted","type":"codergen"},
    {"id":"empty","type":"codergen","label":"","prompt":""}
  ]}`
	pipeline, err := Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	omitted := mustNode[*CodergenNode](t, pipeline, "omitted")
	if omitted.Label.Present || omitted.Prompt.Present || omitted.DisplayLabel() != "omitted" {
		t.Fatalf("omitted = %#v", omitted)
	}
	empty := mustNode[*CodergenNode](t, pipeline, "empty")
	if !empty.Label.Present || !empty.Prompt.Present || empty.DisplayLabel() != "" {
		t.Fatalf("empty = %#v", empty)
	}
}

func TestParseRejectsStructuralViolations(t *testing.T) {
	tests := map[string]string{
		"missing start":             `{"nodes":[]}`,
		"unknown type":              `{"start":"x","nodes":[{"id":"x","type":"notify.slack"}]}`,
		"cross type field":          `{"start":"x","nodes":[{"id":"x","type":"tool","tool_command":"true","on_success":"success","prompt":"no"}]}`,
		"missing tool command":      `{"start":"x","nodes":[{"id":"x","type":"tool","on_success":"success"}]}`,
		"missing tool route":        `{"start":"x","nodes":[{"id":"x","type":"tool","tool_command":"true"}]}`,
		"missing branches":          `{"start":"x","nodes":[{"id":"x","type":"parallel"}]}`,
		"missing supervisor prompt": `{"start":"x","nodes":[{"id":"x","type":"supervisor","supervises":["x"]}]}`,
		"missing supervises":        `{"start":"x","nodes":[{"id":"x","type":"supervisor","prompt":"watch"}]}`,
		"unknown top field":         `{"start":"x","nodes":[],"extra":1}`,
		"unknown defaults":          `{"start":"x","defaults":{"max_visits":1},"nodes":[]}`,
		"null":                      `{"start":"x","name":null,"nodes":[]}`,
		"wrong type":                `{"start":1,"nodes":[]}`,
		"trailing value":            `{"start":"x","nodes":[]} {}`,
		"comment":                   `{"start":"x","nodes":[]} // no`,
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(document)); err == nil {
				t.Fatal("invalid pipeline admitted")
			}
		})
	}
}

func TestParseRejectsDuplicateMembersAndNodeIDs(t *testing.T) {
	for name, document := range map[string]string{
		"top":  `{"start":"x","start":"y","nodes":[]}`,
		"node": `{"start":"a","nodes":[{"id":"a","id":"b","type":"codergen"}]}`,
		"edge": `{"start":"a","nodes":[{"id":"a","type":"codergen","edges":[{"to":"success","to":"failure"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(document))
			if err == nil || !strings.Contains(err.Error(), "duplicate object member") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	duplicate := `{"start":"same","nodes":[{"id":"same","type":"codergen"},{"id":"same","type":"codergen"}]}`
	if _, err := Parse([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate node ID") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestParseRejectsInvalidAndReservedNodeIDs(t *testing.T) {
	for _, id := range []string{"", "1bad", "bad-name", "bad name", Success, Failure} {
		document := `{"start":"x","nodes":[{"id":` + quoted(id) + `,"type":"codergen"}]}`
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("invalid ID %q admitted", id)
		}
	}
}

func TestParseYAMLUsesSameContract(t *testing.T) {
	document := `
name: yaml
defaults:
  timeout: 2m
start: work
nodes:
  # Keep commands readable.
  - id: work
    type: tool
    label: Run checks
    tool_command: |
      printf '%s\n' first
      printf '%s\n' second
    on_success: success
  - id: coach
    type: supervisor
    prompt: Watch the command
    supervises: [work]
`
	pipeline, err := ParseYAML([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	tool := mustNode[*ToolNode](t, pipeline, "work")
	if tool.ToolCommand != "printf '%s\\n' first\nprintf '%s\\n' second\n" || tool.Timeout.Value != "2m" {
		t.Fatalf("tool = %#v", tool)
	}
	if mustNode[*SupervisorNode](t, pipeline, "coach").IntervalValue() != "60s" {
		t.Fatal("supervisor interval default missing")
	}
	for name, invalid := range map[string]string{
		"duplicate": "start: a\nstart: b\nnodes: []\n",
		"unknown":   "start: a\nunknown: true\nnodes: []\n",
		"null":      "start: a\nname: null\nnodes: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseYAML([]byte(invalid)); err == nil {
				t.Fatal("invalid YAML admitted")
			}
		})
	}
}

func TestDurationSyntaxAndParsing(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"250ms": 250 * time.Millisecond,
		"900s":  900 * time.Second,
		"15m":   15 * time.Minute,
		"2h":    2 * time.Hour,
		"1d":    24 * time.Hour,
	} {
		document := `{"start":"tool","nodes":[{"id":"tool","type":"tool","tool_command":"true","on_success":"success","timeout":` + quoted(value) + `}]}`
		if _, err := Parse([]byte(document)); err != nil {
			t.Errorf("duration %q rejected: %v", value, err)
		}
		got, err := Duration(value).Parse()
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"1.5s", "-1s", "1", "1 second", "1h30m", ""} {
		document := `{"start":"tool","nodes":[{"id":"tool","type":"tool","tool_command":"true","on_success":"success","timeout":` + quoted(value) + `}]}`
		if _, err := Parse([]byte(document)); err == nil {
			t.Errorf("duration %q admitted", value)
		}
	}
}

func TestGraphSchemaIsCommittedAndClosed(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal((Graph{}).Schema(), &root); err != nil {
		t.Fatal(err)
	}
	if root["additionalProperties"] != false || !reflect.DeepEqual(root["required"], []any{"start", "nodes"}) {
		t.Fatalf("top-level schema = %#v", root)
	}
	properties := root["properties"].(map[string]any)
	options := properties["nodes"].(map[string]any)["items"].(map[string]any)["anyOf"].([]any)
	if len(options) != 5 {
		t.Fatalf("node union has %d cases", len(options))
	}
	for _, raw := range options {
		if raw.(map[string]any)["additionalProperties"] != false {
			t.Fatal("node schema is not closed")
		}
	}
}

func mustNode[T Node](t *testing.T, graph *Graph, id string) T {
	t.Helper()
	node, exists := graph.NodeByID(id)
	if !exists {
		t.Fatalf("node %q missing", id)
	}
	typed, ok := node.(T)
	if !ok {
		t.Fatalf("node %q has type %T", id, node)
	}
	return typed
}

func quoted(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
