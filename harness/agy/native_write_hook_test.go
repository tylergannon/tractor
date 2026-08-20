package agy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// runHookScript feeds payload to the real hook command (the one Tractor
// provisions into hooks.json) via `sh -c`, exactly as agy would invoke it,
// and parses its stdout decision.
func runHookScript(t *testing.T, payload string) (decision, reason string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", nativeWriteHookCommand())
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("hook command failed: %v (stderr=%s)", err, exitErr.Stderr)
		}
		t.Fatalf("hook command failed: %v", err)
	}
	var parsed struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("hook command did not print valid JSON: %v (%q)", err, out)
	}
	return parsed.Decision, parsed.Reason
}

// Payload shapes below mirror the real agy 1.1.15 PreToolUse stdin payload,
// captured empirically by a debug hook during this work (see
// ephemeral/worklog/202608191900-agy-artifact-prevention.md).

func TestNativeWriteHookCommandAllowsPlainWorkspaceWrite(t *testing.T) {
	// No ArtifactMetadata argument at all: this is the common case (an
	// ordinary workspace file) and is legal regardless of where it targets
	// — agy's own validator never rejects it. Verified empirically: an
	// identical write_to_file call to this same kind of target succeeded
	// once the model omitted ArtifactMetadata (see the worklog).
	payload := `{"artifactDirectoryPath":"/Users/tyler/.gemini/antigravity-cli/brain/abc","conversationId":"abc","toolCall":{"name":"write_to_file","args":{"TargetFile":"/Users/tyler/workspace/foo.md","CodeContent":"hi"}}}`
	decision, _ := runHookScript(t, payload)
	if decision != "allow" {
		t.Fatalf("decision = %q, want allow for a write with no ArtifactMetadata argument", decision)
	}
}

func TestNativeWriteHookCommandAllowsInBrainArtifact(t *testing.T) {
	payload := `{"artifactDirectoryPath":"/Users/tyler/.gemini/antigravity-cli/brain/abc","conversationId":"abc","toolCall":{"name":"write_to_file","args":{"ArtifactMetadata":{"UserFacing":true},"TargetFile":"/Users/tyler/.gemini/antigravity-cli/brain/abc/note.md","CodeContent":"hi"}}}`
	decision, _ := runHookScript(t, payload)
	if decision != "allow" {
		t.Fatalf("decision = %q, want allow for an ArtifactMetadata write targeting inside artifactDirectoryPath", decision)
	}
}

func TestNativeWriteHookCommandDeniesOutOfBrainArtifact(t *testing.T) {
	// The one combination empirically confirmed to reproduce agy's bug:
	// ArtifactMetadata present AND the target outside artifactDirectoryPath.
	payload := `{"artifactDirectoryPath":"/Users/tyler/.gemini/antigravity-cli/brain/abc","conversationId":"abc","toolCall":{"name":"write_to_file","args":{"ArtifactMetadata":{"UserFacing":true},"TargetFile":"/Users/tyler/workspace/foo.md","CodeContent":"hi"}}}`
	decision, reason := runHookScript(t, payload)
	if decision != "deny" {
		t.Fatalf("decision = %q, want deny for an ArtifactMetadata write outside artifactDirectoryPath", decision)
	}
	if !strings.Contains(reason, nativeWriteHookMarker) {
		t.Fatalf("deny reason = %q, want it to contain %q", reason, nativeWriteHookMarker)
	}
	if !strings.Contains(reason, "ArtifactMetadata") {
		t.Fatalf("deny reason = %q, want it to tell the model to omit ArtifactMetadata", reason)
	}
}

func TestNativeWriteHookCommandDeniesWhenArtifactDirectoryMissing(t *testing.T) {
	// A payload that doesn't parse as expected (an ArtifactMetadata write
	// with no artifactDirectoryPath) must fail closed, never open: it can
	// only ever wrongly deny a write agy would have accepted, never wrongly
	// allow one it would reject.
	decision, _ := runHookScript(t, `{"toolCall":{"name":"write_to_file","args":{"ArtifactMetadata":{},"TargetFile":"/workspace/foo.md"}}}`)
	if decision != "deny" {
		t.Fatalf("decision = %q, want deny when artifactDirectoryPath is absent from an ArtifactMetadata payload", decision)
	}
}

func TestNativeWriteHookCommandDeniesEmptyPayload(t *testing.T) {
	// An empty payload carries no `"ArtifactMetadata":` substring, so the
	// script's own fail-closed-only-for-artifact-calls design would allow
	// it by default; feed it an ArtifactMetadata payload instead to prove
	// the parse-failure branch (missing artifactDirectoryPath and
	// TargetFile) still denies rather than allowing on garbage input.
	decision, _ := runHookScript(t, `{"toolCall":{"args":{"ArtifactMetadata":{}}}}`)
	if decision != "deny" {
		t.Fatalf("decision = %q, want deny for an unparseable ArtifactMetadata payload", decision)
	}
}

