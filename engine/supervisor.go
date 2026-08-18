package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

type liveExecution struct {
	NodeID  string `json:"node_id"`
	Attempt int    `json:"attempt,omitempty"`
	RunLog  string `json:"run_log,omitempty"`
}

func (r *Runner) beginExecution(nodeID string, attempt int, runLog string) uint64 {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	r.nextLiveID++
	r.live[r.nextLiveID] = liveExecution{NodeID: nodeID, Attempt: attempt, RunLog: runLog}
	return r.nextLiveID
}

func (r *Runner) endExecution(id uint64) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	delete(r.live, id)
}

func (r *Runner) liveSnapshot() []liveExecution {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	entries := make([]liveExecution, 0, len(r.live))
	for _, entry := range r.live {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].NodeID != entries[j].NodeID {
			return entries[i].NodeID < entries[j].NodeID
		}
		return entries[i].Attempt < entries[j].Attempt
	})
	return entries
}

type attemptDigest struct {
	NodeID      string                `json:"node_id"`
	Disposition string                `json:"disposition"`
	Seq         uint64                `json:"seq"`
	Attempt     int                   `json:"attempt"`
	StageDir    string                `json:"stage_dir"`
	Next        string                `json:"next,omitempty"`
	Notes       string                `json:"notes,omitempty"`
	Category    harness.ErrorCategory `json:"category,omitempty"`
	Message     string                `json:"message,omitempty"`
}

type supervisorDigest struct {
	NodeID      string `json:"node_id"`
	Disposition string `json:"disposition"`
	Verdict     string `json:"verdict,omitempty"`
	Target      string `json:"target,omitempty"`
	Message     string `json:"message,omitempty"`
	Delivered   *bool  `json:"delivered,omitempty"`
}

type supervisorRuntime struct {
	node     *graph.SupervisorNode
	dir      string
	interval time.Duration

	inboxMu   sync.Mutex
	errorMu   sync.Mutex
	nextBatch uint64
	busy      atomic.Bool
}

type supervisionErrorRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	Supervisor string    `json:"supervisor"`
	Operation  string    `json:"operation"`
	Error      string    `json:"error"`
	Batch      string    `json:"batch,omitempty"`
}

type supervisorBriefingRecord struct {
	Binding harness.ThreadBinding `json:"binding"`
}

type supervisionService struct {
	runner   *Runner
	store    *runStore
	byID     map[string]*supervisorRuntime
	all      []*supervisorRuntime
	done     chan struct{}
	stop     sync.Once
	stopping atomic.Bool
	wg       sync.WaitGroup
}

func newSupervisionService(runner *Runner, store *runStore) (*supervisionService, error) {
	service := &supervisionService{
		runner: runner,
		store:  store,
		byID:   make(map[string]*supervisorRuntime),
		done:   make(chan struct{}),
	}
	for _, candidate := range runner.graph.Nodes {
		node, ok := candidate.(*graph.SupervisorNode)
		if !ok {
			continue
		}
		interval, err := node.IntervalValue().Parse()
		if err != nil || interval <= 0 {
			return nil, fmt.Errorf("parse supervisor %s interval: %v", node.ID, err)
		}
		dir := filepath.Join(store.root, "supervisors", node.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create supervisor directory %s: %w", node.ID, err)
		}
		nextBatch, err := recoverSupervisorBatch(dir)
		if err != nil {
			return nil, fmt.Errorf("recover supervisor %s batches: %w", node.ID, err)
		}
		runtime := &supervisorRuntime{node: node, dir: dir, interval: interval, nextBatch: nextBatch}
		service.all = append(service.all, runtime)
		service.byID[node.ID] = runtime
	}
	return service, nil
}

func (s *supervisionService) start() {
	if s.runner.config.Backend == nil {
		return
	}
	for _, runtime := range s.all {
		s.wg.Add(1)
		go s.patrol(runtime)
	}
}

func (s *supervisionService) stopAndWait() {
	didStop := false
	s.stop.Do(func() {
		didStop = true
		s.stopping.Store(true)
		close(s.done)
	})
	if !didStop {
		s.wg.Wait()
		return
	}
	if len(s.all) == 0 || s.runner.config.Backend == nil {
		s.wg.Wait()
		return
	}

	// A flush publishes its engine registry entry immediately before entering
	// the backend. Keep interrupting until every flush returns so a stop cannot
	// land in that narrow handoff and miss the backend's live-turn registry.
	stopped := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(stopped)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.runner.config.Backend.InterruptAll()
		select {
		case <-stopped:
			return
		case <-ticker.C:
		}
	}
}

