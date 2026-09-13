package observation

import (
	"encoding/json"
	"strings"
)

// usageUpdated is the native event that carries a session's running total.
// It is set semantics: the latest one is the session's usage so far, and
// nothing here adds an event to what came before it.
const usageUpdated = "session.usage.updated"

// Cache is the two halves of a prompt cache: what was served from it and
// what was written into it.
type Cache struct {
	Read  float64 `json:"read"`
	Write float64 `json:"write"`
}

// Tokens is the five token counts every adapter reports, with one meaning
// each. A count the provider does not report is 0: zero is a number, and
// nothing here says whether it was measured or absent.
type Tokens struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     Cache   `json:"cache"`
}

// Usage is cost in USD and the five token counts. It is the shape the
// native `session.usage.updated` event carries, and the shape the page
// reads. It is declared here rather than in gimble because gimble imports
// this package.
type Usage struct {
	Cost   float64 `json:"cost"`
	Tokens Tokens  `json:"tokens"`
}

// add is the sum of two usages, field by field.
func (u Usage) add(other Usage) Usage {
	u.Cost += other.Cost
	u.Tokens.Input += other.Tokens.Input
	u.Tokens.Output += other.Tokens.Output
	u.Tokens.Reasoning += other.Tokens.Reasoning
	u.Tokens.Cache.Read += other.Tokens.Cache.Read
	u.Tokens.Cache.Write += other.Tokens.Cache.Write
	return u
}

// foldUsageLocked remembers one session's latest running total. The caller
// holds the reduction lock and has already read the event's type.
func (s *Store) foldUsageLocked(at Placement, envelope json.RawMessage) {
	var frame struct {
		Data Usage `json:"data"`
	}
	if err := json.Unmarshal(envelope, &frame); err != nil {
		return
	}
	if s.run.Usage == nil {
		s.run.Usage = map[string]Usage{}
	}
	s.run.Usage[at.Session] = frame.Data
}

// ScopeUsage is the usage of one scope: the sum of the latest running total
// of every session in that scope or nested under it. The root scope "" is
// the whole run.
func (r RunInfo) ScopeUsage(scope string) Usage {
	var total Usage
	for session, usage := range r.Usage {
		if inScope(scope, r.Sessions[session].Scope) {
			total = total.add(usage)
		}
	}
	return total
}

// ScopeUsage answers the same question of a run that is still going.
func (s *Store) ScopeUsage(scope string) Usage {
	if s == nil {
		return Usage{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run.ScopeUsage(scope)
}

// inScope reports whether a session's scope is within the asked-for scope.
// Scope keys are slash-joined paths -- lap.3/bakeoff.1/attempt.2 -- so a
// nested scope is a path below it, and the root matches every session.
func inScope(scope, session string) bool {
	return scope == "" || session == scope || strings.HasPrefix(session, scope+"/")
}
