// Package runlog allocates the event segments used by backend turns.
package runlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Segment identifies one allocated run-log segment.
type Segment struct {
	Seq  uint64
	Path string
}

// Allocator creates run-log segments in one run directory.
type Allocator struct {
	logsRoot   string
	eventsRoot string

	mu      sync.Mutex
	nextSeq uint64
}

// New constructs an allocator and recovers its sequence from existing
// segments.
func New(logsRoot string) (*Allocator, error) {
	if strings.TrimSpace(logsRoot) == "" {
		return nil, fmt.Errorf("logs root must not be empty")
	}
	eventsRoot := filepath.Join(logsRoot, "events")
	if err := os.MkdirAll(eventsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create run log directory: %w", err)
	}
	nextSeq, err := recoverSequence(eventsRoot)
	if err != nil {
		return nil, fmt.Errorf("recover run log sequence: %w", err)
	}
	return &Allocator{logsRoot: logsRoot, eventsRoot: eventsRoot, nextSeq: nextSeq}, nil
}

// Allocate creates and indexes the next segment for nodeID. Allocation is
// serialized across all callers of this allocator.
func (a *Allocator) Allocate(nodeID string) (Segment, error) {
	if strings.TrimSpace(nodeID) == "" {
		return Segment{}, fmt.Errorf("node ID must not be empty")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.nextSeq++
	seq := a.nextSeq
	name := fmt.Sprintf("%06d-%s.jsonl", seq, nodeID)
	relativePath := filepath.Join("events", name)
	absolutePath := filepath.Join(a.logsRoot, relativePath)
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return Segment{}, fmt.Errorf("create run log segment: %w", err)
	}
	if err := file.Close(); err != nil {
		return Segment{}, fmt.Errorf("close run log segment: %w", err)
	}

	entry := struct {
		Seq    uint64 `json:"seq"`
		NodeID string `json:"node_id"`
		Path   string `json:"path"`
		TS     string `json:"ts"`
	}{Seq: seq, NodeID: nodeID, Path: relativePath, TS: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := appendJSONLine(filepath.Join(a.eventsRoot, "index.jsonl"), entry); err != nil {
		return Segment{}, fmt.Errorf("append run log index: %w", err)
	}
	return Segment{Seq: seq, Path: absolutePath}, nil
}

func recoverSequence(eventsRoot string) (uint64, error) {
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