func (s *supervisionService) patrol(runtime *supervisorRuntime) {
	defer s.wg.Done()
	ticker := time.NewTicker(runtime.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-s.runner.stop.done:
			return
		case <-ticker.C:
			if s.stopping.Load() || s.runner.stop.IsSet() {
				return
			}
			snapshot := s.runner.liveSnapshot()
			if !scopeIsLive(runtime.node.Supervises, snapshot) || !runtime.busy.CompareAndSwap(false, true) {
				continue
			}
			s.wg.Go(func() {
				defer runtime.busy.Store(false)
				s.flush(runtime, snapshot)
			})
		}
	}
}

func scopeIsLive(scope []string, snapshot []liveExecution) bool {
	for _, entry := range snapshot {
		if slices.Contains(scope, entry.NodeID) {
			return true
		}
	}
	return false
}

func (s *supervisionService) appendAttempt(nodeID string, attempt int, stage stage, outcome harness.Outcome, runErr *harness.Error) {
	for _, runtime := range s.all {
		if !contains(runtime.node.Supervises, nodeID) {
			continue
		}
		digest := attemptDigest{
			NodeID: nodeID, Seq: stage.Seq, Attempt: attempt, StageDir: stage.Dir,
		}
		if runErr == nil {
			digest.Disposition = "outcome"
			digest.Next = outcome.Next
			digest.Notes = outcome.Notes
		} else {
			digest.Disposition = "error"
			digest.Category = runErr.Category
			digest.Message = runErr.Message
		}
		if err := runtime.append(digest); err != nil {
			runtime.recordError("append_attempt_digest", err, "")
		}
	}
}

func (r *Runner) appendAttemptDigests(nodeID string, attempt int, stage stage, outcome harness.Outcome, runErr *harness.Error) {
	if r.supervision != nil {
		r.supervision.appendAttempt(nodeID, attempt, stage, outcome, runErr)
	}
}

func (runtime *supervisorRuntime) append(digest any) error {
	runtime.inboxMu.Lock()
	defer runtime.inboxMu.Unlock()
	return appendJSONLine(filepath.Join(runtime.dir, "inbox.jsonl"), digest)
}

func (runtime *supervisorRuntime) recordError(operation string, err error, batch string) {
	if err == nil {
		return
	}
	runtime.errorMu.Lock()
	defer runtime.errorMu.Unlock()
	_ = appendJSONLine(filepath.Join(runtime.dir, "errors.jsonl"), supervisionErrorRecord{
		Timestamp: time.Now().UTC(), Supervisor: runtime.node.ID, Operation: operation, Error: err.Error(), Batch: batch,
	})
}

func (runtime *supervisorRuntime) rotate() (string, int, map[string]map[string]int, error) {
	runtime.inboxMu.Lock()
	defer runtime.inboxMu.Unlock()

	inbox := filepath.Join(runtime.dir, "inbox.jsonl")
	info, err := os.Stat(inbox)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return "", 0, nil, nil
	}
	if err != nil {
		return "", 0, nil, err
	}
	runtime.nextBatch++
	batch := filepath.Join(runtime.dir, fmt.Sprintf("inbox.%06d.jsonl", runtime.nextBatch))
	if err := os.Rename(inbox, batch); err != nil {
		runtime.nextBatch--
		return "", 0, nil, err
	}
	count, tally, err := digestTally(batch)
	return batch, count, tally, err
}

func digestTally(path string) (int, map[string]map[string]int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = file.Close() }()
	tally := make(map[string]map[string]int)
	count := 0
	decoder := json.NewDecoder(file)
	for {
		var header struct {
			NodeID      string `json:"node_id"`
			Disposition string `json:"disposition"`
		}
		if err := decoder.Decode(&header); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return count, tally, err
		}
		if tally[header.NodeID] == nil {
			tally[header.NodeID] = make(map[string]int)
		}
		tally[header.NodeID][header.Disposition]++
		count++
	}
	return count, tally, nil
}

