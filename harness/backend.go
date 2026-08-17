package harness

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const noneThreadPrefix = "\x00none:"

// DefaultProviderRoutes returns the standard provider-to-harness routing.
func DefaultProviderRoutes() map[string]string {
	return map[string]string{
		"anthropic": "claude",
		"openai":    "codex",
	}
}

// HarnessBackend routes codergen turns to harness adapters and owns the
// logical bindings, live controls, and run log for one pipeline run.
type HarnessBackend struct {
	logsRoot string
	adapters map[string]HarnessAdapter
	routes   map[string]string

	mu           sync.Mutex
	threads      map[string]ThreadBinding
	bindingLocks map[string]*sync.Mutex
	live         map[uint64]liveTurn
	nextLiveID   uint64

	nextSeq              uint64
	currentPinnedToIndex bool
}

type liveTurn struct {
	binding ThreadBinding
}

// NewHarnessBackend constructs a backend for one run. A nil routes map uses
// DefaultProviderRoutes. Restored bindings are copied into the backend.
func NewHarnessBackend(
	logsRoot string,
	adapters map[string]HarnessAdapter,
	routes map[string]string,
	bindings map[string]ThreadBinding,
) (*HarnessBackend, *Error) {
	if strings.TrimSpace(logsRoot) == "" {
		return nil, terminalError("logs root must not be empty")
	}
	if routes == nil {
		routes = DefaultProviderRoutes()
	}

	backend := &HarnessBackend{
		logsRoot:     logsRoot,
		adapters:     copyAdapters(adapters),
		routes:       copyStrings(routes),
		threads:      make(map[string]ThreadBinding, len(bindings)),
		bindingLocks: make(map[string]*sync.Mutex),
		live:         make(map[uint64]liveTurn),
	}
	for key, binding := range bindings {
		if strings.TrimSpace(key) == "" || !validBinding(binding) {
			return nil, terminalError(fmt.Sprintf("invalid restored thread binding %q", key))
		}
		if backend.adapters[binding.Harness] == nil {
			return nil, terminalError(fmt.Sprintf("restored thread %q uses unconfigured harness %q", key, binding.Harness))
		}
		backend.threads[key] = binding
	}
	eventsRoot := filepath.Join(logsRoot, "events")
	if err := os.MkdirAll(eventsRoot, 0o755); err != nil {
		return nil, terminalError(fmt.Sprintf("create run log directory: %v", err))
	}
	nextSeq, err := recoverEventSequence(eventsRoot)
	if err != nil {
		return nil, terminalError(fmt.Sprintf("recover run log sequence: %v", err))
	}
	backend.nextSeq = nextSeq
	return backend, nil
}

// Run executes one fully resolved codergen turn.
func (b *HarnessBackend) Run(turn CodergenTurn) (Outcome, *Error) {
	if err := ValidateCodergenTurn(turn); err != nil {
		return Outcome{}, err
	}
	validator, validationErr := NewResultValidator(turn.OutputSchema)
	if validationErr != nil {
		return Outcome{}, validationErr
	}
	harnessName, adapter, err := b.selectAdapter(turn.Provider)
	if err != nil {
		return Outcome{}, err
	}

	key := turn.ThreadKey
	if turn.Fidelity == FidelityNone {
		key = noneThreadPrefix + turn.NodeID
	}
	binding, err := b.prepareBinding(key, harnessName, adapter, turn)
	if err != nil {
		return Outcome{}, err
	}

	turnLog, liveID, err := b.startTurnLog(turn.NodeID, binding)
	if err != nil {
		return Outcome{}, err
	}
	result, adapterErr := adapter.RunTurn(RunTurnInput{
		SessionID:       binding.SessionID,
		Model:           turn.Model,
		ReasoningEffort: turn.ReasoningEffort,
		OutputSchema:    turn.OutputSchema,
		Workdir:         turn.Workdir,
		Parts:           turn.Parts,
		Timeout:         turn.Timeout,
	}, turnLog.writeEvent)
	logErr := b.finishTurnLog(liveID, turnLog)

	if adapterErr != nil {
		return Outcome{}, adapterErr
	}
	if logErr != nil {
		return Outcome{}, logErr
	}
	rawResult, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return Outcome{}, terminalError(fmt.Sprintf("encode harness result: %v", marshalErr))
	}
	validated, resultErr := validator.Validate(rawResult)
	if resultErr != nil {
		return Outcome{}, resultErr
	}
	return decodeOutcome(validated)
}

