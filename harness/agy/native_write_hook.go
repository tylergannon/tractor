package agy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// minSupportedAgyVersion is the oldest agy release harness/agy has
// verified live to honor the tractor-no-native-write PreToolUse hook (see
// verifyAgyHookSupport in adapter.go). Only raise it alongside fresh live
// evidence; only lower it after re-verifying hook support on the older
// version.
const minSupportedAgyVersion = "1.1.15"

// agyVersionAtLeast reports whether actual (agy's raw `--version` stdout,
// e.g. "1.1.15") is >= min, comparing dotted numeric segments left to
// right. Returns an error if actual doesn't parse as a dotted-numeric
// version at all — callers should treat that as "support unconfirmed",
// not "supported".
func agyVersionAtLeast(actual, min string) (bool, error) {
	actualParts, err := parseDottedVersion(actual)
	if err != nil {
		return false, err
	}
	minParts, err := parseDottedVersion(min)
	if err != nil {
		return false, err
	}
	for i := range minParts {
		var a int
		if i < len(actualParts) {
			a = actualParts[i]
		}
		if a != minParts[i] {
			return a > minParts[i], nil
		}
	}
	return true, nil
}

func parseDottedVersion(raw string) ([]int, error) {
	fields := strings.Split(strings.TrimSpace(raw), ".")
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty version string")
	}
	parts := make([]int, len(fields))
	for i, field := range fields {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return nil, fmt.Errorf("version segment %q is not numeric: %w", field, err)
		}
		parts[i] = value
	}
	return parts, nil
}

// nativeWriteTools are agy's native tools capable of mutating files
// in-process. All three accept a TargetFile argument (verified empirically
// against real agy 1.1.15 for write_to_file and replace_file_content by
// inspecting the PreToolUse hook's own stdin payload; multi_replace_file_content
// is grouped with write_to_file in Google's Hooks tool reference as the other
// tool able to carry ArtifactMetadata and is assumed, not independently
// confirmed, to use the same TargetFile key — see
// ephemeral/worklog/202608191900-agy-artifact-prevention.md).
//
// A call to any of the three that carries an ArtifactMetadata argument and
// whose target lies outside agy's private per-conversation
// ~/.gemini/antigravity-cli/brain/<uuid>/ directory fails the whole turn
// with "is not a valid artifact path" — see isArtifactPathError in
// adapter.go for the reactive fallback that recovers from that failure.
// nativeWriteHookSpec below is the preventive layer: a PreToolUse hook that
// denies the call before agy ever reaches that failure.
//
// The hook is artifact-aware, not a blanket path-based deny: live evidence
// (ephemeral/worklog/202608191900-agy-artifact-prevention.md, "The failure
// mechanism is ArtifactMetadata, not path") shows the failure is gated on
// the tool call's own ArtifactMetadata argument, not merely on the target's
// location. An identical write_to_file call to the identical out-of-workspace
// path failed when it carried ArtifactMetadata and succeeded when it did
// not. So the hook denies only a call that (a) carries ArtifactMetadata and
// (b) targets a path outside the artifact directory — the one combination
// that reproduces the bug — and allows every other native write: plain
// workspace writes (no ArtifactMetadata, any target) and genuine artifact
// writes (ArtifactMetadata, target inside the artifact directory) are both
// legal and both let through. A blanket deny, or a path-only deny, would
// break legitimate native writes for any agy session, Tractor-launched or a
// user's own interactive session, sharing this global hook.
var nativeWriteTools = []string{
	"write_to_file",
	"replace_file_content",
	"multi_replace_file_content",
}

// tractorHookName is the top-level key Tractor owns inside agy's global
// hooks.json. ensureNativeWriteHook only ever reads or writes this one
// key, leaving any other hooks the user or another tool has configured
// alone.
const tractorHookName = "tractor-no-native-write"

// nativeWriteHookMarker is embedded in the hook's deny reason. It lets
// ensureNativeWriteHook tell a stale/older Tractor-authored entry (safe to
// overwrite with the current content) apart from a same-named hook a user
// configured independently (refuse to clobber), and lets isArtifactPathError
// in adapter.go recognize the hook's own denial as the same recoverable
// failure class as agy's native artifact-path error.
const nativeWriteHookMarker = "[tractor-no-native-write]"

func hooksConfigPath(homeDir string) string {
	return filepath.Join(homeDir, ".gemini", "config", "hooks.json")
}

func hooksLockPath(homeDir string) string {
	return filepath.Join(homeDir, ".gemini", "config", ".tractor-hooks.lock")
}

type hookHandler struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookSpec struct {
	PreToolUse []hookGroup `json:"PreToolUse"`
}