func TestNativeWriteHookCommandAllowsCompletelyEmptyPayload(t *testing.T) {
	// A payload with no ArtifactMetadata marker at all (including a truly
	// empty stdin) allows by default — see the "plain workspace write"
	// case above. This is intentional, not a gap: agy's validator itself
	// never rejects a call lacking ArtifactMetadata, so there is nothing
	// for the hook to protect against here.
	decision, _ := runHookScript(t, "")
	if decision != "allow" {
		t.Fatalf("decision = %q, want allow for a payload with no ArtifactMetadata marker", decision)
	}
}

func TestNativeWriteHookCommandDeniesIfAnyTargetEscapesRoot(t *testing.T) {
	// multi_replace_file_content's exact args shape wasn't independently
	// captured live (see worklog); this proves the script's stance if an
	// ArtifactMetadata-bearing call ever carries more than one TargetFile
	// occurrence: any target outside the root denies the whole call.
	payload := `{"artifactDirectoryPath":"/brain/abc","toolCall":{"name":"multi_replace_file_content","args":{"ArtifactMetadata":{},"Edits":[{"TargetFile":"/brain/abc/a.md"},{"TargetFile":"/workspace/b.md"}]}}}`
	decision, _ := runHookScript(t, payload)
	if decision != "deny" {
		t.Fatalf("decision = %q, want deny when any extracted TargetFile lies outside the artifact root", decision)
	}
}

func TestAgyVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		actual, min string
		want        bool
	}{
		{"1.1.15", "1.1.15", true},
		{"1.1.16", "1.1.15", true},
		{"1.2.0", "1.1.15", true},
		{"2.0.0", "1.1.15", true},
		{"1.1.14", "1.1.15", false},
		{"1.0.99", "1.1.15", false},
		{"1.10.0", "1.9.0", true}, // numeric, not lexicographic, comparison
		{"1.1", "1.1.15", false},  // shorter actual treated as zero-padded
		{"1.1.15.2", "1.1.15", true},
	} {
		got, err := agyVersionAtLeast(tc.actual, tc.min)
		if err != nil {
			t.Fatalf("agyVersionAtLeast(%q, %q) unexpected error: %v", tc.actual, tc.min, err)
		}
		if got != tc.want {
			t.Errorf("agyVersionAtLeast(%q, %q) = %v, want %v", tc.actual, tc.min, got, tc.want)
		}
	}
}

func TestAgyVersionAtLeastRejectsUnparseableVersions(t *testing.T) {
	for _, actual := range []string{"", "unknown-build", "v1.1.15-beta", "1.1.x"} {
		if _, err := agyVersionAtLeast(actual, minSupportedAgyVersion); err == nil {
			t.Errorf("agyVersionAtLeast(%q, ...) = nil error, want a parse error for an unparseable version", actual)
		}
	}
}

func TestEnsureNativeWriteHookPreservesExistingFileMode(t *testing.T) {
	home := t.TempDir()
	path := hooksConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const restrictive = 0o600
	if err := os.WriteFile(path, []byte(`{"user-hook":{"PostToolUse":[]}}`), restrictive); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeWriteHook(home); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != restrictive {
		t.Fatalf("hooks.json mode = %o, want preserved %o (ensureNativeWriteHook must not widen an existing file's permissions)", got, restrictive)
	}
}

func TestEnsureNativeWriteHookConcurrentProvisioningLosesNoUpdates(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o755); err != nil {
		t.Fatal(err)
	}

	const otherWriters = 8
	var wg sync.WaitGroup
	errs := make(chan error, otherWriters+4)

	// Simulate other concurrent writers (other Tractor processes / tools)
	// each adding their own distinct top-level key via the same lock +
	// atomic-rename discipline ensureNativeWriteHook uses, racing against
	// several concurrent ensureNativeWriteHook calls.
	addForeignKey := func(name string) error {
		unlock, err := lockHooksConfig(home)
		if err != nil {
			return err
		}
		defer unlock()
		path := hooksConfigPath(home)
		doc := map[string]json.RawMessage{}
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &doc); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		doc[name] = json.RawMessage(`{"PostToolUse":[]}`)
		encoded, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return writeFileAtomic(path, append(encoded, '\n'), 0o644)
	}

	for i := range otherWriters {
		wg.Add(1)
		name := fmt.Sprintf("other-hook-%d", i)
		go func() {
			defer wg.Done()
			if err := addForeignKey(name); err != nil {
				errs <- err
			}
		}()
	}
	for range 4 {
		wg.Go(func() {
			if err := ensureNativeWriteHook(home); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent provisioning error: %v", err)
	}

	raw, err := os.ReadFile(hooksConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("hooks.json is not valid JSON after concurrent writers: %v (%s)", err, raw)
	}
	if _, ok := doc[tractorHookName]; !ok {
		t.Fatalf("tractor hook missing after concurrent provisioning: %s", raw)
	}
	for i := range otherWriters {
		name := fmt.Sprintf("other-hook-%d", i)
		if _, ok := doc[name]; !ok {
			t.Fatalf("concurrent writer's key %q was lost (read-modify-write race): %s", name, raw)
		}
	}
}
