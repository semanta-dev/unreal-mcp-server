package supervisor

import (
	"context"
	"log/slog"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/config"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// RelaunchWatcher relaunches the editor when it disappears from discovery for a
// sustained window (unattended crash recovery). Opt-in via UMCP_AUTO_RELAUNCH;
// requires -project (to find the .uproject) and -engine. Uses a cooldown so a
// launch that is still coming up is not relaunched again.
func RelaunchWatcher(ctx context.Context, sess *uexec.Session, cfg config.Config, logger *slog.Logger) {
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
			var launch bool
			absentSince, launch = relaunchDue(now, absentSince, lastLaunch, len(sess.Nodes()) > 0, goneFor, cooldown)
			if !launch {
				continue
			}
			logger.Warn("editor absent from discovery; auto-relaunching")
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

// relaunchDue is the watcher's decision: relaunch only after the editor has been
// absent from discovery for goneFor, and not within cooldown of the last launch.
// It returns the updated absence start and whether to launch now.
func relaunchDue(now, absentSince, lastLaunch time.Time, present bool, goneFor, cooldown time.Duration) (time.Time, bool) {
	if present {
		return time.Time{}, false
	}
	if absentSince.IsZero() {
		return now, false
	}
	if now.Sub(absentSince) < goneFor || now.Sub(lastLaunch) < cooldown {
		return absentSince, false
	}
	return time.Time{}, true
}