func (b *HarnessBackend) selectAdapter(provider string) (string, HarnessAdapter, *Error) {
	harnessName, ok := b.routes[provider]
	if !ok || strings.TrimSpace(harnessName) == "" {
		return "", nil, terminalError(fmt.Sprintf("no harness route for provider %q", provider))
	}
	adapter := b.adapters[harnessName]
	if adapter == nil {
		return "", nil, terminalError(fmt.Sprintf("harness %q is not configured", harnessName))
	}
	return harnessName, adapter, nil
}

func (b *HarnessBackend) prepareBinding(
	key string,
	harnessName string,
	adapter HarnessAdapter,
	turn CodergenTurn,
) (ThreadBinding, *Error) {
	lock := b.bindingLock(key)
	lock.Lock()
	defer lock.Unlock()

	b.mu.Lock()
	binding, exists := b.threads[key]
	b.mu.Unlock()
	if exists && turn.Fidelity != FidelityNone {
		if binding.Harness != harnessName {
			return ThreadBinding{}, terminalError("logical thread cannot change harness")
		}
		if binding.Workdir == turn.Workdir {
			if turn.Fidelity == FidelityCompacted {
				if err := adapter.Compact(binding.SessionID, turn.Workdir); err != nil {
					return ThreadBinding{}, err
				}
			}
			return binding, nil
		}
	}

	sessionID, err := adapter.CreateSession(turn.Model, turn.Workdir)
	if err != nil {
		return ThreadBinding{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return ThreadBinding{}, terminalError("harness returned an empty session ID")
	}
	binding = ThreadBinding{Harness: harnessName, SessionID: sessionID, Workdir: turn.Workdir}
	b.mu.Lock()
	b.threads[key] = binding
	b.mu.Unlock()
	return binding, nil
}

func (b *HarnessBackend) bindingLock(key string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	lock := b.bindingLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		b.bindingLocks[key] = lock
	}
	return lock
}

// Steer sends a best-effort instruction when exactly one turn is live.
func (b *HarnessBackend) Steer(parts []ContentPart) SteerStatus {
	b.mu.Lock()
	if len(b.live) == 0 {
		b.mu.Unlock()
		return SteerNotActive
	}
	if len(b.live) != 1 {
		b.mu.Unlock()
		return SteerAmbiguousTarget
	}
	var target ThreadBinding
	for _, turn := range b.live {
		target = turn.binding
	}
	adapter := b.adapters[target.Harness]
	b.mu.Unlock()

	adapter.Steer(target.SessionID, parts)
	return SteerAccepted
}

// InterruptAll requests interruption of every live turn.
func (b *HarnessBackend) InterruptAll() {
	b.mu.Lock()
	targets := make([]ThreadBinding, 0, len(b.live))
	for _, turn := range b.live {
		targets = append(targets, turn.binding)
	}
	b.mu.Unlock()

	for _, target := range targets {
		b.adapters[target.Harness].Interrupt(target.SessionID)
	}
}

// Bindings returns a checkpoint-safe snapshot of all logical bindings.
func (b *HarnessBackend) Bindings() map[string]ThreadBinding {
	b.mu.Lock()
	defer b.mu.Unlock()
	bindings := make(map[string]ThreadBinding, len(b.threads))
	maps.Copy(bindings, b.threads)
	return bindings
}

type turnLog struct {
	file   *os.File
	nodeID string
	mu     sync.Mutex
	err    error
}

