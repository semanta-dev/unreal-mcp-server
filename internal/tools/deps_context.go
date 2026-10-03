package tools

import (
	"context"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/session"
)

// Per-session Deps resolution (MULTI_PROJECT_SYSTEM.md §4). Under the Model-A daemon,
// each MCP session is bound to its own editor lease, so a tool call must resolve its
// Deps (Bridge / ProjectDir / EngineDir / Jobs) from the REQUEST, not from a single
// process-global captured at registration. This is done via the request context: the
// daemon's middleware puts the session's Deps in ctx before the handler runs.
//
// To keep the single-project stdio server BYTE-IDENTICAL, resolution always falls
// back to the value captured at registration when no per-session Deps is in ctx — so
// tools compiled against a captured `d` behave exactly as before until a daemon opts
// them into per-session routing by populating the context.

// resolveDeps returns the per-session Deps if the daemon set one, else the fallback
// captured at registration (single-project stdio path is unchanged).
func resolveDeps(ctx context.Context, fallback Deps) Deps {
	if d, ok := session.From(ctx); ok {
		return d
	}
	return fallback
}

// bridgeFromCtx returns the per-session Bridge if set (and non-nil), else fallback.
// Used by structHandler/textHandler so every dispatch tool routes per-session with
// no call-site change.
func bridgeFromCtx(ctx context.Context, fallback *bridge.Bridge) *bridge.Bridge {
	if d, ok := session.From(ctx); ok && d.Bridge != nil {
		return d.Bridge
	}
	return fallback
}
