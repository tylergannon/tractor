package claude

import (
	"encoding/json"
	"strings"
	"sync"

	claudeagent "github.com/roasbeef/claude-agent-sdk-go"
	"github.com/tylergannon/tractor/harness"
)

type eventProjector struct {
	emit         harness.OnEvent
	mu           sync.Mutex
	pendingTools []string
}

func newEventProjector(emit harness.OnEvent) *eventProjector {
	return &eventProjector{emit: emit}
}

func (p *eventProjector) user(parts []harness.ContentPart) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emit(harness.Event{"type": harness.EventUser, "parts": append([]harness.ContentPart(nil), parts...)})
}

func (p *eventProjector) message(message claudeagent.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch native := message.(type) {
	case claudeagent.AssistantMessage:
		p.assistant(native)
	case *claudeagent.AssistantMessage:
		p.assistant(*native)
	case claudeagent.UserMessage:
		p.toolResult(native)
	case *claudeagent.UserMessage:
		p.toolResult(*native)
	case claudeagent.ResultMessage:
		p.resultUsage(native)
	case *claudeagent.ResultMessage:
		p.resultUsage(*native)
	}
}

func (p *eventProjector) assistant(message claudeagent.AssistantMessage) {
	texts := make([]string, 0)
	for _, block := range message.Message.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, block.Text)
		}
	}
	emittedText := false
	for _, block := range message.Message.Content {
		switch block.Type {
		case "text":
			if !emittedText && len(texts) > 0 {
				p.emit(harness.Event{"type": harness.EventAssistant, "text": strings.Join(texts, "")})
				emittedText = true
			}
		case "thinking":
			if strings.TrimSpace(block.Text) != "" {
				p.emit(harness.Event{"type": harness.EventThinking, "text": block.Text})
			}
		case "tool_use":
			var args any
			if len(block.Input) > 0 && json.Unmarshal(block.Input, &args) != nil {
				args = string(block.Input)
			}
			p.emit(harness.Event{
				"type":    harness.EventToolCall,
				"call_id": block.ID,
				"name":    block.Name,
				"args":    args,
			})
			p.pendingTools = append(p.pendingTools, block.ID)
		}
	}
	if message.Usage != nil {
		p.emit(harness.Event{"type": harness.EventUsage, "usage": normalizeNative(message.Usage)})
	}
}

func (p *eventProjector) toolResult(message claudeagent.UserMessage) {
	if message.ToolUseResult == nil {
		return
	}
	callID := ""
	if message.ParentToolUseID != nil {
		callID = *message.ParentToolUseID
	} else if len(p.pendingTools) > 0 {
		callID = p.pendingTools[0]
		p.pendingTools = p.pendingTools[1:]
	}
	if callID == "" {
		return
	}
	p.emit(harness.Event{
		"type":    harness.EventToolResult,
		"call_id": callID,
		"output":  normalizeNative(message.ToolUseResult),
	})
}

func (p *eventProjector) resultUsage(message claudeagent.ResultMessage) {
	if message.Usage == nil {
		return
	}
	p.emit(harness.Event{"type": harness.EventUsage, "usage": normalizeNative(message.Usage)})
}
