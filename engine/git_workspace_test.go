package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestFreezeGitWorkspaceRejectsNonGitDirectory(t *testing.T) {
	sandbox := t.TempDir()
	nonGit := filepath.Join(sandbox, "not-a-repository")
	if err := os.Mkdir(nonGit, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", sandbox)
	_, err := freezeGitWorkspace(nonGit)
	if err == nil || !strings.Contains(err.Error(), "requires a git worktree") {
		t.Fatalf("freezeGitWorkspace error = %v", err)
	}
}

func TestGitWorkspaceSnapshotFanoutInventoryAndCleanup(t *testing.T) {
	repo := newGitTestRepository(t)
	logsRoot := t.TempDir()
	t.Setenv("GIT_TRACE", "1")
	hooksDir := t.TempDir()
	writeTestFile(t, hooksDir, "post-checkout", "#!/bin/sh\nexit 97\n")
	if err := os.Chmod(filepath.Join(hooksDir, "post-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "config", "core.hooksPath", hooksDir)

	writeTestFile(t, repo, "staged.txt", "staged snapshot\n")
	gitTest(t, repo, "add", "staged.txt")
	writeTestFile(t, repo, "unstaged.txt", "unstaged snapshot\n")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, repo, "untracked.txt", "untracked snapshot\n")
	writeTestFile(t, repo, "ignored.txt", "must not be captured\n")

	before := captureMainWorkspace(t, repo)
	snapshot, err := freezeGitWorkspace(filepath.Join(repo, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.repoRoot != resolvedRepo {
		t.Fatalf("repo root = %q, want %q", snapshot.repoRoot, resolvedRepo)
	}
	if snapshot.workdirRel != "nested" {
		t.Fatalf("workspace suffix = %q, want nested", snapshot.workdirRel)
	}
	if parent := gitTest(t, repo, "rev-parse", snapshot.commit+"^"); parent != before.head {
		t.Fatalf("snapshot parent = %q, want HEAD %q", parent, before.head)
	}
	second, err := freezeGitWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	if second.commit != snapshot.commit {
		t.Fatalf("snapshot commit is not deterministic: %q != %q", second.commit, snapshot.commit)
	}
	assertMainWorkspaceUnchanged(t, repo, before)

	branches, err := createBranchWorktrees(snapshot, logsRoot, []string{"branch-a", "branch-b", "branch-c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 3 {
		t.Fatalf("created %d worktrees, want 3", len(branches))
	}
	wantFiles := map[string]string{
		".gitignore":    "ignored.txt\n",
		"committed.txt": "committed\n",
		"staged.txt":    "staged snapshot\n",
		"unstaged.txt":  "unstaged snapshot\n",
		"untracked.txt": "untracked snapshot\n",
	}
	var fanoutRoot string
	for _, branch := range branches {
		if branch.BranchID == "" {
			t.Fatal("empty branch ID")
		}
		if fanoutRoot == "" {
			fanoutRoot = filepath.Dir(branch.Path)
		} else if filepath.Dir(branch.Path) != fanoutRoot {
			t.Fatalf("worktree %q is outside fan-out directory %q", branch.Path, fanoutRoot)
		}
		if got := snapshotTestFiles(t, branch.Path); !reflect.DeepEqual(got, wantFiles) {
			t.Fatalf("files in %s = %#v, want %#v", branch.BranchID, got, wantFiles)
		}
		if branch.Workdir != filepath.Join(branch.Path, "nested") {
			t.Fatalf("branch workdir = %q, want nested path under %q", branch.Workdir, branch.Path)
		}
		if got := gitTest(t, branch.Path, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
			t.Fatalf("worktree %s is dirty: %s", branch.BranchID, got)
		}
		if got := gitTest(t, branch.Path, "rev-parse", "HEAD"); got != snapshot.commit {
			t.Fatalf("worktree %s HEAD = %q, want %q", branch.BranchID, got, snapshot.commit)
		}
		gitTestFails(t, branch.Path, "symbolic-ref", "-q", "HEAD")
	}
	assertMainWorkspaceUnchanged(t, repo, before)

	inventoryBeforeCleanup := readInventoryLines(t, logsRoot)
	if len(inventoryBeforeCleanup) != len(branches) {
		t.Fatalf("inventory entries = %d, want %d", len(inventoryBeforeCleanup), len(branches))
	}
	for index, entry := range inventoryBeforeCleanup {
		if entry.Path != branches[index].Path || entry.BranchID != branches[index].BranchID {
			t.Fatalf("inventory[%d] = %#v, want %#v", index, entry, branches[index])
		}
		if entry.TS.IsZero() || time.Since(entry.TS) > time.Minute {
			t.Fatalf("inventory[%d] timestamp = %v", index, entry.TS)
		}
	}
	if err := os.RemoveAll(branches[0].Path); err != nil {
		t.Fatal(err)
	}

	if err := cleanupBranchWorktrees(repo, logsRoot); err != nil {
		t.Fatal(err)
	}
	if err := cleanupBranchWorktrees(repo, logsRoot); err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
	for _, branch := range branches {
		if _, err := os.Stat(branch.Path); !os.IsNotExist(err) {
			t.Fatalf("worktree %q still exists: %v", branch.Path, err)
		}
	}
	if _, err := os.Stat(fanoutRoot); !os.IsNotExist(err) {
		t.Fatalf("fan-out root %q still exists: %v", fanoutRoot, err)
	}
	if got := gitTest(t, repo, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatalf("worktree list after cleanup:\n%s", got)
	}
	if inventoryAfterCleanup := readInventoryLines(t, logsRoot); !reflect.DeepEqual(inventoryAfterCleanup, inventoryBeforeCleanup) {
		t.Fatalf("cleanup rewrote append-only inventory: %#v != %#v", inventoryAfterCleanup, inventoryBeforeCleanup)
	}
	assertMainWorkspaceUnchanged(t, repo, before)
}

func TestGitOutputStopCancelsCommand(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	script := "#!/bin/sh\n/usr/bin/touch \"$TRACTOR_TEST_MARKER\"\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("TRACTOR_TEST_MARKER", marker)
	stop := NewStopSignal()
	commandDir := t.TempDir()
	finished := make(chan error, 1)
	go func() {
		_, err := gitOutputWithStop(commandDir, nil, nil, stop, "status")
		finished <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake git command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop.Stop()
	select {
	case err := <-finished:
		if !errors.Is(err, errGitStopped) {
			t.Fatalf("cancelled git error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("git command did not stop promptly")
	}
}

func TestStoppedWorktreeCreationLeavesCleanupInventory(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "created")
	script := `#!/bin/sh
previous=""
for argument in "$@"; do
  if [ "$previous" = "--detach" ]; then
    /bin/mkdir -p "$argument"
    /usr/bin/touch "$TRACTOR_TEST_MARKER"
    exec /bin/sleep 30
  fi
  previous="$argument"
done
exit 2
`
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("TRACTOR_TEST_MARKER", marker)
	root := t.TempDir()
	snapshotRepo := t.TempDir()
	stop := NewStopSignal()
	finished := make(chan error, 1)
	go func() {
		_, err := createBranchWorktreesWithStop(gitWorkspaceSnapshot{repoRoot: snapshotRepo, commit: "snapshot"}, root, []string{"left"}, stop)
		finished <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake worktree creation did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop.Stop()
	select {
	case err := <-finished:
		if !errors.Is(err, errGitStopped) {
			t.Fatalf("cancelled worktree error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worktree creation did not stop promptly")
	}
	entries, err := readWorktreeInventory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].BranchID != "left" {
		t.Fatalf("worktree inventory = %#v", entries)
	}
}

type mainWorkspaceState struct {
	head      string
	ref       string
	headBytes []byte
	refBytes  []byte
	index     []byte
	status    string
	files     map[string]string
}

func newGitTestRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitTest(t, repo, "init", "-b", "main")
	gitTest(t, repo, "config", "user.name", "Test User")
	gitTest(t, repo, "config", "user.email", "test@example.com")
	writeTestFile(t, repo, ".gitignore", "ignored.txt\n")
	writeTestFile(t, repo, "committed.txt", "committed\n")
	writeTestFile(t, repo, "staged.txt", "staged base\n")
	writeTestFile(t, repo, "unstaged.txt", "unstaged base\n")
	writeTestFile(t, repo, "deleted.txt", "deleted base\n")
	if err := os.Mkdir(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "-A")
	gitTest(t, repo, "commit", "-m", "initial")
	return repo
}

func captureMainWorkspace(t *testing.T, repo string) mainWorkspaceState {
	t.Helper()
	head := gitTest(t, repo, "rev-parse", "HEAD")
	ref := gitTest(t, repo, "symbolic-ref", "HEAD")
	headPath := resolveGitPath(t, repo, "HEAD")
	refPath := resolveGitPath(t, repo, ref)
	indexPath := gitTest(t, repo, "rev-parse", "--git-path", "index")
	indexPath = absoluteTestPath(repo, indexPath)
	headBytes, err := os.ReadFile(headPath)
	if err != nil {
		t.Fatal(err)
	}
	refBytes, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	return mainWorkspaceState{
		head:      head,
		ref:       ref,
		headBytes: headBytes,
		refBytes:  refBytes,
		index:     index,
		status:    gitTest(t, repo, "status", "--porcelain=v1", "--untracked-files=all", "--ignored"),
		files:     snapshotTestFiles(t, repo),
	}
}

func assertMainWorkspaceUnchanged(t *testing.T, repo string, want mainWorkspaceState) {
	t.Helper()
	got := captureMainWorkspace(t, repo)
	if got.head != want.head || got.ref != want.ref || !bytes.Equal(got.headBytes, want.headBytes) || !bytes.Equal(got.refBytes, want.refBytes) || !bytes.Equal(got.index, want.index) || got.status != want.status || !reflect.DeepEqual(got.files, want.files) {
		t.Fatalf("main workspace changed\ngot:  %#v\nwant: %#v", got, want)
	}
}

func resolveGitPath(t *testing.T, repo, name string) string {
	t.Helper()
	return absoluteTestPath(repo, gitTest(t, repo, "rev-parse", "--git-path", name))
}

func absoluteTestPath(repo, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(repo, path)
}

func snapshotTestFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func readInventoryLines(t *testing.T, logsRoot string) []worktreeInventoryEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(logsRoot, "worktrees.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	entries := make([]worktreeInventoryEntry, 0, len(lines))
	for _, line := range lines {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, []string{"branch_id", "path", "ts"}) {
			t.Fatalf("inventory fields = %v", keys)
		}
		var entry worktreeInventoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = cleanGitEnvironment(os.Environ())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), stderr.String(), err)
	}
	return strings.TrimSpace(stdout.String())
}

func gitTestFails(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = cleanGitEnvironment(os.Environ())
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("git %s unexpectedly succeeded: %s", strings.Join(args, " "), output)
	}
}
