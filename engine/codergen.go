package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	jsonschema "github.com/tylergannon/go-gen-jsonschema"
	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

// CodergenConfig supplies the backend and implementation-level model defaults.
type CodergenConfig struct {
	Backend                harness.CodergenBackend
	DefaultModel           string
	DefaultProvider        string
	DefaultReasoningEffort string
}

// CodergenHandler executes codergen nodes through a CodergenBackend.
type CodergenHandler struct {
	config CodergenConfig
}

// NewCodergenHandler constructs a codergen handler. A nil backend enables simulation.
func NewCodergenHandler(config CodergenConfig) *CodergenHandler {
	return &CodergenHandler{config: config}
}

// Execute renders and executes one codergen turn.
func (h *CodergenHandler) Execute(node graph.Node, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph) (harness.Outcome, *harness.Error) {
	codergen, ok := node.(*graph.CodergenNode)
	if !ok {
		return harness.Outcome{}, terminalError(fmt.Sprintf("codergen handler cannot execute node type %s", node.NodeType()))
	}

	prompt := codergen.PromptValue(codergen.DisplayLabel())
	prompt = strings.ReplaceAll(prompt, "$goal", scope.Goal)
	return h.executeTurn(codergen, &codergen.LLMNodeFields, offered, scope, pipeline, prompt)
}

func (h *CodergenHandler) executeTurn(node graph.Node, fields *graph.LLMNodeFields, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph, prompt string) (harness.Outcome, *harness.Error) {
	if err := os.WriteFile(filepath.Join(scope.StageDir, "prompt.md"), []byte(prompt), 0o644); err != nil {
		return harness.Outcome{}, terminalError(fmt.Sprintf("write prompt: %v", err))
	}

	turn, err := h.turn(node, fields, offered, scope, pipeline, prompt)
	if err != nil {
		return harness.Outcome{}, err
	}
	if validationErr := harness.ValidateCodergenTurn(turn); validationErr != nil {
		return harness.Outcome{}, validationErr
	}
	var outcome harness.Outcome
	if h.config.Backend == nil {
		outcome = harness.Outcome{Notes: "[Simulated] Stage completed: " + node.Base().ID}
		if len(offered) > 1 {
			outcome.Next = offered[0].To
		}
	} else {
		var runErr *harness.Error
		outcome, runErr = h.config.Backend.Run(turn)
		if runErr != nil {
			return harness.Outcome{}, runErr
		}
	}

	if err := writeResponse(filepath.Join(scope.StageDir, "response.md"), outcome); err != nil {
		return harness.Outcome{}, terminalError(fmt.Sprintf("write response: %v", err))
	}
	return outcome, nil
}

func (h *CodergenHandler) turn(node graph.Node, fields *graph.LLMNodeFields, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph, prompt string) (harness.CodergenTurn, *harness.Error) {
	model := resolveString(fields.LLMModel, pipeline.Defaults.LLMModel, h.config.DefaultModel)
	provider := resolveProvider(fields.LLMProvider, pipeline.Defaults.LLMProvider, h.config.DefaultProvider, model)
	reasoningDefault := h.config.DefaultReasoningEffort
	if reasoningDefault == "" {
		reasoningDefault = "high"
	}
	reasoning := resolveString(fields.ReasoningEffort, pipeline.Defaults.ReasoningEffort, reasoningDefault)
	fidelity := resolveString(fields.Fidelity, pipeline.Defaults.Fidelity, string(harness.FidelityCompacted))
	threadKey := ""
	if fidelity != string(harness.FidelityNone) {
		threadKey = fields.ThreadKey(node.Base().ID)
	}
	timeout, timeoutErr := resolveTimeout(fields.Timeout, pipeline.Defaults.Timeout)
	if timeoutErr != nil {
		return harness.CodergenTurn{}, terminalError(timeoutErr.Error())
	}
	schema, schemaErr := choiceSchema(offered, pipeline)
	if schemaErr != nil {
		return harness.CodergenTurn{}, terminalError(schemaErr.Error())
	}
	return harness.CodergenTurn{
		NodeID:          node.Base().ID,
		Parts:           []harness.ContentPart{{Type: harness.ContentPartText, Text: prompt}},
		OutputSchema:    schema,
		Model:           model,
		Provider:        provider,
		ReasoningEffort: reasoning,
		Fidelity:        harness.FidelityMode(fidelity),
		ThreadKey:       threadKey,
		Workdir:         scope.Workdir,
		Timeout:         timeout,
	}, nil
}

