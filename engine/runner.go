package engine

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/tylergannon/tractor/graph"
	"github.com/tylergannon/tractor/harness"
)

// ExecutionScope is the engine-owned context for one handler execution.
type ExecutionScope struct {
	Workdir  string
	StageDir string
	Goal     string
	Stop     *StopSignal
}

// Handler executes one graph node.
type Handler interface {
	Execute(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error)

func (f HandlerFunc) Execute(node graph.Node, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph) (harness.Outcome, *harness.Error) {
	return f(node, offered, scope, pipeline)
}

// Registry maps node type strings to handlers.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry returns a registry containing the non-LLM built-in handlers.
func NewRegistry() *Registry {
	registry := &Registry{handlers: make(map[string]Handler)}
	registry.Register("start", HandlerFunc(startHandler))
	registry.Register("tool", HandlerFunc(toolHandler))
	return registry
}

// Register installs a handler, replacing any handler for the same type.
func (r *Registry) Register(nodeType string, handler Handler) {
	r.handlers[nodeType] = handler
}

// Resolve returns the handler registered for node's explicit type.
func (r *Registry) Resolve(node graph.Node) (Handler, *harness.Error) {
	handler := r.handlers[node.NodeType()]
	if handler == nil {
		return nil, terminalError(fmt.Sprintf("unknown handler type: %s", node.NodeType()))
	}
	return handler, nil
}

func startHandler(graph.Node, []graph.Edge, ExecutionScope, *graph.Graph) (harness.Outcome, *harness.Error) {
	return harness.Outcome{Notes: "run started"}, nil
}

// StopSignal is a one-shot operator-stop signal.
type StopSignal struct {
	once sync.Once
	done chan struct{}
}

// NewStopSignal creates an unset stop signal.
func NewStopSignal() *StopSignal {
	return &StopSignal{done: make(chan struct{})}
}

// Stop sets the signal. Repeated calls have no effect.
func (s *StopSignal) Stop() {
	s.once.Do(func() { close(s.done) })
}

// IsSet reports whether Stop has been called.
func (s *StopSignal) IsSet() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Wait blocks until Stop is called.
func (s *StopSignal) Wait() {
	<-s.done
}

// ValidateFunc performs the graph validation phase before initialization.
type ValidateFunc func(graph.Graph) error

// RunnerConfig supplies the run paths and the already-configured backend.
type RunnerConfig struct {
	LogsRoot string
	Workdir  string
	Validate ValidateFunc
	Backend  harness.CodergenBackend
	Stop     *StopSignal
}

// RunStatus is the terminal status of a graph walk.
type RunStatus string

const (
	RunCompleted RunStatus = "COMPLETED"
	RunFailed    RunStatus = "FAILED"
)

// RunResult is the run-level verdict.
type RunResult struct {
	Status        RunStatus
	FailureReason string
}

type nodeExecution struct {
	outcome   harness.Outcome
	nextID    string
	runErr    *harness.Error
	attempted bool
	stageDirs []string
}

// Runner executes one graph from its start node.
type Runner struct {
	graph            graph.Graph
	registry         *Registry
	config           RunnerConfig
	stop             *StopSignal
	nodes            map[string]graph.Node
	startID          string
	resumeCheckpoint *Checkpoint
	retryDelay       func(int) time.Duration
	activeMu         sync.Mutex
	active           *activeExecution
}

// NewRunner validates the graph and prepares a runner without creating run files.
func NewRunner(pipeline graph.Graph, registry *Registry, config RunnerConfig) (*Runner, error) {
	return newRunner(pipeline, registry, config)
}

// ResumeRunner validates the graph and prepares a runner from its durable checkpoint.
func ResumeRunner(pipeline graph.Graph, registry *Registry, config RunnerConfig) (*Runner, error) {
	runner, err := newRunner(pipeline, registry, config)
	if err != nil {
		return nil, err
	}
	checkpoint, err := LoadCheckpoint(config.LogsRoot)
	if err != nil {
		return nil, err
	}
	if err := runner.validateResumeCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	runner.resumeCheckpoint = &checkpoint
	return runner, nil
}

func newRunner(pipeline graph.Graph, registry *Registry, config RunnerConfig) (*Runner, error) {
	if strings.TrimSpace(config.LogsRoot) == "" {
		return nil, fmt.Errorf("logs root must not be empty")
	}
	if strings.TrimSpace(config.Workdir) == "" {
		return nil, fmt.Errorf("workdir must not be empty")
	}
	if config.Validate == nil {
		return nil, fmt.Errorf("validate function must not be nil")
	}
	if err := config.Validate(pipeline); err != nil {
		return nil, fmt.Errorf("validate graph: %w", err)
	}
	registry = cloneRegistry(registry)
	stop := config.Stop
	if stop == nil {
		stop = NewStopSignal()
	}
	nodes := make(map[string]graph.Node, len(pipeline.Nodes))
	startID := ""
	startCount := 0
	for _, node := range pipeline.Nodes {
		if node == nil {
			return nil, fmt.Errorf("graph contains a nil node")
		}
		nodes[node.Base().ID] = node
		if node.NodeType() == "start" {
			startID = node.Base().ID
			startCount++
		}
	}
	if startCount != 1 {
		return nil, fmt.Errorf("graph must contain exactly one start node")
	}
	return &Runner{
		graph:      pipeline,
		registry:   registry,
		config:     config,
		stop:       stop,
		nodes:      nodes,
		startID:    startID,
		retryDelay: defaultRetryDelay,
	}, nil
}

func cloneRegistry(registry *Registry) *Registry {
	if registry == nil {
		return NewRegistry()
	}
	cloned := &Registry{handlers: make(map[string]Handler, len(registry.handlers))}
	maps.Copy(cloned.handlers, registry.handlers)
	return cloned
}

// Stop requests that the run end at the next interruption point.
func (r *Runner) Stop() {
	r.stop.Stop()
	if r.config.Backend != nil {
		r.config.Backend.InterruptAll()
	}
}

// Run initializes or restores durable state and walks the graph to completion or failure.
func (r *Runner) Run() (RunResult, error) {
	state := newEngineState()
	currentID := r.startID
	if r.resumeCheckpoint != nil {
		if r.resumeCheckpoint.NextNode == "" {
			if err := cleanupBranchWorktrees(r.config.Workdir, r.config.LogsRoot); err != nil {
				return RunResult{}, err
			}
			return RunResult{Status: RunCompleted}, nil
		}
		state = stateFromCheckpoint(*r.resumeCheckpoint)
		currentID = r.resumeCheckpoint.NextNode
	}
	store, err := openRunStore(r.config.LogsRoot, state)
	if err != nil {
		return RunResult{}, err
	}
	control, manifest, err := r.startControlServer(store)
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = control.close() }()
	started := time.Now()
	if err := store.appendTimeline(timelineEvent{
		"type": "PipelineStarted",
		"name": r.graph.Name,
		"id":   manifest.ID,
	}); err != nil {
		return RunResult{}, err
	}
	r.registry.Register("parallel", &parallelHandler{runner: r, state: state, store: store})
	if r.resumeCheckpoint == nil {
		if err := r.saveCheckpoint(store, state.checkpoint("", r.startID, false, r.bindings()), r.startID); err != nil {
			eventErr := store.appendTimeline(timelineEvent{
				"type": "PipelineFailed", "error": err.Error(), "duration": time.Since(started).String(),
			})
			return RunResult{}, errors.Join(err, eventErr)
		}
	}
	result, runErr := r.walk(state, store, currentID)
	duration := time.Since(started).String()
	if runErr != nil {
		if err := store.appendTimeline(timelineEvent{"type": "PipelineFailed", "error": runErr.Error(), "duration": duration}); err != nil {
			return RunResult{}, errors.Join(runErr, err)
		}
		return result, runErr
	}
	if result.Status == RunFailed {
		if err := store.appendTimeline(timelineEvent{"type": "PipelineFailed", "error": result.FailureReason, "duration": duration}); err != nil {
			return RunResult{}, err
		}
		return result, nil
	}
	if err := store.appendTimeline(timelineEvent{"type": "PipelineCompleted", "duration": duration}); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func (r *Runner) walk(state *engineState, store *runStore, currentID string) (RunResult, error) {
	for {
		node := r.nodes[currentID]
		if node == nil {
			return failed(fmt.Sprintf("unknown node: %s", currentID)), nil
		}
		if node.NodeType() == "exit" {
			if err := r.saveCheckpoint(store, state.checkpoint(currentID, "", false, r.bindings()), currentID); err != nil {
				return RunResult{}, err
			}
			if err := cleanupBranchWorktrees(r.config.Workdir, r.config.LogsRoot); err != nil {
				return RunResult{}, err
			}
			return RunResult{Status: RunCompleted}, nil
		}
		if r.stop.IsSet() {
			return failed("stopped by operator"), nil
		}

		r.beginTopLevel(node.Base().ID, node.NodeType())
		execution, executeErr := r.executeNode(node, state, store, r.config.Workdir, "")
		r.clearTopLevel(node.Base().ID)
		if executeErr != nil {
			return RunResult{}, executeErr
		}
		if execution.runErr != nil {
			if execution.attempted {
				if err := r.saveCheckpoint(store, state.checkpoint(node.Base().ID, node.Base().ID, true, r.bindings()), node.Base().ID); err != nil {
					return RunResult{}, err
				}
			}
			return failed(execution.runErr.Message), nil
		}
		state.complete(node.Base().ID, execution.outcome.Notes)
		if err := r.saveCheckpoint(store, state.checkpoint(node.Base().ID, execution.nextID, false, r.bindings()), node.Base().ID); err != nil {
			return RunResult{}, err
		}
		currentID = execution.nextID
	}
}

func (r *Runner) saveCheckpoint(store *runStore, checkpoint Checkpoint, nodeID string) error {
	if err := store.saveCheckpoint(checkpoint); err != nil {
		return err
	}
	return store.appendTimeline(timelineEvent{"type": "CheckpointSaved", "node_id": nodeID})
}

func (r *Runner) executeNode(node graph.Node, state *engineState, store *runStore, workdir, branchID string) (nodeExecution, error) {
	offered, err := r.offeredSuccessors(node, state)
	if err != nil {
		return nodeExecution{runErr: terminalError(err.Error())}, nil
	}
	if len(offered) == 0 {
		return nodeExecution{runErr: terminalError(fmt.Sprintf("every successor of %s has exhausted its visit budget", node.Base().ID))}, nil
	}
	handler, resolveErr := r.registry.Resolve(node)
	if resolveErr != nil {
		return nodeExecution{runErr: resolveErr}, nil
	}
	outcome, nextID, runErr, attempted, stageDirs, err := r.executeWithRetry(node, handler, offered, state, store, workdir, branchID)
	execution := nodeExecution{outcome: outcome, nextID: nextID, runErr: runErr, attempted: attempted, stageDirs: stageDirs}
	if err != nil {
		return execution, err
	}
	return execution, nil
}

func (r *Runner) validateResumeCheckpoint(checkpoint Checkpoint) error {
	if checkpoint.NextNode == "" {
		current := r.nodes[checkpoint.CurrentNode]
		if current == nil || current.NodeType() != "exit" || checkpoint.RetryVisit {
			return fmt.Errorf("checkpoint has no valid continuation")
		}
		return nil
	}
	if r.nodes[checkpoint.NextNode] == nil {
		return fmt.Errorf("checkpoint next node %q is not in the graph", checkpoint.NextNode)
	}
	if checkpoint.CurrentNode == "" {
		if checkpoint.NextNode != r.startID || checkpoint.RetryVisit {
			return fmt.Errorf("checkpoint has an invalid initial continuation")
		}
		return nil
	}
	current := r.nodes[checkpoint.CurrentNode]
	if current == nil {
		return fmt.Errorf("checkpoint current node %q is not in the graph", checkpoint.CurrentNode)
	}
	if current.NodeType() == "exit" {
		return fmt.Errorf("checkpoint continues after exit node %q", checkpoint.CurrentNode)
	}
	if checkpoint.RetryVisit && checkpoint.NextNode != checkpoint.CurrentNode {
		return fmt.Errorf("checkpoint retry continuation does not name current node %q", checkpoint.CurrentNode)
	}
	return nil
}

func (r *Runner) executeWithRetry(
	node graph.Node,
	handler Handler,
	offered []graph.Edge,
	state *engineState,
	store *runStore,
	workdir, branchID string,
) (harness.Outcome, string, *harness.Error, bool, []string, error) {
	maxRetries := resolvedMaxRetries(node, r.graph.Defaults)
	if maxRetries < 0 {
		return harness.Outcome{}, "", terminalError("max_retries must be nonnegative"), false, nil, nil
	}
	maxAttempts := maxRetries + 1
	stageDirs := make([]string, 0, maxAttempts)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if r.stop.IsSet() {
			return harness.Outcome{}, "", interruptedError("stopped by operator"), attempt > 1, stageDirs, nil
		}
		stage, err := store.allocateStage(node.Base().ID)
		if err != nil {
			return harness.Outcome{}, "", nil, attempt > 1, stageDirs, err
		}
		stageDirs = append(stageDirs, stage.Dir)
		r.setActiveStage(node.Base().ID, stage.Dir)
		stageStarted := time.Now()
		if err := store.appendTimeline(stageEvent(branchID, workdir, timelineEvent{
			"type":  "StageStarted",
			"name":  node.Base().ID,
			"index": stage.Seq,
		})); err != nil {
			return harness.Outcome{}, "", nil, attempt > 1, stageDirs, err
		}
		if attempt == 1 {
			state.beginVisit(node.Base().ID)
		}
		state.beginAttempt(node.Base().ID)
		outcome, runErr := callHandler(handler, node, offered, ExecutionScope{
			Workdir:  workdir,
			StageDir: stage.Dir,
			Goal:     r.graph.Goal,
			Stop:     r.stop,
		}, &r.graph)
		if runErr == nil {
			if err := stage.complete(outcome); err != nil {
				return harness.Outcome{}, "", nil, true, stageDirs, err
			}
			nextID, err := r.resolveNext(node, outcome, offered)
			if err != nil {
				return outcome, "", terminalError(err.Error()), true, stageDirs, nil
			}
			if err := store.appendTimeline(stageEvent(branchID, workdir, timelineEvent{
				"type":     "StageCompleted",
				"name":     node.Base().ID,
				"index":    stage.Seq,
				"duration": time.Since(stageStarted).String(),
				"next":     nextID,
			})); err != nil {
				return harness.Outcome{}, "", nil, true, stageDirs, err
			}
			return outcome, nextID, nil, true, stageDirs, nil
		}
		if err := stage.fail(runErr); err != nil {
			return harness.Outcome{}, "", nil, true, stageDirs, err
		}
		willRetry := runErr.Category == harness.ErrorRetryable && attempt < maxAttempts
		if err := store.appendTimeline(stageEvent(branchID, workdir, timelineEvent{
			"type":       "StageFailed",
			"name":       node.Base().ID,
			"index":      stage.Seq,
			"error":      runErr.Message,
			"will_retry": willRetry,
		})); err != nil {
			return harness.Outcome{}, "", nil, true, stageDirs, err
		}
		if !willRetry {
			return harness.Outcome{}, "", runErr, true, stageDirs, nil
		}
		delay := r.retryDelay(attempt)
		if err := store.appendTimeline(stageEvent(branchID, workdir, timelineEvent{
			"type":    "StageRetrying",
			"name":    node.Base().ID,
			"index":   stage.Seq,
			"attempt": attempt + 1,
			"delay":   delay.String(),
		})); err != nil {
			return harness.Outcome{}, "", nil, true, stageDirs, err
		}
		if !waitForBackoff(delay, r.stop) {
			return harness.Outcome{}, "", interruptedError("stopped by operator"), true, stageDirs, nil
		}
	}
	panic("unreachable")
}

