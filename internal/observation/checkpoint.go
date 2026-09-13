package observation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// checkpointName is the final observation written into a run directory.
// Live runs are served directly from their registered store.
const checkpointName = "observation.json"

// writeCheckpoint atomically persists one detached final snapshot. It runs
// after the store has closed, outside the reduction lock and event hot path.
func writeCheckpoint(dir string, snapshot RunSnapshot) error {
	if dir == "" {
		return nil
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, checkpointName)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// Close freezes the final snapshot, ends subscribers, persists the snapshot,
// and then removes the store from the live registry. No disk work happens on
// the event path or while the reduction lock is held.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	final := s.snapshotLocked()
	s.finishSubscribersLocked()
	s.mu.Unlock()

	if err := writeCheckpoint(s.dir, final); err != nil {
		return fmt.Errorf("observation: write checkpoint: %w", err)
	}
	if s.registry != nil {
		s.registry.finish(s.id)
	}
	return nil
}

func loadCheckpoint(dir string) (RunSnapshot, error) {
	var out RunSnapshot
	raw, err := os.ReadFile(filepath.Join(dir, checkpointName))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("observation: decode checkpoint in %s: %w", dir, err)
	}
	if out.Run.Sessions == nil {
		out.Run.Sessions = map[string]SessionInfo{}
	}
	if out.Run.Usage == nil {
		out.Run.Usage = map[string]Usage{}
	}
	if out.Scopes == nil {
		out.Scopes = map[string]ScopeInfo{}
	}
	if out.Invocations == nil {
		out.Invocations = map[string]Invocation{}
	}
	return out, nil
}