func (s *supervisionService) flush(runtime *supervisorRuntime, snapshot []liveExecution) {
	if s.stopping.Load() || s.runner.stop.IsSet() {
		return
	}
	segment, err := s.runner.runLogs.Allocate(runtime.node.ID)
	if err != nil {
		runtime.recordError("allocate_run_log", err, "")
		return
	}
	batch, count, tally, err := runtime.rotate()
	if err != nil {
		runtime.recordError("rotate_inbox", err, batch)
		return
	}
	event := timelineEvent{"type": "SupervisorFlushed", "supervisor": runtime.node.ID, "count": count}
	if batch != "" {
		event["batch"] = batch
	}
	if err := s.store.appendTimeline(event); err != nil {
		runtime.recordError("append_flush_event", err, batch)
		return
	}
	briefing, briefingErr := s.supervisorNeedsBriefing(runtime)
	if briefingErr != nil {
		runtime.recordError("read_briefing_record", briefingErr, batch)
	}
	message, renderErr := renderNudge(runtime, snapshot, batch, count, tally, briefing, s.runner.graph.Goal)
	if renderErr != nil {
		runtime.recordError("render_nudge", renderErr, batch)
		return
	}
	turn, turnErr := s.supervisorTurn(runtime.node, message, segment.Path)
	if turnErr != nil {
		s.recordVerdict(runtime, harness.Verdict{}, turnErr)
		return
	}
	liveID := s.runner.beginExecution(runtime.node.ID, 0, segment.Path)
	if s.stopping.Load() || s.runner.stop.IsSet() {
		s.runner.endExecution(liveID)
		return
	}
	verdict, runErr := s.runner.config.Backend.RunSupervisor(turn)
	s.runner.endExecution(liveID)
	if briefing && runErr == nil {
		if err := s.markSupervisorBriefed(runtime); err != nil {
			runtime.recordError("write_briefing_record", err, batch)
		}
	}
	s.recordVerdict(runtime, verdict, runErr)
}

func (s *supervisionService) supervisorTurn(node *graph.SupervisorNode, message, runLog string) (harness.SupervisorTurn, *harness.Error) {
	model := resolveString(node.LLMModel, s.runner.graph.Defaults.LLMModel, s.runner.config.DefaultModel)
	provider := resolveProvider(node.LLMProvider, s.runner.graph.Defaults.LLMProvider, s.runner.config.DefaultProvider, model)
	effortDefault := s.runner.config.DefaultReasoningEffort
	if effortDefault == "" {
		effortDefault = "high"
	}
	effort := resolveString(node.ReasoningEffort, s.runner.graph.Defaults.ReasoningEffort, effortDefault)
	timeout, err := resolveTimeout(node.Timeout, s.runner.graph.Defaults.Timeout)
	if err != nil {
		return harness.SupervisorTurn{}, terminalError(err.Error())
	}
	schema, err := verdictSchema(node.Supervises)
	if err != nil {
		return harness.SupervisorTurn{}, terminalError(err.Error())
	}
	return harness.SupervisorTurn{
		NodeID: node.ID, Parts: []harness.ContentPart{{Type: harness.ContentPartText, Text: message}},
		OutputSchema: schema, Model: model, Provider: provider, ReasoningEffort: effort,
		Workdir: s.runner.config.Workdir, RunLog: runLog, Timeout: timeout,
	}, nil
}

func verdictSchema(scope []string) (json.RawMessage, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"ok", "steer"}, "description": "Use steer only to coach a named in-scope target; otherwise use ok."},
			"target":  map[string]any{"type": "string", "enum": scope, "description": "Required when verdict is steer; the in-scope node to coach."},
			"message": map[string]any{"type": "string", "description": "For steer, a required non-blank instruction delivered verbatim; for ok, an optional observation."},
		},
		"required": []string{"verdict"}, "additionalProperties": false,
	}
	return json.Marshal(schema)
}

func renderNudge(runtime *supervisorRuntime, snapshot []liveExecution, batch string, count int, tally map[string]map[string]int, briefing bool, goal string) (string, error) {
	data := struct {
		Live  []liveExecution           `json:"live"`
		Batch string                    `json:"batch,omitempty"`
		Count int                       `json:"count"`
		Tally map[string]map[string]int `json:"tally,omitempty"`
	}{Live: inScope(runtime.node.Supervises, snapshot), Batch: batch, Count: count, Tally: tally}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	var message strings.Builder
	if briefing {
		message.WriteString("Supervisor briefing (idempotent):\n")
		message.WriteString(expandPrompt(runtime.node.Prompt, goal))
		message.WriteString("\n\nRun goal: ")
		message.WriteString(goal)
		message.WriteString("\nSupervisor directory: ")
		message.WriteString(runtime.dir)
		message.WriteString("\nReturn verdict=ok, or verdict=steer with an in-scope target and non-blank message.\n\n")
	}
	message.WriteString("Patrol nudge:\n")
	message.Write(raw)
	return message.String(), nil
}

