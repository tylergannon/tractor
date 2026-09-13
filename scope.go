package gimble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// closeTimeout bounds one adapter's Close call, on a context independent of
// the run's own ctx so a cancelled run still releases everything it holds.
const closeTimeout = 10 * time.Second

// Output is what polytype generates, and what Generate and SetJSON require.
type Output interface {
	Schema() json.RawMessage
	ValidateJSON([]byte) error
}

type scopeKey struct{}
type taskKey struct{}

// scope is one instance of a named span of the workflow. Nothing is in the
// ctx but a pointer to it.
type scope struct {
	run    *run
	parent *scope
	key    string // names with ordinals from the root, as in lap.3/bakeoff.1/attempt.2; "" for the root

	mu       sync.Mutex
	ordinals map[string]int // the last ordinal given to each child scope and session name
	keys     []string       // in the order they were set
	values   map[string][]byte
	sessions []*Session
	ended    bool
}

func current(ctx context.Context) (*scope, error) {
	if s, _ := ctx.Value(scopeKey{}).(*scope); s != nil {
		return s, nil
	}
	return nil, errors.New("gimble: no scope in the ctx; it must come from gimble.Run")
}

// next names the next child of s called name. The caller holds s.mu.
func (s *scope) next(name string) string {
	if s.ordinals == nil {
		s.ordinals = make(map[string]int)
	}
	s.ordinals[name]++
	return path.Join(s.key, fmt.Sprintf("%s.%d", name, s.ordinals[name]))
}

func (s *scope) child(name string) *scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &scope{run: s.run, parent: s, key: s.next(name)}
}

// adopt makes session belong to s, which closes it when s ends.
func (s *scope) adopt(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session.id = s.next(session.name)
	session.closed = s.ended
	s.sessions = append(s.sessions, session)
}

// do runs body as the scope: it begins when body is called and ends when
// body returns.
func (s *scope) do(ctx context.Context, body func(context.Context) error) error {
	ctx, cancel := context.WithCancel(context.WithValue(ctx, scopeKey{}, s))
	e := ScopeBegan{Name: path.Base(s.key)}
	if task, ok := ctx.Value(taskKey{}).(Task); ok {
		e.Task = optionalTask(task)
	}
	s.run.event(s.key, "", "", e)
	defer s.end(cancel)
	err := body(ctx)
	s.run.event(s.key, "", "", ScopeEnded{Error: errString(err)})
	return err
}

// end closes the scope's sessions, then cancels its ctx. Each session is
// marked closed before its adapter is asked to release it, so no Generate
// can enter while cleanup runs. A session with no native id had nothing
// allocated by its adapter and gets no Close call, only its SessionClosed
// event. Close runs on a timeout ctx independent of the run's own, so a
// cancelled run still releases every native session. A Close failure never
// changes the scope's own result (recorded on SessionClosed and folded into
// this run's aggregate close error instead); Run joins that aggregate into
// its returned error once the root scope has ended.
func (s *scope) end(cancel context.CancelFunc) {
	s.mu.Lock()
	s.ended = true
	sessions := s.sessions
	s.mu.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		session.closed = true
		native := session.native
		session.mu.Unlock()
		var closeErr error
		if native != "" {
			closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			closeErr = session.adapter.Close(closeCtx, native)
			cancel()
			if closeErr != nil {
				s.run.recordCloseFailure(session.id, closeErr)
			}
		}
		s.run.event(s.key, session.id, "", SessionClosed{Error: errString(closeErr)})
	}
	cancel()
}

// Scope runs body in a child scope named name, and returns its error. The
// scope ends when body returns: each session it created is closed through
// its adapter's Close, then its ctx is cancelled. A Close failure is
// recorded, not returned here: it never enters this function's error and
// instead surfaces from the run's own aggregate cleanup error.
func Scope(ctx context.Context, name string, body func(ctx context.Context) error) error {
	parent, err := current(ctx)
	if err != nil {
		return err
	}
	return parent.child(name).do(ctx, body)
}

// Set stores a scalar in the ctx's scope. A key is set once per scope
// instance; revision is shadowing, in a child scope.
func Set[V ~string | ~int | ~float64 | ~bool | ~[]string](ctx context.Context, key string, value V) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("gimble: set %q: %w", key, err)
	}
	return store(ctx, key, raw)
}

// SetJSON stores a polytype-generated value in the ctx's scope, once per
// key like Set.
func SetJSON[V Output](ctx context.Context, key string, value V) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("gimble: set %q: %w", key, err)
	}
	return store(ctx, key, raw)
}

func store(ctx context.Context, key string, raw []byte) error {
	s, err := current(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return fmt.Errorf("gimble: set %q in scope %q after it ended", key, s.key)
	}
	if _, ok := s.values[key]; ok {
		return fmt.Errorf("gimble: %q is already set in scope %q", key, s.key)
	}
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = raw
	s.keys = append(s.keys, key)
	s.run.event(s.key, "", "", ValueSet{Key: key, Value: JSONText(raw)})
	return nil
}

// ScopeText renders every value visible from the ctx's scope for a prompt:
// outermost scope first, and for each key the value of the nearest scope
// that set it.
func ScopeText(ctx context.Context) string {
	shown := make(map[string]bool)
	var sections []string // innermost first, reversed below
	for s, _ := ctx.Value(scopeKey{}).(*scope); s != nil; s = s.parent {
		s.mu.Lock()
		for _, key := range slices.Backward(s.keys) {
			if !shown[key] {
				shown[key] = true
				sections = append(sections, "## "+key+"\n\n"+render(s.values[key]))
			}
		}
		s.mu.Unlock()
	}
	slices.Reverse(sections)
	return strings.Join(sections, "\n\n")
}

// localText renders only the values written in this scope. Loop uses it to
// carry a completed task's record forward without promoting those values into
// the parent scope.
func (s *scope) localText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sections := make([]string, 0, len(s.keys))
	for _, key := range s.keys {
		sections = append(sections, "## "+key+"\n\n"+render(s.values[key]))
	}
	return strings.Join(sections, "\n\n")
}

// render shows a JSON string as its text and anything else as indented JSON.
func render(raw []byte) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}
