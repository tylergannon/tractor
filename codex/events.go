package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tylergannon/gimble"
)

// projector turns the ordered app-server stream into OpenCode session events.
// responseID arrives only at rawResponse/completed, so a response window keeps
// one provisional native message key and binds that ID without renaming it.
type projector struct {
	mu        sync.Mutex
	emit      func(gimble.AgentEvent) error
	sessionID string
	turnID    string
	model     string

	response      int
	messageID     string
	responseID    string
	stepOpen      bool
	streamed      bool
	compacting    bool
	partOrdinal   int
	textOpen      bool
	textOrdinal   int
	text          strings.Builder
	reasoningOpen bool
	reasoningOrd  int
	reasoning     strings.Builder
	tools         map[string]*toolState
	pendingTools  int
	usage         normalizedUsage
}

type toolState struct {
	name   string
	output strings.Builder
	done   bool
}

type normalizedUsage struct {
	input, output, reasoning, cacheRead, cacheWrite float64
}

func newProjector(sessionID, turnID, model string, emit func(gimble.AgentEvent) error) *projector {
	return &projector{emit: emit, sessionID: sessionID, turnID: turnID, model: model, tools: make(map[string]*toolState)}
}

func (p *projector) event(eventType string, data map[string]any, native any) error {
	data["sessionID"] = p.sessionID
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	event := gimble.AgentEvent{Type: eventType, Data: raw}
	if native != nil {
		event.NativeRef, err = json.Marshal(native)
		if err != nil {
			return err
		}
	}
	return p.emit(event)
}

func (p *projector) ensureStep(nativeMessageID string, native any) error {
	if p.stepOpen {
		return nil
	}
	p.response++
	p.messageID = nativeMessageID
	if p.messageID == "" {
		p.messageID = fmt.Sprintf("%s/response.%d", p.turnID, p.response)
	}
	p.responseID = ""
	p.stepOpen, p.streamed = true, false
	p.partOrdinal = 0
	p.usage = normalizedUsage{}
	if ref, ok := native.(map[string]any); ok {
		ref["messageID"] = p.messageID
	}
	return p.event("session.step.started", map[string]any{
		"assistantMessageID": p.messageID,
		"agent":              "codex",
		"model":              map[string]any{"providerID": "openai", "id": p.model},
	}, native)
}

func (p *projector) nativeRef(params json.RawMessage) map[string]any {
	ref := map[string]any{"provider": "codex", "sessionID": p.sessionID, "turnID": p.turnID}
	var value map[string]any
	_ = json.Unmarshal(params, &value)
	itemID := stringField(value, "itemId")
	if item := objectValueOrNil(value["item"]); itemID == "" && item != nil {
		itemID = stringField(item, "id")
	}
	if itemID != "" {
		ref["itemID"] = itemID
	}
	if p.messageID != "" {
		ref["messageID"] = p.messageID
	}
	if p.responseID != "" {
		ref["responseID"] = p.responseID
	}
	return ref
}

func (p *projector) itemStarted(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := decodeItem(params)
	if !ok {
		return errors.New("codex: item/started has no item identity")
	}
	if item.kind == "contextCompaction" {
		p.compacting = true
		return nil
	}
	if p.streamed && p.pendingTools == 0 && !isTool(item.kind) {
		if err := p.endStep(nil); err != nil {
			return err
		}
	}
	if err := p.ensureStep(item.id, p.nativeRef(params)); err != nil {
		return err
	}
	switch {
	case item.kind == "agentMessage":
		return p.startText(p.nativeRef(params))
	case item.kind == "reasoning":
		return p.startReasoning(p.nativeRef(params))
	case isTool(item.kind):
		return p.startTool(item, params)
	}
	return nil
}

func (p *projector) startText(native any) error {
	if p.textOpen {
		return errors.New("codex: a second text part opened before the first ended")
	}
	p.textOpen, p.textOrdinal = true, p.partOrdinal
	p.partOrdinal++
	p.text.Reset()
	return p.event("session.text.started", map[string]any{"assistantMessageID": p.messageID, "ordinal": p.textOrdinal}, native)
}

