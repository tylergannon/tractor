package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/tylergannon/tractor/harness"
)

type eventProjector struct {
	emit        harness.OnEvent
	startedTool map[string]bool
	mu          sync.Mutex
}

func newEventProjector(emit harness.OnEvent) *eventProjector {
	return &eventProjector{emit: emit, startedTool: make(map[string]bool)}
}

func (p *eventProjector) user(parts []harness.ContentPart) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emit(harness.Event{"type": harness.EventUser, "parts": append([]harness.ContentPart(nil), parts...)})
}

func (p *eventProjector) usage(params json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var decoded map[string]any
	if json.Unmarshal(params, &decoded) != nil {
		return
	}
	event := harness.Event{"type": harness.EventUsage}
	if usage, ok := decoded["tokenUsage"]; ok {
		event["tokenUsage"] = usage
	}
	p.emit(event)
}

func (p *eventProjector) itemStarted(raw json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := decodeItem(raw)
	if !ok || !isToolItem(item.Type) {
		return
	}
	p.emitToolCall(item)
}

func (p *eventProjector) itemCompleted(raw json.RawMessage) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := decodeItem(raw)
	if !ok {
		return "", false
	}
	switch item.Type {
	case "agentMessage":
		text, _ := item.Value["text"].(string)
		p.emit(harness.Event{"type": harness.EventAssistant, "text": text})
		return text, true
	case "reasoning":
		text := joinedStrings(item.Value["summary"])
		if text == "" {
			text = joinedStrings(item.Value["content"])
		}
		if text != "" {
			p.emit(harness.Event{"type": harness.EventThinking, "text": text})
		}
		return "", false
	default:
		if isToolItem(item.Type) {
			if !p.startedTool[item.ID] {
				p.emitToolCall(item)
			}
			p.emit(harness.Event{
				"type":    harness.EventToolResult,
				"call_id": item.ID,
				"output":  toolOutput(item),
			})
		}
		return "", false
	}
}

type nativeItem struct {
	ID    string
	Type  string
	Value map[string]any
}

func decodeItem(params json.RawMessage) (nativeItem, bool) {
	var envelope struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(params, &envelope) != nil || envelope.Item == nil {
		return nativeItem{}, false
	}
	id, _ := envelope.Item["id"].(string)
	typeName, _ := envelope.Item["type"].(string)
	if id == "" || typeName == "" {
		return nativeItem{}, false
	}
	return nativeItem{ID: id, Type: typeName, Value: envelope.Item}, true
}

func (p *eventProjector) emitToolCall(item nativeItem) {
	p.startedTool[item.ID] = true
	p.emit(harness.Event{
		"type":    harness.EventToolCall,
		"call_id": item.ID,
		"name":    toolName(item),
		"args":    toolArgs(item),
	})
}

func isToolItem(typeName string) bool {
	switch typeName {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall",
		"collabAgentToolCall", "subAgentActivity", "webSearch", "imageView",
		"sleep", "imageGeneration":
		return true
	default:
		return false
	}
}

func toolName(item nativeItem) string {
	switch item.Type {
	case "mcpToolCall":
		return fmt.Sprintf("%v/%v", item.Value["server"], item.Value["tool"])
	case "dynamicToolCall":
		if namespace, _ := item.Value["namespace"].(string); namespace != "" {
			return namespace + "/" + fmt.Sprint(item.Value["tool"])
		}
		return fmt.Sprint(item.Value["tool"])
	default:
		return item.Type
	}
}

func toolArgs(item nativeItem) any {
	switch item.Type {
	case "commandExecution":
		return map[string]any{"command": item.Value["command"], "cwd": item.Value["cwd"]}
	case "fileChange":
		return map[string]any{"changes": item.Value["changes"]}
	case "mcpToolCall", "dynamicToolCall":
		return item.Value["arguments"]
	case "webSearch":
		return map[string]any{"query": item.Value["query"], "action": item.Value["action"]}
	default:
		return item.Value
	}
}

func toolOutput(item nativeItem) any {
	switch item.Type {
	case "commandExecution":
		return item.Value["aggregatedOutput"]
	case "fileChange":
		return map[string]any{"status": item.Value["status"], "changes": item.Value["changes"]}
	case "mcpToolCall":
		if item.Value["error"] != nil {
			return item.Value["error"]
		}
		return item.Value["result"]
	case "dynamicToolCall":
		return map[string]any{"success": item.Value["success"], "contentItems": item.Value["contentItems"]}
	case "webSearch":
		return item.Value["results"]
	default:
		return item.Value
	}
}

func joinedStrings(value any) string {
	values, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}