// nativeWriteHookCommand is the shell command agy invokes for a matched
// PreToolUse call. It reads the hook's JSON payload from stdin and prints
// an allow/deny decision:
//
//   - allow, when the call carries no ArtifactMetadata argument (an
//     ordinary workspace write, wherever it targets — agy's own validator
//     only ever rejects a call that both carries ArtifactMetadata and
//     targets outside the artifact directory) or when every extracted
//     TargetFile lies inside the artifact directory (a genuine conversation
//     artifact write, which agy's own validator accepts anyway);
//   - deny (with a Tractor-authored, marker-tagged reason), when the call
//     carries ArtifactMetadata and any target lies outside the artifact
//     directory, or the payload doesn't parse as expected — the one
//     combination empirically confirmed to reproduce the bug, plus the
//     fail-closed default for a payload shape the script doesn't recognize.
//
// The parse is a plain-text extraction (grep/sed/case), not a JSON library,
// to keep the command a single portable POSIX-sh script with no external
// interpreter dependency (no jq/python assumed to be on the invoking
// machine). Deny-on-parse-failure is the safe default: it can never
// wrongly allow a write agy would reject, only wrongly deny one it would
// have accepted — recoverable by the existing repair-retry either way.
//
// Payload shape and the ArtifactMetadata mechanism verified empirically
// against real agy 1.1.15 (captured via a debug hook that logged its own
// stdin; see ephemeral/worklog/202608191900-agy-artifact-prevention.md,
// "The failure mechanism is ArtifactMetadata, not path"): the identical
// write_to_file call to the identical out-of-workspace path failed when
// its args included an ArtifactMetadata object and succeeded, with no
// error, when the model omitted that argument on retry:
//
//	{"artifactDirectoryPath":"/.../brain/<uuid>","toolCall":{"name":"write_to_file","args":{"ArtifactMetadata":{...},"TargetFile":"...", ...}}, ...}
func nativeWriteHookCommand() string {
	denyReason := nativeWriteHookMarker + " native write declared as a conversation artifact (an ArtifactMetadata argument) targets a path outside the conversation's private artifact directory: agy fails the whole turn in that combination, even though the physical write already succeeds. Retry the SAME write_to_file/replace_file_content/multi_replace_file_content call, unchanged, but omit the ArtifactMetadata argument entirely — this is an ordinary workspace file, not a conversation artifact."
	denyJSON := mustCompactJSON(map[string]string{"decision": "deny", "reason": denyReason})
	allowJSON := mustCompactJSON(map[string]string{"decision": "allow"})

	// shSingleQuote below closes and reopens the surrounding sh single
	// quotes around any literal "'" byte, since denyReason (unlike round
	// 1/2's text) now contains one ("conversation's") and POSIX sh has no
	// escape sequence inside a single-quoted string.
	script := `input="$(cat)"
deny() { printf '%s\n' '` + shSingleQuote(denyJSON) + `'; }
allow() { printf '%s\n' '` + shSingleQuote(allowJSON) + `'; }
case "$input" in
  *'"ArtifactMetadata":'*) ;;
  *) allow; exit 0 ;;
esac
root=$(printf '%s' "$input" | sed -n 's/.*"artifactDirectoryPath":"\([^"]*\)".*/\1/p' | head -n1)
if [ -z "$root" ]; then deny; exit 0; fi
targets=$(printf '%s' "$input" | grep -o '"TargetFile":"[^"]*"' | sed 's/^"TargetFile":"//; s/"$//')
if [ -z "$targets" ]; then deny; exit 0; fi
oldifs=$IFS
IFS='
'
for t in $targets; do
  case "$t" in
    "$root"/*) ;;
    *) deny; exit 0 ;;
  esac
done
IFS=$oldifs
allow
`
	return script
}

// shSingleQuote escapes s for embedding inside an *already-open* POSIX sh
// single-quoted string literal (i.e. the caller supplies the enclosing `'`
// characters; this only handles literal `'` bytes inside s). POSIX sh has
// no escape sequence within single quotes, so each embedded `'` must close
// the quote, emit an escaped literal quote, and reopen it: `'\”`.
func shSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

func mustCompactJSON(value map[string]string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(fmt.Sprintf("agy: encode native-write hook JSON: %v", err))
	}
	return strings.TrimSpace(buf.String())
}

// nativeWriteHookSpec builds the PreToolUse hook entry that intercepts
// nativeWriteTools before agy executes them. See nativeWriteHookCommand for
// the allow/deny logic. Verified live against agy 1.1.15: a call targeting
// a path inside the conversation's real artifactDirectoryPath is allowed
// and the physical write lands there; a call targeting a workspace path is
// denied before it ever reaches the filesystem.
func nativeWriteHookSpec() hookSpec {
	return hookSpec{
		PreToolUse: []hookGroup{
			{
				Matcher: strings.Join(nativeWriteTools, "|"),
				Hooks:   []hookHandler{{Command: nativeWriteHookCommand(), Timeout: 5}},
			},
		},
	}
}