func stageEvent(branchID, workdir string, event timelineEvent) timelineEvent {
	if branchID != "" {
		event["branch"] = branchID
		event["workdir"] = workdir
	}
	return event
}

func resolvedMaxRetries(node graph.Node, defaults graph.Defaults) int {
	var configured int
	var present bool
	var supported bool
	switch current := node.(type) {
	case *graph.CodergenNode:
		supported = true
		configured, present = current.MaxRetries.Value, current.MaxRetries.Present
	case *graph.FanInNode:
		supported = true
		configured, present = current.MaxRetries.Value, current.MaxRetries.Present
	case *graph.CustomNode:
		supported = true
		configured, present = current.MaxRetries.Value, current.MaxRetries.Present
	}
	if !supported {
		return 0
	}
	if present {
		return configured
	}
	if defaults.MaxRetries.Present {
		return defaults.MaxRetries.Value
	}
	return 0
}

func defaultRetryDelay(attempt int) time.Duration {
	return backoffDelay(attempt, rand.Float64())
}

func backoffDelay(attempt int, jitter float64) time.Duration {
	delay := 200 * time.Millisecond
	for step := 1; step < attempt && delay < 60*time.Second; step++ {
		delay *= 2
		if delay > 60*time.Second {
			delay = 60 * time.Second
		}
	}
	return time.Duration(float64(delay) * (0.5 + jitter))
}