func (p *projector) startReasoning(native any) error {
	if p.reasoningOpen {
		return errors.New("codex: a second reasoning part opened before the first ended")
	}
	p.reasoningOpen, p.reasoningOrd = true, p.partOrdinal
	p.partOrdinal++
	p.reasoning.Reset()
	return p.event("session.reasoning.started", map[string]any{"assistantMessageID": p.messageID, "ordinal": p.reasoningOrd}, native)
}

func (p *projector) startTool(item nativeItem, params json.RawMessage) error {
	if _, exists := p.tools[item.id]; exists {
		return fmt.Errorf("codex: tool %s opened twice", item.id)
	}
	state := &toolState{name: toolName(item)}
	p.tools[item.id] = state
	p.pendingTools++
	ref := p.nativeRef(params)
	if err := p.event("session.tool.input.started", map[string]any{"assistantMessageID": p.messageID, "id": item.id, "name": state.name}, ref); err != nil {
		return err
	}
	input := toolArgs(item)
	inputRaw, _ := json.Marshal(input)
	if err := p.event("session.tool.input.ended", map[string]any{"assistantMessageID": p.messageID, "id": item.id, "text": string(inputRaw)}, ref); err != nil {
		return err
	}
	return p.event("session.tool.called", map[string]any{"assistantMessageID": p.messageID, "id": item.id, "input": objectValue(input), "executed": true}, ref)
}

func (p *projector) textDelta(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stepOpen {
		if err := p.ensureStep(paramItemID(params), p.nativeRef(params)); err != nil {
			return err
		}
	}
	if !p.textOpen {
		if err := p.startText(p.nativeRef(params)); err != nil {
			return err
		}
	}
	delta := deltaText(params)
	p.text.WriteString(delta)
	return p.event("session.text.delta", map[string]any{"assistantMessageID": p.messageID, "ordinal": p.textOrdinal, "delta": delta}, p.nativeRef(params))
}

func (p *projector) reasoningDelta(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stepOpen {
		if err := p.ensureStep(paramItemID(params), p.nativeRef(params)); err != nil {
			return err
		}
	}
	if !p.reasoningOpen {
		if err := p.startReasoning(p.nativeRef(params)); err != nil {
			return err
		}
	}
	delta := deltaText(params)
	p.reasoning.WriteString(delta)
	return p.event("session.reasoning.delta", map[string]any{"assistantMessageID": p.messageID, "ordinal": p.reasoningOrd, "delta": delta}, p.nativeRef(params))
}

func (p *projector) toolOutputDelta(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var value struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal(params, &value); err != nil || value.ItemID == "" {
		return errors.New("codex: tool output delta has no itemId")
	}
	state := p.tools[value.ItemID]
	if state == nil || state.done {
		return fmt.Errorf("codex: output for unopened tool %s", value.ItemID)
	}
	state.output.WriteString(value.Delta)
	return p.event("session.tool.progress", map[string]any{
		"assistantMessageID": p.messageID, "id": value.ItemID,
		"metadata": map[string]any{"output": state.output.String(), "mode": "replace"},
	}, p.nativeRef(params))
}

