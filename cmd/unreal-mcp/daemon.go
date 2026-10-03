package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/jdziat/unreal-mcp-server/internal/app"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/config"
	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
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
				dm.SweepIdleSessions(cfg.SessionIdle)
				dm.TickDrains()
				dm.PruneProjectJobs()
				dm.PruneDeadRecords()
			}
		}
	}()

	// A fresh MCP server per HTTP session (per-session toolsets + state). Fallback Deps
	// (engine dir, a jobs registry so build/lifecycle tools register, the project
	// manager); per-session Bridge/ProjectDir/Jobs come from the resolver once the
	// session attaches a project. Session end (DELETE, idle expiry, sweeper) funnels
	// into dm.EndSession's drain rule.
	handler := app.Handler(app.Options{
		Logger:         logger,
		Deps:           session.Deps{EngineDir: cfg.EngineDir, Jobs: jobs.NewRegistry(), Projects: dm},
		Resolver:       dm.DepsResolver(),
		DaemonMode:     true,
		OnSessionStart: dm.RegisterSession,
		OnSessionEnd:   dm.EndSession,
	}, cfg.SessionIdle)
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
