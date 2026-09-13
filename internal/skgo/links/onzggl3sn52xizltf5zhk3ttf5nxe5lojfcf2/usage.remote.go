package runid

import (
	"context"
	"errors"
	"net/http"

	"github.com/tylergannon/skgo"

	"github.com/tylergannon/gimble/internal/observation"
)

// ScopeRef names one run and one scope within it. A remote function's
// argument is how the page says what it is watching: a query may not read
// the page's own URL, so the run id crosses in the argument.
type ScopeRef struct {
	RunID string `json:"runID"`
	Scope string `json:"scope"`
}

// scopeUsage streams the token usage of one scope: the sum of the latest
// running total of every session in that scope or nested under it. The root
// scope "" is the whole run.
//
// It yields what the run has spent so far at once, and again whenever an
// event changes it, until the run finishes or the browser disconnects. A run
// that has already finished is answered once from its checkpoint: there is
// nothing further to stream, and the value is final.
func scopeUsage(ctx context.Context, arg ScopeRef, yield func(observation.Usage) error) error {
	registry := observation.FromContext(ctx)
	if registry == nil {
		// The registry rides on the request context the runtime supplies
		// through BaseContext, which is the context a live call's own
		// context derives from; this is the same reach the page's load makes.
		if request := skgo.EventFrom(ctx).Request(); request != nil {
			registry = observation.FromContext(request.Context())
		}
	}
	if registry == nil {
		return skgo.Errorf(http.StatusInternalServerError,
			"This server has no observation registry in its context, so no run can be read.")
	}

	store, live := registry.Live(arg.RunID)
	if !live {
		return yieldFinal(registry, arg, yield)
	}

	snapshot, sub, err := store.Subscribe()
	if errors.Is(err, observation.ErrClosed) {
		// The run finished between the lookup and the subscription. Its
		// snapshot is the final one, so the answer is the same as a run that
		// had already finished.
		return yield(snapshot.Run.ScopeUsage(arg.Scope))
	}
	if err != nil {
		return err
	}
	defer sub.Close()

	current := snapshot.Run.ScopeUsage(arg.Scope)
	if err := yield(current); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Done():
			// The subscription stopped for good -- it overflowed, or its
			// reader was released. There is no suffix left to read.
			return sub.Err()
		case frame, ok := <-sub.Frames():
			if !ok {
				// The run ended and its queued suffix is drained: the last
				// value yielded is final.
				return nil
			}
			sub.Took(frame)
			next := store.ScopeUsage(arg.Scope)
			if next == current {
				continue
			}
			current = next
			if err := yield(current); err != nil {
				return err
			}
		}
	}
}

// yieldFinal answers for a run that is not in the live registry, from the
// checkpoint the registry reads for it.
func yieldFinal(registry *observation.Registry, arg ScopeRef, yield func(observation.Usage) error) error {
	snapshot, err := registry.Snapshot(arg.RunID)
	if errors.Is(err, observation.ErrNoRun) {
		return skgo.Errorf(http.StatusNotFound, "There is no run %s in this project.", arg.RunID)
	}
	if err != nil {
		return err
	}
	return yield(snapshot.Run.ScopeUsage(arg.Scope))
}

var _ = skgo.LiveQuery(scopeUsage)