// itemCompleted emits an authoritative completed item and returns final prose.
func (p *projector) itemCompleted(params json.RawMessage) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	item, ok := decodeItem(params)
	if !ok {
		return "", false, errors.New("codex: item/completed has no item identity")
	}
	if item.kind == "contextCompaction" {
		p.compacting = false
		return "", false, nil
	}
	if !p.stepOpen {
		if err := p.ensureStep(item.id, p.nativeRef(params)); err != nil {
			return "", false, err
		}
	}
	ref := p.nativeRef(params)
	switch {
	case item.kind == "agentMessage":
		text, _ := item.value["text"].(string)
		if !p.textOpen {
			if err := p.startText(ref); err != nil {
				return "", false, err
			}
		}
		p.text.Reset()
		p.text.WriteString(text)
		if err := p.event("session.text.ended", map[string]any{"assistantMessageID": p.messageID, "ordinal": p.textOrdinal, "text": text}, ref); err != nil {
			return "", false, err
		}
		p.textOpen = false
		return text, true, nil
	case item.kind == "reasoning":
		text := joined(item.value["summary"])
		if text == "" {
			text = joined(item.value["content"])
		}
		if text == "" && !p.reasoningOpen {
			return "", false, nil
		}
		if !p.reasoningOpen {
			if err := p.startReasoning(ref); err != nil {
				return "", false, err
			}
		}
		if err := p.event("session.reasoning.ended", map[string]any{"assistantMessageID": p.messageID, "ordinal": p.reasoningOrd, "text": text}, ref); err != nil {
			return "", false, err
		}
		p.reasoningOpen = false
	case isTool(item.kind):
		state := p.tools[item.id]
		if state == nil {
			if err := p.startTool(item, params); err != nil {
				return "", false, err
			}
			state = p.tools[item.id]
		}
		if state.done {
			return "", false, fmt.Errorf("codex: tool %s completed twice", item.id)
		}
		state.done = true
		p.pendingTools--
		output := toolOutput(item)
		if failed, message := toolFailure(item); failed {
			if err := p.event("session.tool.failed", map[string]any{"assistantMessageID": p.messageID, "id": item.id, "error": map[string]any{"type": item.kind, "message": message}, "executed": true}, ref); err != nil {
				return "", false, err
			}
		} else {
			if err := p.event("session.tool.success", map[string]any{"assistantMessageID": p.messageID, "id": item.id, "content": []any{map[string]any{"type": "text", "text": outputText(output)}}, "executed": true}, ref); err != nil {
				return "", false, err
			}
		}
		if p.streamed && p.pendingTools == 0 {
			if err := p.endStep(params); err != nil {
				return "", false, err
			}
		}
	}
	return "", false, nil
}

func (p *projector) rawResponseCompleted(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.compacting {
		return nil
	}
	if p.streamed {
		return errors.New("codex: response completed twice before its step ended")
	}
	var value map[string]any
	if err := json.Unmarshal(params, &value); err != nil {
		return fmt.Errorf("codex: decode raw response completion: %w", err)
	}
	responseID := stringField(value, "responseId", "response_id", "id")
	if responseID == "" {
		return errors.New("codex: rawResponse/completed has no responseId")
	}
	if err := p.ensureStep(responseID, p.nativeRef(params)); err != nil {
		return err
	}
	p.responseID = responseID
	if usage, ok := value["usage"].(map[string]any); ok {
		p.usage = normalizeUsage(usage)
	} else {
		p.usage = normalizedUsage{}
	}
	p.streamed = true
	if err := p.event("session.step.streamed", map[string]any{"assistantMessageID": p.messageID}, p.nativeRef(params)); err != nil {
		return err
	}
	if p.pendingTools == 0 {
		return p.endStep(params)
	}
	return nil
}

