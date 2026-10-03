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
	wc := PyOps["widget_compose"]
	if got := wc.EffectiveTier(map[string]any{}); got != Mutating {
		t.Fatalf("widget_compose default = %v, want mutating", got)
	}
	if got := wc.EffectiveTier(map[string]any{"prune": true}); got != Destructive {
		t.Fatalf("widget_compose prune = %v, want destructive", got)
	}
	if got := PyOps["datatable_import"].EffectiveTier(map[string]any{"json": "[]"}); got != Destructive {
		t.Fatalf("datatable_import json = %v, want destructive", got)
	}
	if got := PyOps["asset_create"].EffectiveTier(map[string]any{"replace": true}); got != Destructive {
		t.Fatalf("asset_create replace = %v, want destructive", got)
	}
	if got := PyOps["widget_compose"].WorstTier(); got != Destructive {
		t.Fatalf("widget_compose worst = %v", got)
	}
}
