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
	// clean_slate defaults false (v2), so only an explicit true escalates — and Exec
	// still dominates Destructive for the tier itself.
	if got := recipe.EffectiveTier(map[string]any{}); got != Exec {
		t.Fatalf("recipe base = %v, want exec", got)
	}
	for _, e := range recipe.Escalations {
		if e.Arg == "clean_slate" && (e.Default != false || e.Tier != Destructive) {
			t.Fatalf("clean_slate escalation = %+v, want default false → destructive", e)
		}
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
