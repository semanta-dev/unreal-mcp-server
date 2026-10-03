package audit

import (
	"os"
	"testing"

)

// The REAL shipped PolyWorld capture records static:0 on every frame (a black render,
// AGENTIC_GAMEDEV_PLAN.md §7.1). Load it via LoadTimeline and assert the
// audit reports StaticRendered==false — the plan's ground-truth Phase-0 falsifier.
func TestRenderHealthPolyWorldTimelineIfPresent(t *testing.T) {
	const path = `C:/Users/jorda/code/games/poly-world/PolyWorld/Saved/PolySlice/slice_timeline.jsonl`
	if _, err := os.Stat(path); err != nil {
		t.Skipf("optional PolyWorld timeline absent: %v", err)
	}
	timeline, err := LoadTimeline(path)
	if err != nil {
		t.Fatalf("LoadTimeline(%q): %v", path, err)
	}
	if len(timeline) == 0 {
		t.Fatalf("loaded empty timeline from %q", path)
	}
	report := RenderHealth(BootToGameConfig(), timeline)
	if report.StaticRendered {
		t.Fatalf("real PolyWorld timeline reported StaticRendered=true; every frame is static:0")
	}
}

func TestRenderHealthFixtures(t *testing.T) {
	t.Run("fails boot to empty and black screen", func(t *testing.T) {
		report := RenderHealth(BootToEmptyConfig(), []TimelineSample{
			{Static: 0},
			{Static: 0},
		})

		if report.Pass {
			t.Fatalf("Pass = true, want false")
		}
		if report.BootsToGame {
			t.Fatalf("BootsToGame = true, want false")
		}
		if report.StaticRendered {
			t.Fatalf("StaticRendered = true, want false")
		}
		if len(report.Reasons) != 2 {
			t.Fatalf("Reasons = %d, want 2: %#v", len(report.Reasons), report.Reasons)
		}
	})

	t.Run("passes boot to game and rendered static", func(t *testing.T) {
		report := RenderHealth(BootToGameConfig(), []TimelineSample{{Static: 42}})

		if !report.Pass {
			t.Fatalf("Pass = false, want true; reasons: %#v", report.Reasons)
		}
		if !report.BootsToGame {
			t.Fatalf("BootsToGame = false, want true")
		}
		if !report.StaticRendered {
			t.Fatalf("StaticRendered = false, want true")
		}
		if len(report.Reasons) != 0 {
			t.Fatalf("Reasons = %#v, want empty", report.Reasons)
		}
	})
}

func TestRenderHealthBootToGameRequiresConfiguredGameMap(t *testing.T) {
	report := RenderHealth(EngineConfig{
		GameDefaultMap: "/Engine/Maps/Templates/OpenWorld",
	}, []TimelineSample{{Static: 1}})

	if report.BootsToGame {
		t.Fatalf("BootsToGame = true, want false for engine map with empty GameMaps")
	}
	if report.Pass {
		t.Fatalf("Pass = true, want false")
	}
}

func TestRenderHealthEmptyTimeline(t *testing.T) {
	report := RenderHealth(BootToGameConfig(), nil)

	if report.StaticRendered {
		t.Fatalf("StaticRendered = true, want false")
	}
	if report.Pass {
		t.Fatalf("Pass = true, want false")
	}
	if !hasReason(report, reasonEmptyTimeline) {
		t.Fatalf("missing empty timeline reason in %#v", report.Reasons)
	}
}

func hasReason(report RenderHealthReport, want string) bool {
	for _, reason := range report.Reasons {
		if reason == want {
			return true
		}
	}
	return false
}
