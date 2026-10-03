package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/config"
	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/version"
)

// runDaemon runs the multi-project daemon (MULTI_PROJECT_SYSTEM.md): one process, a
// shared editor pool + discovery, and a StreamableHTTP endpoint where each agent MCP
// session leases its own 1:1 editor. Tool calls resolve their editor per-session via
// the deps middleware. Unlike the stdio server, it does NOT kill sibling processes —
// concurrent editors are the whole point; reattach reconciliation handles orphans.
func runDaemon(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	// Reattach records must survive a daemon crash + restart, so they go in a stable
	// per-user state dir — NOT os.TempDir(), which OS temp cleaners can wipe out from
	// under running editors. Falls back to temp only if no config dir is available.
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	recordsDir := filepath.Join(base, "unreal-mcp-daemon", "records")
	dm, err := daemon.NewDaemon(ctx, cfg.Uexec(), cfg.EngineDir, recordsDir, bridge.SnippetMode(cfg.SnippetMode), logger)
	if err != nil {
		return fmt.Errorf("daemon init: %w", err)
	}

	// §6 reattach BARRIER: before serving any project_attach, reconcile persisted
	// spawn records — kill any daemon-owned editor that outlived a prior (crashed)
	// daemon, so no orphan lingers on the shared discovery. Runs synchronously.
	dm.ReconcileAtStartup()

	// Background: the liveness heartbeat/reaper (also closes discovery on ctx done),
	// and the idle-session lease reclaimer (a generous backstop for vanished agents).
	go dm.Run(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				dm.SweepIdleSessions(30 * time.Minute)
				dm.PruneLeaseJobs()
				dm.PruneDeadRecords()
			}
		}
	}()

	// One shared MCP server; the StreamableHTTP handler creates a ServerSession per
	// HTTP session, and the deps middleware routes each to its lease.
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: version.Version}, &mcp.ServerOptions{Logger: logger})
	tools.InstallMiddleware(srv, logger)
	session.InstallMiddleware(srv, dm.DepsResolver())
	// Fallback Deps (engine dir + a jobs registry so build/lifecycle tools register);
	// per-session Bridge/ProjectDir/Jobs are supplied by the middleware. An unattached
	// session's tool calls get NO_PROJECT_ATTACHED (nil bridge) until project_attach.
	tools.RegisterAll(srv, session.Deps{EngineDir: cfg.EngineDir, Jobs: jobs.NewRegistry()})
	dm.RegisterProjectTools(srv)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	httpSrv := &http.Server{Addr: cfg.DaemonAddr, Handler: handler}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	logger.Info("unreal-mcp daemon listening",
		"version", version.String(), "addr", cfg.DaemonAddr,
		"engine", cfg.EngineDir, "snippet_mode", cfg.SnippetMode)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("daemon http: %w", err)
	}
	return nil
}
