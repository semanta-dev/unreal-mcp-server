// Package renderhealth implements the render_health Layer-A audit from
// AGENTIC_GAMEDEV_PLAN.md §7.1 / RC7, checking packaged boot config and captured
// static render output for ship-config-blind failures.
package renderhealth

import "github.com/jdziat/unreal-mcp-server/internal/gametrace"

const (
	reasonNoGameDefaultMap      = "GameDefaultMap is empty"
	reasonGameDefaultMapNotGame = "GameDefaultMap is not a configured game map"
	reasonEmptyTimeline         = "timeline is empty"
	reasonNoStaticRendered      = "timeline has no static render output"
)

// Report is the render health audit result.
type Report struct {
	Pass           bool
	BootsToGame    bool
	StaticRendered bool
	Reasons        []string
}

// RenderHealth checks that the packaged build boots into a game map and that the
// capture timeline contains at least one frame with static geometry rendered.
func RenderHealth(cfg gametrace.EngineConfig, timeline []gametrace.TimelineSample) Report {
	bootsToGame, bootReason := bootsToGame(cfg)
	staticRendered, staticReason := staticRendered(timeline)

	var reasons []string
	if !bootsToGame {
		reasons = append(reasons, bootReason)
	}
	if !staticRendered {
		reasons = append(reasons, staticReason)
	}

	return Report{
		Pass:           bootsToGame && staticRendered,
		BootsToGame:    bootsToGame,
		StaticRendered: staticRendered,
		Reasons:        reasons,
	}
}

func bootsToGame(cfg gametrace.EngineConfig) (bool, string) {
	if cfg.GameDefaultMap == "" {
		return false, reasonNoGameDefaultMap
	}
	for _, gameMap := range cfg.GameMaps {
		if cfg.GameDefaultMap == gameMap {
			return true, ""
		}
	}
	return false, reasonGameDefaultMapNotGame
}

func staticRendered(timeline []gametrace.TimelineSample) (bool, string) {
	if len(timeline) == 0 {
		return false, reasonEmptyTimeline
	}
	for _, sample := range timeline {
		if sample.Static > 0 {
			return true, ""
		}
	}
	return false, reasonNoStaticRendered
}