func waitForBackoff(delay time.Duration, stop *StopSignal) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-stop.done:
		return false
	}
}

func (r *Runner) offeredSuccessors(node graph.Node, state *engineState) ([]graph.Edge, error) {
	offered := make([]graph.Edge, 0, len(node.Base().Edges))
	for _, edge := range node.Base().Edges {
		target := r.nodes[edge.To]
		if target == nil {
			return nil, fmt.Errorf("edge from %s names unknown node %s", node.Base().ID, edge.To)
		}
		budget := target.Base().MaxVisits
		if budget.Present && state.visits(target.Base().ID) >= budget.Value {
			continue
		}
		offered = append(offered, edge)
	}
	return offered, nil
}

func (r *Runner) bindings() map[string]harness.ThreadBinding {
	if r.config.Backend == nil {
		return map[string]harness.ThreadBinding{}
	}
	return r.config.Backend.Bindings()
}

func (r *Runner) resolveNext(node graph.Node, outcome harness.Outcome, offered []graph.Edge) (string, error) {
	if node.NodeType() == "parallel" {
		join, err := parallelFanIn(r.graph, node.Base().ID)
		if err != nil {
			return "", err
		}
		if outcome.Next == join.ID {
			return join.ID, nil
		}
		return "", fmt.Errorf("parallel handler named invalid fan-in successor: %s", outcome.Next)
	}
	if outcome.Next == "" {
		if len(offered) == 1 {
			return offered[0].To, nil
		}
		return "", fmt.Errorf("handler supplied no choice among %d offered successors", len(offered))
	}
	for _, edge := range offered {
		if edge.To == outcome.Next {
			return outcome.Next, nil
		}
	}
	return "", fmt.Errorf("chooser named an unoffered successor: %s", outcome.Next)
}

func callHandler(handler Handler, node graph.Node, offered []graph.Edge, scope ExecutionScope, pipeline *graph.Graph) (outcome harness.Outcome, runErr *harness.Error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = harness.Outcome{}
			runErr = terminalError(fmt.Sprintf("handler panic: %v", recovered))
		}
	}()
	return handler.Execute(node, offered, scope, pipeline)
}

func terminalError(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorTerminal, Message: message}
}

func interruptedError(message string) *harness.Error {
	return &harness.Error{Category: harness.ErrorInterrupted, Message: message}
}

func failed(reason string) RunResult {
	return RunResult{Status: RunFailed, FailureReason: reason}
}
