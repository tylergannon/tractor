package examples_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The tractor skill bundles copies of the loop examples so agents can use
// them without the repo checked out. This tripwire keeps the copies from
// drifting.
func TestSkillBundleMatchesLoopExamples(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("loops/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no loop examples found")
	}
	for _, path := range paths {
		bundled := filepath.Join("..", "skills", "tractor", "examples", filepath.Base(path))
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(bundled)
		if err != nil {
			t.Fatalf("skill bundle missing %s: %v", filepath.Base(path), err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s differs from %s; re-copy the example into the skill bundle", bundled, path)
		}
	}
}
