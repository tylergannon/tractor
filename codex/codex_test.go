package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloseIsIdempotentForAnUnknownSession covers acceptance item 4: Close
// on an id the adapter never registered (no session, no connection) returns
// nil both times, and needs no daemon: the adapter's session map does not
// contain the id, so Close returns before it ever touches the connection.
func TestCloseIsIdempotentForAnUnknownSession(t *testing.T) {
	ad := New().(*adapter)
	if err := ad.Close(context.Background(), "unknown-thread"); err != nil {
		t.Fatalf("first Close = %v, want nil", err)
	}
	if err := ad.Close(context.Background(), "unknown-thread"); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// TestCloseOnADeadConnectionDropsLocalRoutingState: when the shared
// connection's reader has already failed, Close must still release
// everything the adapter holds locally (the session entry and the thread's
// routing channel on that connection) before it tries to reach the daemon.
// The cleanup context here is already cancelled, so the redial cannot
// succeed: Close must then report the archive it could not do rather than
// return nil, and the local state must be gone regardless.
func TestCloseOnADeadConnectionDropsLocalRoutingState(t *testing.T) {
	ad := New().(*adapter)
	conn := &connection{threads: make(map[string]chan rpcMessage), readDone: make(chan struct{})}
	conn.registerThread("thread-1")
	close(conn.readDone) // the reader has failed: conn.dead() is true
	ad.sharedConn = conn
	ad.sessions["thread-1"] = &session{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ad.Close(ctx, "thread-1"); err == nil {
		t.Fatal("Close = nil, want an error: the archive could not run on a cancelled cleanup context")
	}
	if n := len(ad.sessions); n != 0 {
		t.Fatalf("sessions after Close = %d, want 0", n)
	}
	conn.threadsMu.Lock()
	n := len(conn.threads)
	conn.threadsMu.Unlock()
	if n != 0 {
		t.Fatalf("connection threads after Close = %d, want 0", n)
	}
}

// TestCloseNeverStartsAStoppedDaemon: Close reaches the daemon through the
// redial path, and that path can start a daemon that is not running. Close
// must not: a stopped daemon holds nothing for the thread, and starting one
// from scope cleanup violates the shared-daemon contract. A fake `codex` on
// PATH reports the daemon stopped and records every invocation; Close must
// return nil, drop local state, and never run `daemon start`.
func TestCloseNeverStartsAStoppedDaemon(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + calls + "\n" +
		"case \"$*\" in *'daemon version'*) echo '{\"status\":\"stopped\",\"socketPath\":\"" + filepath.Join(dir, "none.sock") + "\"}';; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ad := New().(*adapter)
	conn := &connection{threads: make(map[string]chan rpcMessage), readDone: make(chan struct{})}
	conn.registerThread("thread-1")
	close(conn.readDone)
	ad.sharedConn = conn
	ad.sessions["thread-1"] = &session{}

	if err := ad.Close(context.Background(), "thread-1"); err != nil {
		t.Fatalf("Close = %v, want nil: a stopped daemon holds nothing to release", err)
	}
	if n := len(ad.sessions); n != 0 {
		t.Fatalf("sessions after Close = %d, want 0", n)
	}
	recorded, _ := os.ReadFile(calls)
	if !strings.Contains(string(recorded), "daemon version") {
		t.Fatalf("Close never asked the daemon's status; fake codex saw:\n%s", recorded)
	}
	if strings.Contains(string(recorded), "daemon start") {
		t.Fatalf("Close started the daemon; fake codex saw:\n%s", recorded)
	}
}