func inScope(scope []string, snapshot []liveExecution) []liveExecution {
	entries := make([]liveExecution, 0, len(snapshot))
	for _, entry := range snapshot {
		if contains(scope, entry.NodeID) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (s *supervisionService) recordVerdict(runtime *supervisorRuntime, verdict harness.Verdict, runErr *harness.Error) {
	verdictName := verdict.Verdict
	target := verdict.Target
	message := verdict.Message
	delivered := false
	if runErr != nil {
		runtime.recordError("supervisor_turn", errors.New(runErr.Message), "")
		verdictName = "error"
		target = ""
		message = runErr.Message
	} else if verdictName == "steer" && (!contains(runtime.node.Supervises, target) || strings.TrimSpace(message) == "") {
		verdictName = "ok"
		target = ""
	} else if verdictName == "steer" {
		if targetRuntime := s.byID[target]; targetRuntime != nil {
			if err := targetRuntime.append(supervisorDigest{NodeID: runtime.node.ID, Disposition: "coaching", Message: message}); err != nil {
				targetRuntime.recordError("append_coaching_digest", err, "")
			} else {
				delivered = true
			}
		} else {
			var deliveryErr error
			delivered, deliveryErr = s.runner.deliverSupervisorSteer(s.store, runtime.node.ID, target, message)
			if deliveryErr != nil {
				runtime.recordError("append_steering_audit", deliveryErr, "")
			}
		}
	} else {
		target = ""
	}
	event := timelineEvent{"type": "SupervisorVerdict", "supervisor": runtime.node.ID, "verdict": verdictName}
	if verdictName == "steer" {
		event["target"] = target
		event["delivered"] = delivered
	}
	if err := s.store.appendTimeline(event); err != nil {
		runtime.recordError("append_verdict_event", err, "")
	}

	digest := supervisorDigest{NodeID: runtime.node.ID, Disposition: "verdict", Verdict: verdictName, Target: target, Message: message}
	if verdictName == "steer" {
		digest.Delivered = &delivered
	}
	for _, parent := range s.all {
		if contains(parent.node.Supervises, runtime.node.ID) {
			if err := parent.append(digest); err != nil {
				parent.recordError("append_verdict_digest", err, "")
			}
		}
	}
}

func (r *Runner) deliverSupervisorSteer(store *runStore, origin, target, message string) (bool, error) {
	parts := []harness.ContentPart{{Type: harness.ContentPartText, Text: message}}
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.active == nil || r.active.nodeID != target || r.active.stageDir == "" || r.active.nodeType == "parallel" || r.config.Backend == nil {
		return false, nil
	}
	if r.config.Backend.Steer(parts) != harness.SteerAccepted {
		return false, nil
	}
	if err := store.appendSteering(r.active.stageDir, parts, origin); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Runner) installBindingCallback(store *runStore) {
	if r.config.Backend == nil {
		return
	}
	r.config.Backend.SetBindingOpened(func(threadKey string, _ harness.ThreadBinding) *harness.Error {
		if r.supervision == nil || r.supervision.byID[threadKey] == nil {
			return nil
		}
		r.checkpointMu.Lock()
		defer r.checkpointMu.Unlock()
		checkpoint := r.lastCheckpoint
		checkpoint.Sessions = r.bindings()
		if err := store.saveCheckpoint(checkpoint); err != nil {
			return terminalError(err.Error())
		}
		r.lastCheckpoint = checkpoint
		_ = store.appendTimeline(timelineEvent{"type": "CheckpointSaved", "node_id": threadKey})
		return nil
	})
}

func recoverSupervisorBatch(dir string) (uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var highest uint64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "inbox.") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		number := strings.TrimSuffix(strings.TrimPrefix(name, "inbox."), ".jsonl")
		value, err := strconv.ParseUint(number, 10, 64)
		if err == nil && value > highest {
			highest = value
		}
	}
	return highest, nil
}

func (s *supervisionService) supervisorNeedsBriefing(runtime *supervisorRuntime) (bool, error) {
	binding, exists := s.runner.bindings()[runtime.node.ID]
	if !exists {
		return true, nil
	}
	raw, err := os.ReadFile(filepath.Join(runtime.dir, "briefed.json"))
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	var record supervisorBriefingRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return true, err
	}
	return record.Binding != binding, nil
}

func (s *supervisionService) markSupervisorBriefed(runtime *supervisorRuntime) error {
	binding, exists := s.runner.bindings()[runtime.node.ID]
	if !exists {
		return fmt.Errorf("supervisor binding is absent after completed turn")
	}
	return writeJSON(filepath.Join(runtime.dir, "briefed.json"), supervisorBriefingRecord{Binding: binding})
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

func expandPrompt(prompt, goal string) string { return strings.ReplaceAll(prompt, "$goal", goal) }

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