// tokenUsageUpdated fills the open step from the turn's most recent model
// call. Only tokenUsage.last is read: tokenUsage.total is cumulative per
// app-server process, so it is never one step's figure. On a turn that
// produced no rawResponse/completed this is the only usage there is.
func (p *projector) tokenUsageUpdated(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stepOpen || p.usage != (normalizedUsage{}) {
		return nil
	}
	var value struct {
		TokenUsage struct {
			Last map[string]any `json:"last"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(params, &value); err != nil {
		return fmt.Errorf("codex: decode thread/tokenUsage/updated: %w", err)
	}
	if value.TokenUsage.Last != nil {
		p.usage = normalizeUsage(value.TokenUsage.Last)
	}
	return nil
}

func (p *projector) turnCompleted(params json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stepOpen {
		if p.pendingTools != 0 {
			return fmt.Errorf("codex: turn completed with %d unsettled tools", p.pendingTools)
		}
		return p.endStep(params)
	}
	return nil
}

func (p *projector) endStep(params json.RawMessage) error {
	if !p.stepOpen {
		return nil
	}
	if p.textOpen || p.reasoningOpen {
		return errors.New("codex: step ended with an open text or reasoning part")
	}
	ref := p.nativeRef(params)
	err := p.event("session.step.ended", map[string]any{
		"assistantMessageID": p.messageID, "finish": "unknown", "cost": 0,
		"tokens": map[string]any{
			"input": p.usage.input, "output": p.usage.output, "reasoning": p.usage.reasoning,
			"cache": map[string]any{"read": p.usage.cacheRead, "write": p.usage.cacheWrite},
		},
	}, ref)
	if err != nil {
		return err
	}
	p.stepOpen, p.streamed = false, false
	p.messageID, p.responseID = "", ""
	p.tools = make(map[string]*toolState)
	return nil
}

type nativeItem struct {
	id    string
	kind  string
	value map[string]any
}

func decodeItem(params json.RawMessage) (nativeItem, bool) {
	var envelope struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(params, &envelope) != nil || envelope.Item == nil {
		return nativeItem{}, false
	}
	id, _ := envelope.Item["id"].(string)
	kind, _ := envelope.Item["type"].(string)
	return nativeItem{id: id, kind: kind, value: envelope.Item}, id != "" && kind != ""
}

func deltaText(params json.RawMessage) string {
	var value map[string]any
	_ = json.Unmarshal(params, &value)
	return stringField(value, "delta", "text")
}

func isTool(kind string) bool {
	switch kind {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "sleep", "imageGeneration":
		return true
	}
	return false
}

func toolName(item nativeItem) string {
	switch item.kind {
	case "mcpToolCall":
		return fmt.Sprintf("%v/%v", item.value["server"], item.value["tool"])
	case "dynamicToolCall":
		if namespace, _ := item.value["namespace"].(string); namespace != "" {
			return namespace + "/" + fmt.Sprint(item.value["tool"])
		}
		return fmt.Sprint(item.value["tool"])
	}
	return item.kind
}

func toolArgs(item nativeItem) any {
	switch item.kind {
	case "commandExecution":
		return map[string]any{"command": item.value["command"], "cwd": item.value["cwd"]}
	case "fileChange":
		return map[string]any{"changes": item.value["changes"]}
	case "mcpToolCall", "dynamicToolCall":
		return item.value["arguments"]
	case "webSearch":
		return map[string]any{"query": item.value["query"], "action": item.value["action"]}
	}
	return item.value
}

func toolOutput(item nativeItem) any {
	switch item.kind {
	case "commandExecution":
		return item.value["aggregatedOutput"]
	case "fileChange":
		return map[string]any{"status": item.value["status"], "changes": item.value["changes"]}
	case "mcpToolCall":
		if item.value["error"] != nil {
			return item.value["error"]
		}
		return item.value["result"]
	case "dynamicToolCall":
		return map[string]any{"success": item.value["success"], "contentItems": item.value["contentItems"]}
	case "webSearch":
		return item.value["results"]
	}
	return item.value
}

func toolFailure(item nativeItem) (bool, string) {
	if item.value["error"] != nil {
		return true, outputText(item.value["error"])
	}
	if success, ok := item.value["success"].(bool); ok && !success {
		return true, outputText(toolOutput(item))
	}
	status := strings.ToLower(fmt.Sprint(item.value["status"]))
	if status == "failed" || status == "error" || status == "declined" {
		return true, outputText(toolOutput(item))
	}
	return false, ""
}

func outputText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func objectValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object
	}
	return map[string]any{"value": value}
}

func objectValueOrNil(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func paramItemID(params json.RawMessage) string {
	var value map[string]any
	_ = json.Unmarshal(params, &value)
	return stringField(value, "itemId")
}

func joined(value any) string {
	values, _ := value.([]any)
	var parts []string
	for _, v := range values {
		if text, ok := v.(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func stringField(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, _ := value[key].(string); text != "" {
			return text
		}
	}
	return ""
}

// normalizeUsage maps Codex's counters onto the five fields every adapter
// reports. The names are camelCase, the only spelling the app-server emits.
// cachedInputTokens is a subset of inputTokens and reasoningOutputTokens a
// subset of outputTokens, so each is subtracted; whether cacheWriteInputTokens
// is also inside inputTokens is unverified, so it is not. A counter the
// notification omits is zero.
func normalizeUsage(value map[string]any) normalizedUsage {
	get := func(key string) float64 {
		number, _ := value[key].(float64)
		return number
	}
	cached, reasoning := get("cachedInputTokens"), get("reasoningOutputTokens")
	return normalizedUsage{
		input:      max(0, get("inputTokens")-cached),
		output:     max(0, get("outputTokens")-reasoning),
		reasoning:  reasoning,
		cacheRead:  cached,
		cacheWrite: get("cacheWriteInputTokens"),
	}
}
