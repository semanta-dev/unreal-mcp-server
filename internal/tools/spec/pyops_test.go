package spec

import (
	"sort"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

// TestPyOpsBijection: every companion op is classified and every classified op
// exists (fail-closed: a new op without a tier fails CI).
func TestPyOpsBijection(t *testing.T) {
	ops := bridge.CompanionOps()
	real := map[string]bool{}
	var missing []string
	for _, op := range ops {
		real[op] = true
		if _, ok := PyOps[op]; !ok {
			missing = append(missing, op)
		}
	}
	var extra []string
	for op := range PyOps {
		if !real[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("pyops drift — unclassified ops: %v; classified non-ops: %v", missing, extra)
	}
}

func TestEffectiveTierUsesDefaults(t *testing.T) {
	recipe := PyOps["apply_level_recipe"]
	// clean_slate defaults true in the op, so an empty arg set already escalates.
	if got := recipe.EffectiveTier(map[string]any{}); got != Exec {
		t.Fatalf("recipe base = %v (exec dominates destructive)", got)
	}
	scene := PyOps["scene_apply"]
	if got := scene.EffectiveTier(map[string]any{}); got != Mutating {
		t.Fatalf("scene_apply default = %v, want mutating", got)
	}
	if got := scene.EffectiveTier(map[string]any{"prune": true}); got != Destructive {
		t.Fatalf("scene_apply prune = %v, want destructive", got)
	}
	if got := PyOps["viewport_set"].EffectiveTier(map[string]any{"console": []any{"stat fps"}}); got != Exec {
		t.Fatalf("viewport_set console = %v, want exec", got)
	}
	if got := PyOps["widget_compose"].WorstTier(); got != Destructive {
		t.Fatalf("widget_compose worst = %v", got)
	}
}
