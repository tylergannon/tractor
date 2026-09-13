package claude

import (
	"context"
	"testing"
)

// TestCloseIsIdempotent covers acceptance item 4: closing a Claude session
// twice returns nil both times. CreateSession only mints an id (Claude
// Code's process is launched per turn, never held by the adapter), so this
// needs no live process either.
func TestCloseIsIdempotent(t *testing.T) {
	ad := New().(*adapter)
	id, err := ad.CreateSession(context.Background(), "model", ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Close(context.Background(), id); err != nil {
		t.Fatalf("first Close = %v, want nil", err)
	}
	if err := ad.Close(context.Background(), id); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}
