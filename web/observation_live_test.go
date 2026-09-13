package web

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tylergannon/gimble/internal/observation"
)

// usagePayload is the argument kit puts in a live query's URL: devalue's flat
// form, base64url-encoded. Writing it here is what proves the Go function
// answers the call the browser actually makes.
func usagePayload(runID, scope string) string {
	flat, err := json.Marshal([]any{map[string]int{"runID": 1, "scope": 2}, runID, scope})
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(flat)
}

// remoteID is the `<hash>/<name>` the generator published a function under.
func remoteID(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("skgo.remotes.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Remotes []string `json:"remotes"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, id := range manifest.Remotes {
		if strings.HasSuffix(id, "/"+name) {
			return id
		}
	}
	t.Fatalf("skgo.remotes.json publishes no remote function named %s: %v", name, manifest.Remotes)
	return ""
}

func usageEvent(cost float64, input int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"id": "u", "type": "session.usage.updated", "created": 13,
		"data": map[string]any{"sessionID": "ses_writer", "cost": cost,
			"tokens": map[string]any{"input": input, "output": 1, "reasoning": 0,
				"cache": map[string]any{"read": 0, "write": 0}}},
	})
	return raw
}

// TestScopeUsageStreamsAScopesTokens is definition-of-done item 4 at the
// boundary the browser uses: the `query.live` is answered by Go over SSE, it
// opens with what the run has spent so far, it yields again when a session
// reports a new running total, and it ends when the run does.
func TestScopeUsageStreamsAScopesTokens(t *testing.T) {
	dist, err := fs.Sub(Build, "build")
	if err != nil {
		t.Fatal(err)
	}
	handler, _, err := NewHandler(dist, "", "")
	if err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	registry := observation.NewRegistry(project)
	server := httptest.NewUnstartedServer(handler)
	server.Config.BaseContext = func(net.Listener) context.Context {
		return observation.WithRegistry(context.Background(), registry)
	}
	server.Start()
	defer server.Close()

	runDir := filepath.Join(project, "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := observation.Open(registry, "run-1", "demo", runDir)
	store.Lifecycle(observation.Lifecycle{
		Placement: observation.Placement{Scope: "lap.1", Session: "writer"},
		Session:   &observation.SessionInfo{Name: "writer", Adapter: "fixture", Model: "test-model", Scope: "lap.1"},
		Record:    json.RawMessage(`{"event":{"kind":"SessionCreated","name":"writer"}}`),
	})
	at := observation.Placement{Scope: "lap.1", Session: "writer", Turn: "turn-1"}
	if err := store.Event(at, usageEvent(0.125, 4321), nil); err != nil {
		t.Fatal(err)
	}

	// The id is the one skgo generated for this function, read from the
	// manifest it wrote: the browser addresses exactly this URL.
	request, err := http.NewRequest(http.MethodGet, server.URL+"/_app/remote/"+remoteID(t, "scopeUsage")+"?payload="+usagePayload("run-1", ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET the live query: status %d", response.StatusCode)
	}

	frames := make(chan string, 8)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				frames <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	next := func(what string) string {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("the stream ended before %s", what)
			}
			return frame
		case <-time.After(5 * time.Second):
			t.Fatalf("no frame for %s", what)
			return ""
		}
	}

	// The scope opens with what it has spent so far.
	if frame := next("the opening value"); !strings.Contains(frame, "4321") {
		t.Fatalf("the opening frame does not carry the run's tokens: %s", frame)
	}
	// A new running total is a new value. The event is the session's total,
	// so the scope's total is that number and not the sum of the two events.
	if err := store.Event(at, usageEvent(0.25, 8642), nil); err != nil {
		t.Fatal(err)
	}
	frame := next("the updated value")
	if !strings.Contains(frame, "8642") || strings.Contains(frame, "12963") {
		t.Fatalf("the updated frame is not the session's running total: %s", frame)
	}

	// And the stream ends with the run, without the browser polling for it.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-frames:
		if ok {
			t.Fatal("the finished run yielded another value")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end when the run did")
	}
}