func resolveString(nodeValue, fileValue jsonschema.Optional[string], systemValue string) string {
	if nodeValue.Present {
		return nodeValue.Value
	}
	if fileValue.Present {
		return fileValue.Value
	}
	return systemValue
}

func resolveProvider(nodeValue, fileValue jsonschema.Optional[string], systemValue, model string) string {
	if nodeValue.Present {
		return nodeValue.Value
	}
	if fileValue.Present {
		return fileValue.Value
	}
	if systemValue != "" {
		return systemValue
	}
	return DetectProvider(model)
}

func resolveTimeout(nodeValue, fileValue jsonschema.Optional[graph.Duration]) (time.Duration, error) {
	if nodeValue.Present {
		return nodeValue.Value.Parse()
	}
	if fileValue.Present {
		return fileValue.Value.Parse()
	}
	return 0, nil
}

// DetectProvider returns the provider implied by a known provider-native model name.
func DetectProvider(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "claude"):
		return "anthropic"
	case strings.HasPrefix(lower, "gpt-"), strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"), strings.HasPrefix(lower, "o4"), strings.Contains(lower, "codex"):
		return "openai"
	case strings.HasPrefix(lower, "gemini"):
		return "gemini"
	default:
		return ""
	}
}

type outputSchema struct {
	Type                 string           `json:"type"`
	Properties           schemaProperties `json:"properties"`
	Required             []string         `json:"required"`
	AdditionalProperties bool             `json:"additionalProperties"`
}

type schemaProperties struct {
	Next  *schemaProperty `json:"next,omitempty"`
	Notes schemaProperty  `json:"notes"`
}

type schemaProperty struct {
	Type        string   `json:"type"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description"`
}

func choiceSchema(offered []graph.Edge, pipeline *graph.Graph) (json.RawMessage, error) {
	properties := schemaProperties{
		Notes: schemaProperty{Type: "string", Description: "Your account of this stage."},
	}
	required := []string{"notes"}
	if len(offered) > 1 {
		targets := make([]string, len(offered))
		for index, edge := range offered {
			targets[index] = edge.To
		}
		properties.Next = &schemaProperty{
			Type:        "string",
			Enum:        targets,
			Description: describeRoutes(offered, pipeline),
		}
		required = []string{"next", "notes"}
	}
	encoded, err := json.Marshal(outputSchema{
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: false,
	})
	if err != nil {
		return nil, fmt.Errorf("encode choice schema: %w", err)
	}
	return encoded, nil
}

func describeRoutes(offered []graph.Edge, pipeline *graph.Graph) string {
	clauses := make([]string, len(offered))
	for index, edge := range offered {
		description := strings.TrimSpace(edge.Condition)
		if description == "" {
			if target, ok := pipeline.NodeByID(edge.To); ok {
				description = strings.TrimSpace(target.Base().DisplayLabel())
			}
			if description == "" {
				description = edge.To
			}
		}
		clauses[index] = description + ": " + edge.To
	}
	return "Choose the next stage. " + strings.Join(clauses, "; ")
}

func writeResponse(path string, outcome harness.Outcome) error {
	var response strings.Builder
	response.WriteString("---\n")
	if outcome.Next != "" {
		response.WriteString("next: ")
		response.WriteString(outcome.Next)
		response.WriteByte('\n')
	}
	response.WriteString("---\n")
	response.WriteString(outcome.Notes)
	return os.WriteFile(path, []byte(response.String()), 0o644)
}
