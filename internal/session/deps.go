// Package session holds what a single MCP session needs to route tool calls: the
// collaborators (Deps) bound to the session's editor, and the plumbing that makes
// them available per request. Both server topologies use it — stdio binds one Deps
// for the process; the daemon resolves Deps per session from its editor lease
// (docs/archive/MULTI_PROJECT_SYSTEM.md §4).
package session

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
)

// Deps are the collaborators the tools need. Bridge is required; the rest enable
// the build/lifecycle/git/log tools.
type Deps struct {
	Bridge     *bridge.Bridge
	Jobs       *jobs.Registry
	ProjectDir string
	EngineDir  string
	// Restart, when non-nil (daemon mode), performs a CONTROLLED editor restart that
	// PRESERVES this session's lease: it tears down the leased editor, runs buildStep
	// with no editor up (nil for a plain restart), relaunches with the same instance
	// token, and re-pins the lease. In single-project stdio mode it is nil and tools
	// relaunch the editor directly (a fresh process is fine — no lease to keep).
	Restart func(ctx context.Context, buildStep func(context.Context) error) error

	// CockpitURL, when non-nil, returns the current browser-cockpit URL and whether it
	// is live. Nil when the cockpit launcher isn't wired.
	CockpitURL func() (string, bool)

	// Projects, when non-nil (daemon mode), binds sessions to project editors.
	Projects ProjectManager
}

// ProjectManager binds an MCP session to a project's editor lease (daemon mode).
type ProjectManager interface {
	// Attach binds the session to the project's editor (reusing a warm one, adopting
	// a draining lease left by an ended session of the same project, or spawning).
	Attach(ctx context.Context, sessionID, project string) (instance string, err error)
	// Release ends the session's binding (draining while a project job still runs).
	Release(sessionID string)
	// List describes every managed editor instance from this session's viewpoint.
	List(sessionID string) []ProjectInstance
}

// ProjectInstance is one managed editor as seen by a session.
type ProjectInstance struct {
	Instance string `json:"instance"`
	Project  string `json:"project"`
	State    string `json:"state"`
	Leased   bool   `json:"leased"`
	Mine     bool   `json:"mine"`
}

type depsCtxKey struct{}

// WithDeps returns ctx carrying the Deps for the current request.
func WithDeps(ctx context.Context, d Deps) context.Context {
	return context.WithValue(ctx, depsCtxKey{}, d)
}

// From returns the per-request Deps in ctx and whether one was set.
func From(ctx context.Context) (Deps, bool) {
	d, ok := ctx.Value(depsCtxKey{}).(Deps)
	return d, ok
}

// Resolver maps a request to its session's Deps; ok=false when the session is not
// bound to an editor (handlers then fall back to their registration-time Deps).
type Resolver func(ctx context.Context, req mcp.Request) (Deps, bool)

// InstallMiddleware puts the resolved per-session Deps into each request's context
// before the handler runs, so tools route to the session's editor.
func InstallMiddleware(s *mcp.Server, resolve Resolver) {
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if d, ok := resolve(ctx, req); ok {
				ctx = WithDeps(ctx, d)
			}
			return next(ctx, method, req)
		}
	})
}
