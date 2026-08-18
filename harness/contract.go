// Package harness defines the provider-neutral contracts shared by coding
// agent harness adapters and their callers.
package harness

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ErrorCategory describes how the orchestration engine handles an operational
// failure.
type ErrorCategory string

const (
	ErrorRetryable   ErrorCategory = "retryable"
	ErrorTerminal    ErrorCategory = "terminal"
	ErrorInterrupted ErrorCategory = "interrupted"
)

// Valid reports whether c is one of the public error categories.
func (c ErrorCategory) Valid() bool {
	switch c {
	case ErrorRetryable, ErrorTerminal, ErrorInterrupted:
		return true
	default:
		return false
	}
}

// Error is an operational harness failure.
type Error struct {
	Category ErrorCategory `json:"category"`
	Message  string        `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Category, e.Message)
}

// Valid reports whether the error is safe to expose through the public
// contract.
func (e *Error) Valid() bool {
	return e != nil && e.Category.Valid() && strings.TrimSpace(e.Message) != ""
}

// ContentPart is one ordered part of a user message. This version of the
// contract supports the mandatory text part defined by the specification.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

const ContentPartText = "text"

// FidelityMode controls whether a logical thread is reused.
type FidelityMode string

const (
	FidelityNone      FidelityMode = "none"
	FidelityFull      FidelityMode = "full"
	FidelityCompacted FidelityMode = "compacted"
)

// CodergenTurn is the fully resolved input to a CodergenBackend.
type CodergenTurn struct {
	NodeID          string
	Parts           []ContentPart
	OutputSchema    json.RawMessage
	Model           string
	Provider        string
	ReasoningEffort string
	Fidelity        FidelityMode
	ThreadKey       string
	Workdir         string
	RunLog          string
	Timeout         time.Duration
}

// SupervisorTurn is one advisory flush on a supervisor-owned thread.
type SupervisorTurn struct {
	NodeID          string
	Parts           []ContentPart
	OutputSchema    json.RawMessage
	Model           string
	Provider        string
	ReasoningEffort string
	Workdir         string
	RunLog          string
	Timeout         time.Duration
}

// Outcome is the semantic result of a codergen turn.
type Outcome struct {
	Next  string `json:"next,omitempty"`
	Notes string `json:"notes"`
}

// Verdict is the semantic result of an advisory supervisor turn.
type Verdict struct {
	Verdict string `json:"verdict"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message,omitempty"`
}

// SteerStatus reports whether a backend accepted a steering handoff.
type SteerStatus string

const (
	SteerAccepted        SteerStatus = "accepted"
	SteerNotActive       SteerStatus = "not_active"
	SteerAmbiguousTarget SteerStatus = "ambiguous_target"
)

// ThreadBinding identifies the native harness session bound to a logical
// thread.
type ThreadBinding struct {
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
	Workdir   string `json:"workdir"`
}

// BindingOpened is called after a new binding is published and before its
// first turn is dispatched. Returning an Error prevents dispatch.
type BindingOpened func(threadKey string, binding ThreadBinding) *Error

// CodergenBackend executes resolved codergen turns.
type CodergenBackend interface {
	Run(CodergenTurn) (Outcome, *Error)
	RunSupervisor(SupervisorTurn) (Verdict, *Error)
	Steer([]ContentPart) SteerStatus
	InterruptAll()
	Bindings() map[string]ThreadBinding
	SetBindingOpened(BindingOpened)
}

// RunTurnInput is the fully resolved input to one harness turn.
type RunTurnInput struct {
	SessionID       string
	Model           string
	ReasoningEffort string
	OutputSchema    json.RawMessage
	Workdir         string
	Parts           []ContentPart
	Timeout         time.Duration
}

// Result is a harness response that has passed the caller's exact schema.
type Result map[string]any

// Event is one complete, provider-neutral logical item emitted during a turn.
// The event type set is open, so the representation preserves unknown fields.
type Event map[string]any

// OnEvent receives complete events in turn order.
type OnEvent func(Event)

const (
	EventUser       = "user"
	EventAssistant  = "assistant"
	EventThinking   = "thinking"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventUsage      = "usage"
)

// HarnessAdapter translates the neutral contract into one native coding-agent
// harness.
type HarnessAdapter interface {
	CreateSession(model, workdir string) (string, *Error)
	RunTurn(RunTurnInput, OnEvent) (Result, *Error)
	Steer(sessionID string, parts []ContentPart)
	Interrupt(sessionID string)
	Compact(sessionID, workdir string) *Error
}
