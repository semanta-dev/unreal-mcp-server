package tools

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RecoverMiddleware turns a panic in any handler into an error rather than
// letting it crash the (long-lived, unattended) server process.
func RecoverMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("recovered panic in MCP handler",
						"method", method, "panic", r, "stack", string(debug.Stack()))
					err = fmt.Errorf("internal error handling %s", method)
				}
			}()
			return next(ctx, method, req)
		}
	}
}

// LoggingMiddleware logs each method call's duration and error at debug level
// (stderr), keeping the stdout JSON-RPC channel pure.
func LoggingMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			start := time.Now()
			res, err := next(ctx, method, req)
			if err != nil {
				logger.Warn("mcp method error", "method", method, "dur", time.Since(start).String(), "err", err)
			} else {
				logger.Debug("mcp method", "method", method, "dur", time.Since(start).String())
			}
			return res, err
		}
	}
}

// Install wires the standard receiving middleware onto a server. Recover is
// outermost so it also guards the logging middleware. Per-command timeouts live
// in the uexec layer (UMCP_COMMAND_TIMEOUT), so no blanket method timeout is
// imposed here (it would truncate legitimately long ops like builds/screenshots).
func InstallMiddleware(s *mcp.Server, logger *slog.Logger) {
	s.AddReceivingMiddleware(RecoverMiddleware(logger), LoggingMiddleware(logger))
}

// InstallDepsMiddleware installs the per-session Deps resolver (MULTI_PROJECT_SYSTEM.md
// §4): before each handler runs, it resolves the request's session to its editor-lease
// Deps and puts them in ctx (WithDeps) so tools route to the right editor. resolve
// returns (Deps, true) when a session is bound to a lease, else (Deps{}, false) — in
// which case handlers fall back to the value captured at registration (single-project
// stdio is unaffected because it installs no such middleware). The daemon supplies
// resolve (it owns the session→lease→bridge mapping); the tools package stays
// decoupled from the daemon.
func InstallDepsMiddleware(s *mcp.Server, resolve func(ctx context.Context, req mcp.Request) (Deps, bool)) {
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if d, ok := resolve(ctx, req); ok {
				ctx = WithDeps(ctx, d)
			}
			return next(ctx, method, req)
		}
	})
}
