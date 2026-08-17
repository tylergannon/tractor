package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/tylergannon/tractor/harness"
)

func TestRunStorePersistsCheckpointTimelineAndStageArtifacts(t *testing.T) {
	state := newEngineState()
	state.completedNodes = []string{"start"}
	state.nodeVisits["start"] = 1
	state.nodeAttempts["start"] = 1
	state.lastStage = "start"
	state.lastResponse = "run started"
	store, err := openRunStore(t.TempDir(), state)
	if err != nil {
		t.Fatal(err)
	}

	failed, err := store.allocateStage("review")
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.fail(&harness.Error{Category: harness.ErrorTerminal, Message: "review failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(store.root, "stages", "latest", "review")); !os.IsNotExist(err) {
		t.Fatalf("latest link after failed stage: %v", err)
	}

	succeeded, err := store.allocateStage("review")
	if err != nil {
		t.Fatal(err)
	}
	outcome := harness.Outcome{Next: "exit", Notes: "approved"}
	if err := succeeded.complete(outcome); err != nil {
		t.Fatal(err)
	}
	assertJSONFile(t, filepath.Join(failed.Dir, "error.json"), harness.Error{Category: harness.ErrorTerminal, Message: "review failed"})
	assertJSONFile(t, filepath.Join(succeeded.Dir, "outcome.json"), outcome)
	latest, err := os.Readlink(filepath.Join(store.root, "stages", "latest", "review"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("..", filepath.Base(succeeded.Dir)); latest != want {
		t.Fatalf("latest target = %q, want %q", latest, want)
	}

	sessions := map[string]harness.ThreadBinding{
		"review": {Harness: "codex", SessionID: "session-1", Workdir: "/workspace"},
	}
	checkpoint := state.checkpoint("review", "exit", false, sessions)
	if checkpoint.Seq != 2 {
		t.Fatalf("checkpoint sequence = %d, want 2", checkpoint.Seq)
	}
	if err := store.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(store.root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Timestamp.IsZero() {
		t.Fatal("checkpoint timestamp is zero")
	}
	checkpoint.Timestamp = loaded.Timestamp
	if !reflect.DeepEqual(loaded, checkpoint) {
		t.Fatalf("loaded checkpoint = %#v, want %#v", loaded, checkpoint)
	}

	if err := store.appendTimeline(timelineEvent{"type": "PipelineStarted", "name": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.appendTimeline(timelineEvent{"type": "CheckpointSaved", "node_id": "review"}); err != nil {
		t.Fatal(err)
	}
	events, err := readTimeline(filepath.Join(store.root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := []any{events[0]["type"], events[1]["type"]}; !reflect.DeepEqual(got, []any{"PipelineStarted", "CheckpointSaved"}) {
		t.Fatalf("timeline event types = %v", got)
	}
}

func TestRunStoreRecoversSequenceAboveCheckpointAndStageScan(t *testing.T) {
	root := t.TempDir()
	stagesRoot := filepath.Join(root, "stages")
	if err := os.MkdirAll(filepath.Join(stagesRoot, "latest"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000003-old", "000009-crashed", "malformed"} {
		if err := os.Mkdir(filepath.Join(stagesRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stagesRoot, "000099-not-a-directory"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	state := stateFromCheckpoint(Checkpoint{Seq: 5})
	store, err := openRunStore(root, state)
	if err != nil {
		t.Fatal(err)
	}
	stageTen, err := store.allocateStage("node")
	if err != nil {
		t.Fatal(err)
	}
	if stageTen.Seq != 10 || filepath.Base(stageTen.Dir) != "000010-node" {
		t.Fatalf("recovered stage = %#v", stageTen)
	}

	reconstructed := stateFromCheckpoint(Checkpoint{Seq: 12})
	second, err := openRunStore(root, reconstructed)
	if err != nil {
		t.Fatal(err)
	}
	stageThirteen, err := second.allocateStage("node")
	if err != nil {
		t.Fatal(err)
	}
	if stageThirteen.Seq != 13 || filepath.Base(stageThirteen.Dir) != "000013-node" {
		t.Fatalf("checkpoint-ahead stage = %#v", stageThirteen)
	}
}

func TestCheckpointReplacementAndTimelineAppendAreAtomicWithinProcess(t *testing.T) {
	store, err := openRunStore(t.TempDir(), newEngineState())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveCheckpoint(Checkpoint{CurrentNode: "initial"}); err != nil {
		t.Fatal(err)
	}

	const writes = 40
	readErrors := make(chan error, 1)
	stopReading := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stopReading:
				return
			default:
			}
			raw, err := os.ReadFile(filepath.Join(store.root, "checkpoint.json"))
			if err == nil {
				var checkpoint Checkpoint
				err = json.Unmarshal(raw, &checkpoint)
			}
			if err != nil {
				select {
				case readErrors <- err:
				default:
				}
				return
			}
		}
	}()
	for index := range writes {
		if err := store.saveCheckpoint(Checkpoint{CurrentNode: fmt.Sprintf("node-%d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	close(stopReading)
	<-readerDone
	if err := pollError(readErrors); err != nil {
		t.Fatalf("checkpoint reader observed partial JSON: %v", err)
	}

	var wait sync.WaitGroup
	timelineErrors := make(chan error, 1)
	for index := range writes {
		wait.Go(func() {
			if err := store.appendTimeline(timelineEvent{"type": "event", "index": index}); err != nil {
				select {
				case timelineErrors <- err:
				default:
				}
			}
		})
	}
	wait.Wait()
	if err := pollError(timelineErrors); err != nil {
		t.Fatal(err)
	}
	events, err := readTimeline(filepath.Join(store.root, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writes {
		t.Fatalf("timeline events = %d, want %d", len(events), writes)
	}
}

func pollError(errors <-chan error) error {
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}

func readTimeline(path string) ([]timelineEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var events []timelineEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event timelineEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func assertJSONFile[T any](t *testing.T, path string, want T) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got T
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", path, got, want)
	}
}
