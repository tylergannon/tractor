package harness

import (
	"encoding/json"
	"testing"
	"time"
)

func TestErrorCategories(t *testing.T) {
	for _, category := range []ErrorCategory{ErrorRetryable, ErrorTerminal, ErrorInterrupted} {
		if !category.Valid() {
			t.Errorf("category %q is not valid", category)
		}
		err := &Error{Category: category, Message: "message"}
		if !err.Valid() {
			t.Errorf("error %#v is not valid", err)
		}
	}
	if (ErrorCategory("unknown")).Valid() {
		t.Error("unknown category is valid")
	}
	if (&Error{Category: ErrorTerminal}).Valid() {
		t.Error("empty error message is valid")
	}
}

func TestValidateRunTurnInput(t *testing.T) {
	valid := RunTurnInput{
		SessionID:       "session",
		Model:           "model",
		ReasoningEffort: "medium",
		OutputSchema:    json.RawMessage(`{"type":"object"}`),
		Workdir:         t.TempDir(),
		Parts:           []ContentPart{{Type: ContentPartText, Text: "do work"}},
		Timeout:         time.Second,
	}
	if err := ValidateRunTurnInput(valid, func(Event) {}); err != nil {
		t.Fatalf("ValidateRunTurnInput() error = %v", err)
	}

	tests := map[string]func(*RunTurnInput){
		"empty session":    func(input *RunTurnInput) { input.SessionID = "" },
		"empty model":      func(input *RunTurnInput) { input.Model = "" },
		"empty effort":     func(input *RunTurnInput) { input.ReasoningEffort = "" },
		"empty workdir":    func(input *RunTurnInput) { input.Workdir = "" },
		"empty parts":      func(input *RunTurnInput) { input.Parts = nil },
		"unsupported part": func(input *RunTurnInput) { input.Parts[0].Type = "image" },
		"negative timeout": func(input *RunTurnInput) { input.Timeout = -time.Second },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.Parts = append([]ContentPart(nil), valid.Parts...)
			mutate(&input)
			assertTerminalError(t, ValidateRunTurnInput(input, func(Event) {}))
		})
	}
	t.Run("nil event callback", func(t *testing.T) {
		assertTerminalError(t, ValidateRunTurnInput(valid, nil))
	})
}

func TestValidateCodergenTurnFidelity(t *testing.T) {
	turn := CodergenTurn{
		NodeID:          "implement",
		Parts:           []ContentPart{{Type: ContentPartText, Text: "do work"}},
		OutputSchema:    json.RawMessage(`{"type":"object"}`),
		Model:           "model",
		Provider:        "provider",
		ReasoningEffort: "medium",
		Fidelity:        FidelityFull,
		ThreadKey:       "implement",
		Workdir:         t.TempDir(),
		RunLog:          "run.jsonl",
	}
	if err := ValidateCodergenTurn(turn); err != nil {
		t.Fatalf("ValidateCodergenTurn() error = %v", err)
	}

	turn.Fidelity = FidelityNone
	assertTerminalError(t, ValidateCodergenTurn(turn))
	turn.ThreadKey = ""
	if err := ValidateCodergenTurn(turn); err != nil {
		t.Fatalf("ValidateCodergenTurn() none fidelity error = %v", err)
	}
	turn.Fidelity = FidelityFull
	assertTerminalError(t, ValidateCodergenTurn(turn))
	turn.Fidelity = FidelityMode("unknown")
	assertTerminalError(t, ValidateCodergenTurn(turn))
}

func TestValidateSupervisorTurn(t *testing.T) {
	turn := SupervisorTurn{
		NodeID:          "coach",
		Parts:           []ContentPart{{Type: ContentPartText, Text: "review"}},
		OutputSchema:    json.RawMessage(`{"type":"object"}`),
		Model:           "model",
		Provider:        "provider",
		ReasoningEffort: "medium",
		Workdir:         t.TempDir(),
		RunLog:          "run.jsonl",
	}
	if err := ValidateSupervisorTurn(turn); err != nil {
		t.Fatalf("ValidateSupervisorTurn() error = %v", err)
	}
	turn.RunLog = ""
	assertTerminalError(t, ValidateSupervisorTurn(turn))
}
