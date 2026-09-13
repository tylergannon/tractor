package observation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sessionStarted is the lifecycle record the run writes when it opens a
// session. The store learns the session's metadata and configured model.
func sessionStarted(session, model string) Lifecycle {
	return Lifecycle{
		Placement: Placement{Session: session},
		Session:   &SessionInfo{Name: session, Adapter: "fixture", Model: model},
		Record:    json.RawMessage(`{"event":{"kind":"SessionCreated","name":"` + session + `"}}`),
	}
}

func created(id, native string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","type":"session.created","created":10,"data":{"sessionID":"` + native + `"}}`)
}

func stepStarted(id, message string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","type":"session.step.started","created":10,"data":{"sessionID":"ses_a","assistantMessageID":"` + message + `","agent":"fixture","model":{}}}`)
}

func openStore(t *testing.T) *Store {
	t.Helper()
	store := Open(nil, "run-1", "fixture", t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// drain reads every frame a finished subscription hands over.
func drain(sub *Subscription) []Frame {
	var out []Frame
	for frame := range sub.Frames() {
		sub.Took(frame)
		out = append(out, frame)
	}
	return out
}

func names(frames []Frame) []string {
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		out = append(out, frame.Name)
	}
	return out
}

// TestSubscribeStartsWithSnapshotThenSuffix is C06 at the public boundary:
// accepted events are in the initial snapshot, and later events are queued as
// its ordered suffix.
func TestSubscribeStartsWithSnapshotThenSuffix(t *testing.T) {
	store := openStore(t)
	store.Lifecycle(sessionStarted("s1", "m"))
	if err := store.Event(Placement{Session: "s1", Turn: "before"}, stepStarted("evt-before", "msg-before"), nil); err != nil {
		t.Fatalf("event before subscribe: %v", err)
	}

	snapshot, sub, err := store.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	if _, ok := snapshot.Invocations["before"]; !ok {
		t.Fatalf("accepted event missing from initial snapshot: %+v", snapshot.Invocations)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "msg-before") {
		t.Fatalf("initial snapshot lacks state from accepted event: %s", raw)
	}
	if err := store.Event(Placement{Session: "s1", Turn: "after"}, created("evt-after", "ses_a"), nil); err != nil {
		t.Fatalf("event after subscribe: %v", err)
	}
	frame := <-sub.Frames()
	sub.Took(frame)
	if frame.Name != FrameEvent || !strings.Contains(string(frame.Data), `"evt-after"`) {
		t.Fatalf("suffix frame = %s %s, want evt-after event", frame.Name, frame.Data)
	}
}

// TestOverflowIsolatesOneSubscriber is C08. A reader that stops reading is
// closed at its bound; the producer does not wait for it, and the subscriber
// beside it keeps receiving the whole ordered suffix.
func TestOverflowIsolatesOneSubscriber(t *testing.T) {
	store := openStore(t)
	store.Lifecycle(sessionStarted("s1", "m"))

	_, healthy, err := store.Subscribe()
	if err != nil {
		t.Fatalf("subscribe healthy: %v", err)
	}
	defer healthy.Close()

	// The next subscriber gets a small bound, so it overflows quickly while
	// the one already registered keeps its own.
	store.mu.Lock()
	store.maxFrames = 4
	store.mu.Unlock()

	_, stalled, err := store.Subscribe()
	if err != nil {
		t.Fatalf("subscribe stalled: %v", err)
	}
	defer stalled.Close()

	// The healthy reader drains continuously; the stalled one never reads.
	seen := make(chan int, 1)
	go func() {
		count := 0
		for frame := range healthy.Frames() {
			healthy.Took(frame)
			if frame.Name == FrameEvent {
				count++
			}
		}
		seen <- count
	}()

	const events = 64
	for i := 0; i < events; i++ {
		if err := store.Event(Placement{Session: "s1", Turn: "t1"}, created("evt", "ses_a"), nil); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}

	select {
	case <-stalled.Done():
	default:
		t.Fatal("the stalled subscriber was not closed at its bound")
	}
	if err := stalled.Err(); err != ErrOverflow {
		t.Fatalf("stalled subscriber ended with %v, want %v", err, ErrOverflow)
	}
	if err := healthy.Err(); err != nil {
		t.Fatalf("the healthy subscriber was affected: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if count := <-seen; count != events {
		t.Fatalf("the healthy subscriber saw %d event frames, want all %d", count, events)
	}
}

// TestNormalCloseDrainsQueuedFrames is the completion boundary: a run that
// finishes normally hands over what it already published, terminal lifecycle
// included, so the browser learns the run ended without reconnecting.
func TestNormalCloseDrainsQueuedFrames(t *testing.T) {
	store := openStore(t)
	_, sub, err := store.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	store.Lifecycle(Lifecycle{
		Status: StatusCompleted,
		Record: json.RawMessage(`{"event":{"kind":"RunEnded"}}`),
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	frames := drain(sub) // returns only once the channel is closed
	if len(frames) != 1 || frames[0].Name != FrameLifecycle {
		t.Fatalf("the terminal suffix was %v, want one lifecycle frame", names(frames))
	}
	if !strings.Contains(string(frames[0].Data), "RunEnded") {
		t.Fatalf("the terminal frame was %s", frames[0].Data)
	}
	select {
	case <-sub.Done():
		t.Fatal("a normal completion cut the reader off instead of handing over its queue")
	default:
	}
}

// TestReaderGoingAwayDoesNotStopTheRun is the cancellation half: releasing a
// connection frees it and leaves the workflow producing exactly as before.
func TestReaderGoingAwayDoesNotStopTheRun(t *testing.T) {
	store := openStore(t)
	store.Lifecycle(sessionStarted("s1", "m"))

	_, leaving, err := store.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_, staying, err := store.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer staying.Close()

	leaving.Close()
	select {
	case <-leaving.Done():
	default:
		t.Fatal("closing a subscription did not release it")
	}

	if err := store.Event(Placement{Session: "s1", Turn: "t1"}, created("evt1", "ses_a"), nil); err != nil {
		t.Fatalf("the run stopped producing after a reader left: %v", err)
	}
	store.mu.Lock()
	subscribers := len(store.subs)
	store.mu.Unlock()
	if subscribers != 1 {
		t.Fatalf("%d subscribers remain, want only the one still reading", subscribers)
	}

	select {
	case frame := <-staying.Frames():
		if frame.Name != FrameEvent {
			t.Fatalf("the remaining subscriber got %q first", frame.Name)
		}
	default:
		t.Fatal("the remaining subscriber received nothing")
	}
}

// TestFinishedRunStaysReadable is the other side of closing subscriptions: a
// finished run is still inspectable, from the registry rather than from a log.
func TestFinishedRunStaysReadable(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, "runs", "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(project)
	store := Open(registry, "run-1", "fixture", dir)
	store.Lifecycle(sessionStarted("s1", "m"))
	if err := store.Event(Placement{Session: "s1", Turn: "t1"}, created("evt1", "ses_a"), nil); err != nil {
		t.Fatalf("event: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, live := registry.Live("run-1"); live {
		t.Fatal("a finished run is still registered as live")
	}
	snapshot, err := registry.Snapshot("run-1")
	if err != nil {
		t.Fatalf("a finished run became unreadable: %v", err)
	}
	if _, ok := snapshot.Invocations["t1"]; !ok {
		t.Fatalf("the finished run's observation lost its invocation: %+v", snapshot)
	}
	if _, err := registry.Snapshot("run-2"); err != ErrNoRun {
		t.Fatalf("an unknown run reported %v, want %v", err, ErrNoRun)
	}
}

func TestCheckpointIsWrittenOnlyAtClose(t *testing.T) {
	dir := t.TempDir()
	store := Open(nil, "run-1", "fixture", dir)
	store.Lifecycle(sessionStarted("s1", "m"))
	for i := 0; i < 256; i++ {
		if err := store.Event(Placement{Session: "s1", Turn: "t1"}, created("evt", "ses_a"), nil); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	path := filepath.Join(dir, checkpointName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkpoint exists on the event path: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("final checkpoint: %v", err)
	}
}

// TestConcurrentProducersAndSubscribers is the race check: the reduction, the
// snapshot cut and the queue handoff are all exercised at once under -race.
func TestConcurrentProducersAndSubscribers(t *testing.T) {
	store := Open(nil, "run-1", "fixture", t.TempDir())
	store.Lifecycle(sessionStarted("s1", "m"))

	var wg sync.WaitGroup
	for turn := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			at := Placement{Session: "s1", Turn: string(rune('a' + turn))}
			for range 50 {
				_ = store.Event(at, created("evt", "ses_a"), nil)
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, sub, err := store.Subscribe()
			if err != nil {
				return
			}
			defer sub.Close()
			for range 20 {
				select {
				case frame, open := <-sub.Frames():
					if !open {
						return
					}
					sub.Took(frame)
				case <-sub.Done():
					return
				}
			}
			_ = store.Snapshot()
		}()
	}
	wg.Wait()
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSnapshotCarriesScopeTree is issue 144 item 3: the snapshot holds the
// workflow state a reload must show, not only the sessions and turns.
func TestSnapshotCarriesScopeTree(t *testing.T) {
	store := openStore(t)
	task := Placement{Scope: "loop.1/task.2"}
	store.Lifecycle(Lifecycle{Placement: Placement{Scope: "loop.1"}, Scope: &ScopeChange{Name: "loop.1", Status: StatusRunning}})
	store.Lifecycle(Lifecycle{Placement: Placement{Scope: "loop.1"}, Decision: json.RawMessage(`{"task":{"name":"write a.txt"}}`)})
	store.Lifecycle(Lifecycle{Placement: task, Scope: &ScopeChange{Name: "task.2", Status: StatusRunning, Task: json.RawMessage(`{"name":"write a.txt"}`)}})
	store.Lifecycle(Lifecycle{Placement: task, Value: &ValueChange{Key: "worker result", Value: json.RawMessage(`"done"`)}})
	store.Lifecycle(Lifecycle{Placement: task, Scope: &ScopeChange{Status: StatusEnded}})

	scopes := store.Snapshot().Scopes
	got := scopes["loop.1/task.2"]
	// An ended scope with no error is ended, never succeeded.
	if got.Name != "task.2" || got.Status != StatusEnded || got.Error != "" {
		t.Fatalf("task scope = %+v", got)
	}
	if string(got.Task) != `{"name":"write a.txt"}` || string(got.Values["worker result"]) != `"done"` {
		t.Fatalf("task scope lost its dispatch or its values: %+v", got)
	}
	decisions := scopes["loop.1"].Decisions
	if len(decisions) != 1 || !strings.Contains(string(decisions[0]), "write a.txt") {
		t.Fatalf("loop scope decisions = %v", decisions)
	}
	if scopes["loop.1"].Status != StatusRunning {
		t.Fatalf("the running loop scope = %+v", scopes["loop.1"])
	}
}
