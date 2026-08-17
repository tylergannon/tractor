package harness

import (
	"fmt"
	"strings"
)

// ValidateCreateSessionInput validates the provider-neutral inputs available
// before a native session is created.
func ValidateCreateSessionInput(model, workdir string) *Error {
	if strings.TrimSpace(model) == "" {
		return terminalError("model must not be empty")
	}
	if strings.TrimSpace(workdir) == "" {
		return terminalError("workdir must not be empty")
	}
	return nil
}

// ValidateRunTurnInput validates a turn before native harness activity begins.
func ValidateRunTurnInput(input RunTurnInput, onEvent OnEvent) *Error {
	if strings.TrimSpace(input.SessionID) == "" {
		return terminalError("session ID must not be empty")
	}
	if err := ValidateCreateSessionInput(input.Model, input.Workdir); err != nil {
		return err
	}
	if strings.TrimSpace(input.ReasoningEffort) == "" {
		return terminalError("reasoning effort must not be empty")
	}
	if err := ValidateContentParts(input.Parts); err != nil {
		return err
	}
	if input.Timeout < 0 {
		return terminalError("timeout must not be negative")
	}
	if onEvent == nil {
		return terminalError("event callback must not be nil")
	}
	_, err := NewResultValidator(input.OutputSchema)
	return err
}

// ValidateSessionInput validates inputs for a session-scoped operation.
func ValidateSessionInput(sessionID, workdir string) *Error {
	if strings.TrimSpace(sessionID) == "" {
		return terminalError("session ID must not be empty")
	}
	if strings.TrimSpace(workdir) == "" {
		return terminalError("workdir must not be empty")
	}
	return nil
}

// ValidateContentParts enforces the non-empty ordered text-part contract.
func ValidateContentParts(parts []ContentPart) *Error {
	if len(parts) == 0 {
		return terminalError("content parts must not be empty")
	}
	for i, part := range parts {
		if part.Type != ContentPartText {
			return terminalError(fmt.Sprintf("content part %d has unsupported type %q", i, part.Type))
		}
	}
	return nil
}

// ValidateCodergenTurn validates a fully resolved backend turn.
func ValidateCodergenTurn(turn CodergenTurn) *Error {
	if strings.TrimSpace(turn.NodeID) == "" {
		return terminalError("node ID must not be empty")
	}
	if strings.TrimSpace(turn.Provider) == "" {
		return terminalError("provider must not be empty")
	}
	if err := ValidateCreateSessionInput(turn.Model, turn.Workdir); err != nil {
		return err
	}
	if strings.TrimSpace(turn.ReasoningEffort) == "" {
		return terminalError("reasoning effort must not be empty")
	}
	if err := ValidateContentParts(turn.Parts); err != nil {
		return err
	}
	if turn.Timeout < 0 {
		return terminalError("timeout must not be negative")
	}
	switch turn.Fidelity {
	case FidelityNone:
		if turn.ThreadKey != "" {
			return terminalError("thread key must be empty for none fidelity")
		}
	case FidelityFull, FidelityCompacted:
		if strings.TrimSpace(turn.ThreadKey) == "" {
			return terminalError("thread key must not be empty for reusable fidelity")
		}
	default:
		return terminalError(fmt.Sprintf("unsupported fidelity %q", turn.Fidelity))
	}
	_, err := NewResultValidator(turn.OutputSchema)
	return err
}

func terminalError(message string) *Error {
	return &Error{Category: ErrorTerminal, Message: message}
}
