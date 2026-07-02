package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/config"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// relaunchWatcher relaunches the editor when it disappears from discovery for a
// sustained window (unattended crash recovery). Opt-in via UMCP_AUTO_RELAUNCH;
// requires -project (to find the .uproject) and -engine. Uses a cooldown so a
// launch that is still coming up is not relaunched again.
func relaunchWatcher(ctx context.Context, sess *uexec.Session, cfg config.Config, logger *slog.Logger) {
	uproject := lifecycle.FindUproject(cfg.ProjectDir)
	if uproject == "" {
		logger.Warn("auto-relaunch enabled but no .uproject found under -project; disabling", "project", cfg.ProjectDir)
		return
	}
	logger.Info("auto-relaunch armed", "uproject", uproject, "engine", cfg.EngineDir)

	const (
		poll     = 10 * time.Second
		goneFor  = 30 * time.Second // sustained absence before relaunch
		cooldown = 90 * time.Second // after a relaunch, don't relaunch again for a while
	)
	var absentSince, lastLaunch time.Time
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if len(sess.Nodes()) > 0 {
				absentSince = time.Time{}
				continue
			}
			if absentSince.IsZero() {
				absentSince = now
				continue
			}
			if now.Sub(absentSince) < goneFor || now.Sub(lastLaunch) < cooldown {
				continue
			}
			logger.Warn("editor absent from discovery; auto-relaunching", "absent_for", now.Sub(absentSince).String())
			pid, err := lifecycle.Launch(cfg.EngineDir, uproject, "-nosplash")
			if err != nil {
				logger.Error("auto-relaunch failed", "err", err)
			} else {
				logger.Info("editor relaunched", "pid", pid)
			}
			lastLaunch = now
			absentSince = time.Time{}
		}
	}
}
