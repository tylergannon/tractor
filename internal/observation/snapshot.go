package observation

import (
	"encoding/json"

	"github.com/tylergannon/gimble/internal/sessionstate"
)

// Run statuses. Only a lifecycle record sets one: a native part, step or
// execution event never decides that a workflow finished.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Scope statuses. An ended scope with no error is "ended", never
// "succeeded": a range-body error in a Loop leaves the task and loop scopes
// ended with an empty error while the run's own error is set.
const (
	StatusEnded = "ended"
)

// Placement is where a native event happened in the workflow. It lives
// outside the native envelope, so native IDs stay unchanged inside it and
// two concurrent invocations never share a projection.
type Placement struct {
	Scope   string `json:"scope"`
	Session string `json:"session"`
	Turn    string `json:"turn"`
}

// SessionInfo is one agent conversation's runtime metadata, taken from the
// run's own lifecycle records.
type SessionInfo struct {
	Name    string `json:"name"`
	Adapter string `json:"adapter"`
	Model   string `json:"model"`
	Scope   string `json:"scope"`
	Parent  string `json:"parent,omitempty"`
}

// RunInfo is the run, its sessions, and each session's usage so far.
type RunInfo struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Status   string                 `json:"status"`
	Error    string                 `json:"error,omitempty"`
	Sessions map[string]SessionInfo `json:"sessions"`
	// Usage is each session's latest running total, keyed the same way
	// Sessions is. It rides in the snapshot so a finished run answers for
	// its usage from its checkpoint, exactly as a live one does.
	Usage map[string]Usage `json:"usage"`
}

// Invocation is one turn's placement, its complete session projection
// snapshot, and the current native provenance of each of its messages.
type Invocation struct {
	Scope      string                     `json:"scope"`
	Session    string                     `json:"session"`
	Turn       string                     `json:"turn"`
	Snapshot   sessionstate.Snapshot      `json:"snapshot"`
	Provenance map[string]json.RawMessage `json:"provenance"`
}

// ScopeInfo is one scope instance's workflow state: how it ended, the task
// it was dispatched with, the values it recorded, and the planner decisions
// taken in it. The parent is the key's path, so it is not repeated here.
type ScopeInfo struct {
	Name      string                     `json:"name"`
	Status    string                     `json:"status"`
	Error     string                     `json:"error,omitempty"`
	Task      json.RawMessage            `json:"task,omitempty"`
	Values    map[string]json.RawMessage `json:"values,omitempty"`
	Decisions []json.RawMessage          `json:"decisions,omitempty"`
}

// RunSnapshot is the complete public observation of one run: everything a
// consumer needs to render it and to continue reducing its events. It is
// the body of GET /api/runs/:runID, the first SSE frame, and the SSR load's
// `snapshot` property.
type RunSnapshot struct {
	Run         RunInfo               `json:"run"`
	Scopes      map[string]ScopeInfo  `json:"scopes"`
	Invocations map[string]Invocation `json:"invocations"`
}
