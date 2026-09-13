package gimble

import (
	"encoding/json"
	"time"

	"github.com/tylergannon/polytype"
)

// JSONText is JSON source text carried opaquely by an event. Complete events
// contain one encoded value; delta events may contain only the next fragment.
// Text preserves provider-specific payloads while the event remains a closed
// Polytype shape.
type JSONText string

// AgentEvent is one OpenCode session event. Adapters supply Type, Data,
// Metadata, and NativeRef. Gimble assigns event identity and
// timestamps before it records or observes the event.
type AgentEvent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Created   int64           `json:"created,omitempty"`
	Data      json.RawMessage `json:"data"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	NativeRef json.RawMessage `json:"-"`
}

// LifecycleEvent is one typed change to a run's lifecycle. The interface is
// sealed: Gimble produces the concrete variants defined here.
type LifecycleEvent interface{ lifecycleEvent() }

// RunStarted records the beginning of a workflow run.
type RunStarted struct {
	Name string `json:"name"`
}

func (RunStarted) lifecycleEvent() {}

// RunEnded records the result of a workflow run.
type RunEnded struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

func (RunEnded) lifecycleEvent() {}

// RunCancelled records a workflow run stopped by context cancellation.
type RunCancelled struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Error  string `json:"error"`
}

func (RunCancelled) lifecycleEvent() {}

// ScopeBegan records entry into one scope instance.
type ScopeBegan struct {
	Name string                  `json:"name"`
	Task polytype.Optional[Task] `json:"task,omitzero"`
}

func (ScopeBegan) lifecycleEvent() {}

// ScopeEnded records exit from one scope instance.
type ScopeEnded struct {
	Error string `json:"error"`
}

func (ScopeEnded) lifecycleEvent() {}

// PlannerDecision records the task selected for the next dispatch. An absent
// Task records that the planner ended dispatch.
type PlannerDecision struct {
	Task polytype.Optional[Task] `json:"task,omitzero"`
}

func (PlannerDecision) lifecycleEvent() {}

// ValueSet records one value written into a scope.
type ValueSet struct {
	Key   string   `json:"key"`
	Value JSONText `json:"value"`
}

func (ValueSet) lifecycleEvent() {}

// SessionCreated records a new agent session or fork.
type SessionCreated struct {
	Name    string `json:"name"`
	Adapter string `json:"adapter"`
	Model   string `json:"model"`
	Workdir string `json:"workdir"`
	Parent  string `json:"parent"`
}

func (SessionCreated) lifecycleEvent() {}

// SessionClosed records that a scope closed one of its sessions. Error is
// set when the session had a native id and the adapter's Close failed;
// a session that never allocated a native id is always closed cleanly.
type SessionClosed struct {
	Error string `json:"error"`
}

func (SessionClosed) lifecycleEvent() {}

// TurnStarted records the beginning of one agent turn.
type TurnStarted struct {
	Prompt     string `json:"prompt"`
	OutputType string `json:"output_type"`
}

func (TurnStarted) lifecycleEvent() {}

// TurnEnded records the result and accounting for one agent turn. Usage is
// what the turn spent, per model: the harness's own turn report when it
// stated one, and otherwise the turn's step events summed under the
// session's model.
type TurnEnded struct {
	Result      JSONText      `json:"result"`
	Error       string        `json:"error"`
	Usage       []ModelUsage  `json:"usage"`
	Duration    time.Duration `json:"duration"`
	Interrupted bool          `json:"interrupted"`
}

func (TurnEnded) lifecycleEvent() {}

// SuperviseAttached records a reviewer attached to a worker turn.
type SuperviseAttached struct {
	Reviewer    string        `json:"reviewer"`
	Worker      string        `json:"worker"`
	Instruction string        `json:"instruction"`
	Interval    time.Duration `json:"interval"`
}

func (SuperviseAttached) lifecycleEvent() {}

// Steer records a message sent to a running session or dropped after it ended.
type Steer struct {
	Target  string `json:"target"`
	Source  string `json:"source"`
	Message string `json:"message"`
	Landed  bool   `json:"landed"`
}

func (Steer) lifecycleEvent() {}

// Interrupt records a request to stop a running session.
type Interrupt struct {
	Target string `json:"target"`
	Source string `json:"source"`
}

func (Interrupt) lifecycleEvent() {}

// Complete marks the durable end of a run log. RecordingError reports an
// earlier failure in another log owned by the run; an absent Complete means
// the run log itself did not finish durably.
type Complete struct {
	RecordingError string `json:"recording_error"`
}

func (Complete) lifecycleEvent() {}

// LifecycleRecord places one lifecycle event in a run or project log.
type LifecycleRecord struct {
	Seq     uint64                    `json:"seq"`
	Time    time.Time                 `json:"time"`
	Scope   string                    `json:"scope"`
	Session polytype.Optional[string] `json:"session,omitzero"`
	Turn    polytype.Optional[string] `json:"turn,omitzero"`
	Event   LifecycleEvent            `json:"event"`
}

// AgentRecord places one harness event in a session transcript.
type AgentRecord struct {
	Seq       uint64          `json:"seq"`
	Time      time.Time       `json:"time"`
	Scope     string          `json:"scope"`
	Session   string          `json:"session"`
	Turn      string          `json:"turn"`
	Event     AgentEvent      `json:"event"`
	NativeRef json.RawMessage `json:"native_ref,omitempty"`
}
