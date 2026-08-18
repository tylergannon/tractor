package runlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestAllocatorRecoversAndSerializesConcurrentAllocations(t *testing.T) {
	root := t.TempDir()
	events := filepath.Join(root, "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000002-old.jsonl", "000009-later.jsonl", "index.jsonl", "unrelated.txt"} {
		if err := os.WriteFile(filepath.Join(events, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(events, "000099-directory.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	allocator, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	const count = 20
	segments := make(chan Segment, count)
	errors := make(chan error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Go(func() {
			segment, allocateErr := allocator.Allocate(fmt.Sprintf("node_%d", i))
			if allocateErr != nil {
				errors <- allocateErr
				return
			}
			segments <- segment
		})
	}
	workers.Wait()
	close(errors)
	close(segments)
	for allocateErr := range errors {
		t.Errorf("Allocate() error = %v", allocateErr)
	}

	sequences := make([]int, 0, count)
	for segment := range segments {
		if _, statErr := os.Stat(segment.Path); statErr != nil {
			t.Errorf("allocated segment %q: %v", segment.Path, statErr)
		}
		sequences = append(sequences, int(segment.Seq))
	}
	sort.Ints(sequences)
	for i, sequence := range sequences {
		if want := 10 + i; sequence != want {
			t.Fatalf("sequence[%d] = %d, want %d; all = %v", i, sequence, want, sequences)
		}
	}

	index, err := os.Open(filepath.Join(events, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := index.Close(); err != nil {
			t.Errorf("close index: %v", err)
		}
	})
	indexed := make(map[uint64]bool, count)
	scanner := bufio.NewScanner(index)
	for scanner.Scan() {
		var entry struct {
			Seq    uint64 `json:"seq"`
			NodeID string `json:"node_id"`
			Path   string `json:"path"`
			TS     string `json:"ts"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.NodeID == "" || entry.Path == "" || entry.TS == "" {
			t.Fatalf("incomplete index entry: %#v", entry)
		}
		indexed[entry.Seq] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(indexed) != count {
		t.Fatalf("indexed sequences = %d, want %d", len(indexed), count)
	}
}

func TestAllocatorReconstructionContinuesSequence(t *testing.T) {
	root := t.TempDir()
	first, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := first.Allocate("first")
	if err != nil {
		t.Fatal(err)
	}
	if segment.Seq != 1 {
		t.Fatalf("first sequence = %d", segment.Seq)
	}

	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	segment, err = second.Allocate("second")
	if err != nil {
		t.Fatal(err)
	}
	if segment.Seq != 2 {
		t.Fatalf("reconstructed sequence = %d", segment.Seq)
	}
}