// ensureNativeWriteHook idempotently provisions Tractor's PreToolUse
// allow/deny hook under homeDir's global agy hooks.json, merging with (not
// replacing) any other hooks already configured there.
//
// Global placement is deliberate, unlike a workspace-local
// .agents/hooks.json: Tractor's isolated-worktree branches (engine/artifacts.go,
// added in 2485e4c) each get their own workdir, so a workspace-local hook
// would need provisioning into and cleanup from every branch worktree, and
// a leftover file would show up in that branch's git status and risk being
// swept into artifact collection — exactly what this fix must not do. A
// single global hooks.json, provisioned once, avoids both problems. It also
// affects the user's own manual interactive agy sessions sharing the same
// global config; because the policy is artifact-aware (allows genuine
// in-brain artifact writes, only denies workspace-targeted ones), it does
// not break that usage the way an unconditional deny would.
//
// The read-modify-write is guarded by an exclusive flock on a sibling lock
// file, so two concurrent Tractor processes (both of which take this lock)
// cannot lose each other's keys, and the write itself lands via a
// same-directory temp file plus rename, so no reader ever observes a
// partially-written file, from any writer, cooperating or not. See
// lockHooksConfig for the precise boundary of what the lock does and does
// not protect against.
func ensureNativeWriteHook(homeDir string) error {
	dir := filepath.Join(homeDir, ".gemini", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	unlock, err := lockHooksConfig(homeDir)
	if err != nil {
		return err
	}
	defer unlock()

	path := hooksConfigPath(homeDir)
	canonical, err := json.Marshal(nativeWriteHookSpec())
	if err != nil {
		return fmt.Errorf("encode tractor native-write hook: %w", err)
	}

	raw, readErr := os.ReadFile(path)
	doc := map[string]json.RawMessage{}
	switch {
	case readErr == nil:
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parse %s: %w (fix or remove the file so Tractor can add its hook)", path, err)
		}
	case os.IsNotExist(readErr):
		// doc stays empty; created below.
	default:
		return fmt.Errorf("read %s: %w", path, readErr)
	}

	if existing, ok := doc[tractorHookName]; ok {
		if bytes.Equal(bytes.TrimSpace(existing), canonical) {
			return nil
		}
		if !strings.Contains(string(existing), nativeWriteHookMarker) {
			return fmt.Errorf(
				"%s already has a %q hook that is not Tractor-managed (missing marker comment in its reason text); "+
					"remove it or rename Tractor's hook (harness/agy.tractorHookName) to avoid clobbering it",
				path, tractorHookName,
			)
		}
		// A stale Tractor-authored entry (e.g. an older reason string);
		// fall through and overwrite it with current content.
	}

	doc[tractorHookName] = canonical
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	// Preserve an existing file's permission bits rather than forcing 0o644:
	// a user (or another tool) may have deliberately set hooks.json to a
	// more restrictive mode (e.g. 0o600), and Tractor merging its own hook
	// into the document is not a reason to widen that. Only a brand-new
	// file gets the 0o644 default.
	perm := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		perm = info.Mode().Perm()
	}
	return writeFileAtomic(path, append(encoded, '\n'), perm)
}

// lockHooksConfig acquires an exclusive interprocess lock over homeDir's
// hooks.json so two *cooperating* writers — two Tractor runs, or any other
// tool that takes this same lock file before reading and writing — can't
// both read the same old document and have the second writer silently
// discard the first writer's new keys. This is an advisory lock: it only
// binds callers that choose to take it. It does not, and cannot, stop a
// person hand-editing hooks.json in a text editor, or another program that
// writes the file directly without acquiring homeDir's
// .tractor-hooks.lock — such a writer can still race ensureNativeWriteHook's
// read-modify-write and have either side's update silently lost to the
// other's atomic rename. Returns an unlock function; the caller must always
// call it (e.g. via defer).
func lockHooksConfig(homeDir string) (func(), error) {
	path := hooksLockPath(homeDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}

// writeFileAtomic writes data to path via a same-directory temp file plus
// rename, so a reader (or a process crash mid-write) never observes a
// truncated or partially-written hooks.json. Rename within the same
// directory is atomic on the POSIX filesystems agy's config lives on.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }() // no-op once renamed away

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write %s: %w", tempName, err)
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod %s: %w", tempName, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tempName, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tempName, path, err)
	}
	return nil
}
