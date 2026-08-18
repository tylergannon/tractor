package agy

import (
	"fmt"
	"strings"
	"sync"

	"github.com/tylergannon/tractor/harness"
	"github.com/tylergannon/tractor/harness/agy/schema"
)

type eventProjector struct {
	emit  harness.OnEvent
	mu    sync.Mutex
	steps map[int]*projectedStep
}

type projectedStep struct {
	text       strings.Builder
	assistant  bool
	toolCall   bool
	toolResult bool
}

func newEventProjector(emit harness.OnEvent) *eventProjector {
	return &eventProjector{emit: emit, steps: make(map[int]*projectedStep)}
}

func (p *eventProjector) user(parts []harness.ContentPart) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emit(harness.Event{"type": harness.EventUser, "parts": append([]harness.ContentPart(nil), parts...)})
}

func (p *eventProjector) envelope(envelope schema.Envelope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if envelope.StepUpdate == nil {
		if envelope.Result != nil && envelope.Result.Usage != nil {
			p.emit(harness.Event{"type": harness.EventUsage, "usage": envelope.Result.Usage})
		}
		return
	}
	update := envelope.StepUpdate
	index := 0
	if update.StepIndex != nil {
		index = *update.StepIndex
	}
	step := p.steps[index]
	if step == nil {
		step = &projectedStep{}
		p.steps[index] = step
	}
	typeName, state := stringValue(update.StepType), stringValue(update.State)
	switch typeName {
	case "agent_response":
		if update.TextDelta != nil {
			step.text.WriteString(*update.TextDelta)
		}
		if state == "DONE" && !step.assistant {
			step.assistant = true
			if text := step.text.String(); strings.TrimSpace(text) != "" {
				p.emit(harness.Event{"type": harness.EventAssistant, "text": text})
			}
		}
		if state == "DONE" && update.Usage != nil {
			p.emit(harness.Event{"type": harness.EventUsage, "usage": update.Usage})
		}
	case "tool":
		info := update.ToolInfo
		if info == nil {
			return
		}
		callID := fmt.Sprintf("step-%d", index)
		if !step.toolCall && info.Name != nil {
			step.toolCall = true
			p.emit(harness.Event{"type": harness.EventToolCall, "call_id": callID, "name": *info.Name, "args": info.Parameters})
		}
		if state == "DONE" && !step.toolResult {
			step.toolResult = true
			output := info.Output
			if info.Error != nil {
				output = map[string]any{"output": info.Output, "error": info.Error}
			}
			p.emit(harness.Event{"type": harness.EventToolResult, "call_id": callID, "output": output})
		}
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
