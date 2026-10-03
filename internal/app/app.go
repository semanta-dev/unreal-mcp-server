// Package app assembles a configured MCP server for one session — the single
// construction path for both topologies (plan §2.6): stdio builds one server for the
// process; the daemon builds a fresh server per new HTTP session (getServer runs
// once per stateful session, before the session ID exists, so the session State is
// bound at initialize and torn down when the session ends).
package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/version"
)

// Options configures server construction.
type Options struct {
	Logger *slog.Logger
	// Deps are the registration/fallback deps (stdio: the bound editor; daemon:
	// engine dir, a fallback jobs registry and the ProjectManager).
	Deps session.Deps
	// Resolver resolves per-request Deps from the session (daemon); nil in stdio.
	Resolver session.Resolver
	// Toolsets are enabled at session start in addition to core.
	Toolsets []spec.Toolset
	// DaemonMode enables the daemon toolset (project_attach/release/list).
	DaemonMode bool
	// Gate is the approval policy (nil = off).
	Gate spec.Gate
	// Catalog builds the tool catalog for a session; defaults to tools.Specs.
	Catalog func(session.Deps) []*spec.Spec
	// OnSessionStart is called once a daemon session is initialized, with a func
	// that closes it (the daemon's sweeper uses it); OnSessionEnd at teardown.
	OnSessionStart func(id string, closeFn func())
	OnSessionEnd   func(id string)
}

// Server is one session's server and its per-session state.
type Server struct {
	MCP      *mcp.Server
	State    *session.State
	Toolsets *spec.Toolsets
}

// NewServer builds a server for one session. st may be nil (a state is created).
func NewServer(o Options, st *session.State) *Server {
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if st == nil {
		st = session.NewState("")
	}
	catalog := o.Catalog
	if catalog == nil {
		catalog = tools.Specs
	}
	sv := &Server{State: st}
	// bind attaches the State to its ServerSession exactly once: at initialized, or on
	// the first request from a client that never sends notifications/initialized.
	var bindOnce sync.Once
	bind := func(ss *mcp.ServerSession) {
		if ss == nil || (o.OnSessionStart == nil && o.OnSessionEnd == nil) {
			return
		}
		bindOnce.Do(func() {
			id := ss.ID()
			st.Bind(id)
			if o.OnSessionEnd != nil {
				st.OnTeardown(func() { o.OnSessionEnd(id) })
			}
			if o.OnSessionStart != nil {
				o.OnSessionStart(id, func() { _ = ss.Close() })
			}
			// ss.Wait returns on DELETE, idle expiry, or Close (the sweeper).
			go func() {
				_ = ss.Wait()
				st.Teardown()
			}()
		})
	}
	initial := o.Toolsets
	if o.DaemonMode {
		initial = append(append([]spec.Toolset(nil), initial...), spec.Daemon)
	}
	sopts := &mcp.ServerOptions{Logger: logger}
	sopts.InitializedHandler = func(ctx context.Context, req *mcp.InitializedRequest) { bind(req.Session) }
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: version.Version}, sopts)
	sv.MCP = srv
	sv.Toolsets = spec.NewToolsets(srv, catalog(o.Deps),
		spec.Options{Logger: logger, Fallback: o.Deps, Gate: o.Gate}, initial...)

	// Receiving middleware, outermost first: panic recovery, logging, per-request
	// context (session state, toolsets, per-session deps), then the envelope net.
	srv.AddReceivingMiddleware(
		tools.RecoverMiddleware(logger),
		tools.LoggingMiddleware(logger),
		func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if ss, ok := req.GetSession().(*mcp.ServerSession); ok && method != "initialize" {
					bind(ss)
				}
				ctx = session.WithState(ctx, st)
				ctx = spec.WithToolsets(ctx, sv.Toolsets)
				if o.Resolver != nil {
					if d, ok := o.Resolver(ctx, req); ok {
						ctx = session.WithDeps(ctx, d)
					}
				}
				return next(ctx, method, req)
			}
		},
		envelope.SafetyNet(sv.Toolsets.Disabled),
	)
	return sv
}

// Handler serves the daemon over StreamableHTTP: a fresh server per new session.
// idle is the SDK SessionTimeout (the fast path for clean idles; the daemon's sweeper
// is the primary liveness backstop because the SDK timer pauses while any stream is
// open).
func Handler(o Options, idle time.Duration) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return NewServer(o, nil).MCP
	}, &mcp.StreamableHTTPOptions{SessionTimeout: idle})
}
