package session

import (
	"context"
	"sync"

	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
)

// State is one MCP session's lifetime: hooks registered with OnTeardown run once
// when the session ends (client DELETE, idle expiry, sweeper, or stdio exit).
type State struct {
	mu     sync.Mutex
	id     string
	hooks  []func()
	closed bool
}

// NewState returns a live session state.
func NewState(id string) *State { return &State{id: id} }

// ID returns the session ID ("" until bound).
func (s *State) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// Bind sets the session ID (the daemon learns it only at initialize).
func (s *State) Bind(id string) {
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
}

// OnTeardown registers fn to run at teardown. If the session already ended, fn
// runs immediately.
func (s *State) OnTeardown(fn func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		fn()
		return
	}
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// Teardown runs the registered hooks (most recent first) exactly once.
func (s *State) Teardown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	hooks := s.hooks
	s.hooks = nil
	s.mu.Unlock()
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i]()
	}
}

// Closed reports whether Teardown has run.
func (s *State) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type stateCtxKey struct{}

// WithState returns ctx carrying the session state.
func WithState(ctx context.Context, s *State) context.Context {
	return context.WithValue(ctx, stateCtxKey{}, s)
}

// StateFrom returns the session state in ctx, if any.
func StateFrom(ctx context.Context) (*State, bool) {
	s, ok := ctx.Value(stateCtxKey{}).(*State)
	return s, ok
}

// ProjectKey canonicalizes a project path (see lifecycle.ProjectKey).
func ProjectKey(path string) string { return lifecycle.ProjectKey(path) }

// ProjectFile is a project's optional .umcp.json (see lifecycle.ProjectFile).
type ProjectFile = lifecycle.ProjectFile

// ProjectFileName is the per-project config file name.
const ProjectFileName = lifecycle.ProjectFileName

// LoadProjectFile reads <dir>/.umcp.json (see lifecycle.LoadProjectFile).
func LoadProjectFile(dir string) (ProjectFile, error) { return lifecycle.LoadProjectFile(dir) }
