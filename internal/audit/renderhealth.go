// renderhealth (merged into package audit) implements the render_health Layer-A audit from
// AGENTIC_GAMEDEV_PLAN.md §7.1 / RC7, checking packaged boot config and captured
// static render output for ship-config-blind failures.
package audit

const (
	reasonNoGameDefaultMap      = "GameDefaultMap is empty"
	reasonGameDefaultMapNotGame = "GameDefaultMap is not a configured game map"
	reasonEmptyTimeline         = "timeline is empty"
	reasonNoStaticRendered      = "timeline has no static render output"
)

// RenderHealthReport is the render health audit result.
type RenderHealthReport struct {
	Pass           bool
	BootsToGame    bool
	StaticRendered bool
	Reasons        []string
}

// RenderHealth checks that the packaged build boots into a game map and that the
// capture timeline contains at least one frame with static geometry rendered.
func RenderHealth(cfg EngineConfig, timeline []TimelineSample) RenderHealthReport {
	bootsToGame, bootReason := bootsToGame(cfg)
	staticRendered, staticReason := staticRendered(timeline)

	var reasons []string
	if !bootsToGame {
		reasons = append(reasons, bootReason)
	}
	if !staticRendered {
		reasons = append(reasons, staticReason)
	}

	return RenderHealthReport{
		Pass:           bootsToGame && staticRendered,
		BootsToGame:    bootsToGame,
		StaticRendered: staticRendered,
		Reasons:        reasons,
	}
}

func bootsToGame(cfg EngineConfig) (bool, string) {
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

func staticRendered(timeline []TimelineSample) (bool, string) {
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
