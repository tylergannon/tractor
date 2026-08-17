// Package lint validates graph relationships that are not structural JSON
// concerns.
package lint

import (
	"fmt"
	"strings"

	"github.com/tylergannon/tractor/graph"
)

// Severity determines whether a diagnostic prevents execution.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// EdgeRef identifies an authored edge as (from, to).
type EdgeRef [2]string

// Diagnostic describes one lint finding.
type Diagnostic struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	NodeID   string   `json:"node_id,omitempty"`
	Edge     *EdgeRef `json:"edge,omitempty"`
	Fix      string   `json:"fix,omitempty"`
}

// HarnessResolver returns the runtime harness selected for resolved provider
// and model values. It must use the same routing as execution.
type HarnessResolver func(provider, model string) (string, error)

// Options supplies the runtime knowledge required by implementation-dependent
// lint rules. Built-in types and fidelity modes are always known.
type Options struct {
	KnownCustomTypes  []string
	SupportedFidelity []string
	ResolveHarness    HarnessResolver
}

// Rule is an additional caller-provided lint rule.
type Rule interface {
	Name() string
	Apply(graph.Graph) []Diagnostic
}

// Validator runs the built-in rules with one runtime configuration.
type Validator struct {
	options options
}

// New constructs a validator. Empty options use the three specified fidelity
// modes and no registered custom handlers.
func New(opts Options) *Validator {
	return &Validator{options: normalizeOptions(opts)}
}

// Validate runs every built-in rule, followed by extra rules in caller order.
func (v *Validator) Validate(g graph.Graph, extraRules ...Rule) []Diagnostic {
	a := newAnalysis(g, v.options)
	diagnostics := a.builtInDiagnostics()
	for _, rule := range extraRules {
		if rule == nil {
			continue
		}
		for _, diagnostic := range rule.Apply(g) {
			if diagnostic.Rule == "" {
				diagnostic.Rule = rule.Name()
			}
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	return diagnostics
}

// ValidateOrError returns all diagnostics and a ValidationError when any are
// error-severity.
func (v *Validator) ValidateOrError(g graph.Graph, extraRules ...Rule) ([]Diagnostic, error) {
	diagnostics := v.Validate(g, extraRules...)
	if !HasErrors(diagnostics) {
		return diagnostics, nil
	}
	return diagnostics, &ValidationError{Diagnostics: diagnostics}
}

// Validate runs the built-in rules with default options.
func Validate(g graph.Graph, extraRules ...Rule) []Diagnostic {
	return New(Options{}).Validate(g, extraRules...)
}

// ValidateOrError runs the built-in rules with default options.
func ValidateOrError(g graph.Graph, extraRules ...Rule) ([]Diagnostic, error) {
	return New(Options{}).ValidateOrError(g, extraRules...)
}

// HasErrors reports whether diagnostics contain an execution-blocking error.
func HasErrors(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ValidationError contains the complete diagnostic set from a failed
// validation.
type ValidationError struct {
	Diagnostics []Diagnostic
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	messages := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		if diagnostic.Severity == SeverityError {
			messages = append(messages, fmt.Sprintf("%s: %s", diagnostic.Rule, diagnostic.Message))
		}
	}
	return "pipeline validation failed: " + strings.Join(messages, "; ")
}

type options struct {
	knownTypes        map[string]struct{}
	supportedFidelity map[string]struct{}
	resolveHarness    HarnessResolver
}

func normalizeOptions(opts Options) options {
	knownTypes := map[string]struct{}{
		"start": {}, "exit": {}, "codergen": {}, "tool": {},
		"parallel": {}, "parallel.fan_in": {},
	}
	for _, nodeType := range opts.KnownCustomTypes {
		knownTypes[nodeType] = struct{}{}
	}
	supported := map[string]struct{}{
		"full": {}, "compacted": {}, "none": {},
	}
	for _, fidelity := range opts.SupportedFidelity {
		supported[fidelity] = struct{}{}
	}
	return options{
		knownTypes:        knownTypes,
		supportedFidelity: supported,
		resolveHarness:    opts.ResolveHarness,
	}
}

func diagnostic(rule string, severity Severity, message, nodeID string) Diagnostic {
	return Diagnostic{Rule: rule, Severity: severity, Message: message, NodeID: nodeID}
}

func edgeDiagnostic(rule, message, from, to string) Diagnostic {
	edge := EdgeRef{from, to}
	return Diagnostic{
		Rule: rule, Severity: SeverityError, Message: message,
		NodeID: from, Edge: &edge,
	}
}