func (l *turnLog) writeEvent(event Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return
	}
	stamped := make(Event, len(event)+2)
	maps.Copy(stamped, event)
	stamped["node_id"] = l.nodeID
	stamped["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	l.err = json.NewEncoder(l.file).Encode(stamped)
}

func (l *turnLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.file.Close(); l.err == nil {
		l.err = err
	}
	return l.err
}

func (b *HarnessBackend) startTurnLog(nodeID string, binding ThreadBinding) (*turnLog, uint64, *Error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextSeq++
	seq := b.nextSeq
	name := fmt.Sprintf("%06d-%s.jsonl", seq, nodeID)
	relativePath := filepath.Join("events", name)
	absolutePath := filepath.Join(b.logsRoot, relativePath)
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, 0, terminalError(fmt.Sprintf("create run log segment: %v", err))
	}
	entry := struct {
		Seq    uint64 `json:"seq"`
		NodeID string `json:"node_id"`
		Path   string `json:"path"`
		TS     string `json:"ts"`
	}{Seq: seq, NodeID: nodeID, Path: relativePath, TS: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := appendJSONLine(filepath.Join(b.logsRoot, "events", "index.jsonl"), entry); err != nil {
		_ = file.Close()
		_ = os.Remove(absolutePath)
		return nil, 0, terminalError(fmt.Sprintf("append run log index: %v", err))
	}

	pinnedToIndex := b.currentPinnedToIndex || len(b.live) > 0
	currentTarget := relativePath
	if pinnedToIndex {
		currentTarget = filepath.Join("events", "index.jsonl")
	}
	if err := b.swapCurrent(currentTarget); err != nil {
		_ = file.Close()
		return nil, 0, terminalError(fmt.Sprintf("update current run log: %v", err))
	}
	b.nextLiveID++
	liveID := b.nextLiveID
	b.live[liveID] = liveTurn{binding: binding}
	b.currentPinnedToIndex = pinnedToIndex
	return &turnLog{file: file, nodeID: nodeID}, liveID, nil
}

func (b *HarnessBackend) finishTurnLog(liveID uint64, log *turnLog) *Error {
	closeErr := log.close()

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.live, liveID)
	if len(b.live) == 0 {
		b.currentPinnedToIndex = false
	}
	if closeErr != nil {
		return terminalError(fmt.Sprintf("write run log segment: %v", closeErr))
	}
	return nil
}

func recoverEventSequence(eventsRoot string) (uint64, error) {
	entries, err := os.ReadDir(eventsRoot)
	if err != nil {
		return 0, err
	}
	var highest uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		dash := strings.IndexByte(name, '-')
		if dash <= 0 || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sequence, parseErr := strconv.ParseUint(name[:dash], 10, 64)
		if parseErr == nil && sequence > highest {
			highest = sequence
		}
	}
	return highest, nil
}

func (b *HarnessBackend) swapCurrent(target string) error {
	current := filepath.Join(b.logsRoot, "current.jsonl")
	temporary := filepath.Join(b.logsRoot, ".current.jsonl.tmp")
	if err := os.Remove(temporary); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, current); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func appendJSONLine(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(value)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func decodeOutcome(result Result) (Outcome, *Error) {
	notes, ok := result["notes"].(string)
	if !ok {
		return Outcome{}, terminalError("harness result has no string notes field")
	}
	outcome := Outcome{Notes: notes}
	if next, exists := result["next"]; exists {
		value, ok := next.(string)
		if !ok {
			return Outcome{}, terminalError("harness result has a non-string next field")
		}
		outcome.Next = value
	}
	return outcome, nil
}

func copyAdapters(source map[string]HarnessAdapter) map[string]HarnessAdapter {
	out := make(map[string]HarnessAdapter, len(source))
	maps.Copy(out, source)
	return out
}

func copyStrings(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	maps.Copy(out, source)
	return out
}

func validBinding(binding ThreadBinding) bool {
	return strings.TrimSpace(binding.Harness) != "" &&
		strings.TrimSpace(binding.SessionID) != "" &&
		strings.TrimSpace(binding.Workdir) != ""
}
