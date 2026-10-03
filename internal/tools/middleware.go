package tools

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
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
	s.AddReceivingMiddleware(RecoverMiddleware(logger), LoggingMiddleware(logger), envelope.SafetyNet(nil))
}
