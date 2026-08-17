package engine

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

	"github.com/tylergannon/tractor/harness"
)

type timelineEvent map[string]any

type runStore struct {
	root  string
	state *engineState

	checkpointMu sync.Mutex
	timelineMu   sync.Mutex
}

type stage struct {
	Seq    uint64
	NodeID string
	Dir    string
	store  *runStore
}

func openRunStore(root string, state *engineState) (*runStore, error) {
	stagesRoot := filepath.Join(root, "stages")
	if err := os.MkdirAll(filepath.Join(stagesRoot, "latest"), 0o755); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	highest, err := recoverStageSequence(stagesRoot)
	if err != nil {
		return nil, fmt.Errorf("recover stage sequence: %w", err)
	}
	state.mu.Lock()
	if highest > state.seq {
		state.seq = highest
	}
	state.mu.Unlock()
	return &runStore{root: root, state: state}, nil
}

// LoadCheckpoint reads the durable state needed to reconstruct a run.
func LoadCheckpoint(root string) (Checkpoint, error) {
	raw, err := os.ReadFile(filepath.Join(root, "checkpoint.json"))
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read checkpoint: %w", err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("decode checkpoint: %w", err)
	}
	checkpoint.CompletedNodes = cloneSlice(checkpoint.CompletedNodes)
	checkpoint.NodeVisits = cloneMap(checkpoint.NodeVisits)
	checkpoint.NodeAttempts = cloneMap(checkpoint.NodeAttempts)
	checkpoint.Sessions = cloneMap(checkpoint.Sessions)
	return checkpoint, nil
}

func (s *runStore) saveCheckpoint(checkpoint Checkpoint) error {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()

	checkpoint.Timestamp = time.Now().UTC()
	checkpoint.CompletedNodes = cloneSlice(checkpoint.CompletedNodes)
	checkpoint.NodeVisits = cloneMap(checkpoint.NodeVisits)
	checkpoint.NodeAttempts = cloneMap(checkpoint.NodeAttempts)
	checkpoint.Sessions = cloneMap(checkpoint.Sessions)
	raw, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	raw = append(raw, '\n')

	path := filepath.Join(s.root, "checkpoint.json")
	temporary := filepath.Join(s.root, ".checkpoint.json.tmp")
	if err := os.WriteFile(temporary, raw, 0o644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace checkpoint: %w", err)
	}
	return nil
}

func (s *runStore) appendTimeline(event timelineEvent) error {
	s.timelineMu.Lock()
	defer s.timelineMu.Unlock()
	stamped := maps.Clone(event)
	if _, exists := stamped["ts"]; !exists {
		stamped["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	file, err := os.OpenFile(filepath.Join(s.root, "timeline.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open timeline: %w", err)
	}
	encodeErr := json.NewEncoder(file).Encode(stamped)
	closeErr := file.Close()
	if encodeErr != nil {
		return fmt.Errorf("append timeline: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close timeline: %w", closeErr)
	}
	return nil
}

func (s *runStore) appendSteering(stageDir string, parts []harness.ContentPart) error {
	file, err := os.OpenFile(filepath.Join(stageDir, "steering.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open steering audit: %w", err)
	}
	encodeErr := json.NewEncoder(file).Encode(struct {
		Timestamp time.Time             `json:"timestamp"`
		Parts     []harness.ContentPart `json:"parts"`
	}{Timestamp: time.Now().UTC(), Parts: parts})
	closeErr := file.Close()
	if encodeErr != nil {
		return fmt.Errorf("append steering audit: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close steering audit: %w", closeErr)
	}
	return nil
}

func (s *runStore) allocateStage(nodeID string) (stage, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	sequence := s.state.seq + 1
	dir := filepath.Join(s.root, "stages", fmt.Sprintf("%06d-%s", sequence, nodeID))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return stage{}, fmt.Errorf("create stage directory: %w", err)
	}
	s.state.seq = sequence
	return stage{Seq: sequence, NodeID: nodeID, Dir: dir, store: s}, nil
}

func (s stage) complete(outcome harness.Outcome) error {
	if err := writeJSON(filepath.Join(s.Dir, "outcome.json"), outcome); err != nil {
		return fmt.Errorf("write outcome: %w", err)
	}
	latest := filepath.Join(s.store.root, "stages", "latest", s.NodeID)
	temporary := latest + ".tmp"
	if err := os.Remove(temporary); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("prepare latest stage link: %w", err)
	}
	target := filepath.Join("..", filepath.Base(s.Dir))
	if err := os.Symlink(target, temporary); err != nil {
		return fmt.Errorf("create latest stage link: %w", err)
	}
	if err := os.Rename(temporary, latest); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace latest stage link: %w", err)
	}
	return nil
}

func (s stage) fail(runErr *harness.Error) error {
	if err := writeJSON(filepath.Join(s.Dir, "error.json"), runErr); err != nil {
		return fmt.Errorf("write stage error: %w", err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func recoverStageSequence(stagesRoot string) (uint64, error) {
	entries, err := os.ReadDir(stagesRoot)
	if err != nil {
		return 0, err
	}
	var highest uint64
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "latest" {
			continue
		}
		dash := strings.IndexByte(entry.Name(), '-')
		if dash <= 0 {
			continue
		}
		sequence, parseErr := strconv.ParseUint(entry.Name()[:dash], 10, 64)
		if parseErr == nil && sequence > highest {
			highest = sequence
		}
	}
	return highest, nil
}
