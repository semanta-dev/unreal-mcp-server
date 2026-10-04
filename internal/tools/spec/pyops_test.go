package spec

import (
	"reflect"
	"regexp"
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

// TestUndoClassMatchesCompanion: every editing op is classified for the undo journal,
// and the companion's untracked sets are exactly the table's (so a new op cannot slip
// past the journal unnoticed).
func TestUndoClassMatchesCompanion(t *testing.T) {
	valid := map[string]bool{"tx": true, "untracked": true, "untracked_world": true, "pie": true, "none": true}
	for name, p := range PyOps {
		c, ok := UndoClass[name]
		if p.WorstTier() >= Mutating && !ok {
			t.Errorf("%s (%s) has no UndoClass", name, p.WorstTier())
		}
		if ok && !valid[c] {
			t.Errorf("%s: bad UndoClass %q", name, c)
		}
	}
	for name := range UndoClass {
		if _, ok := PyOps[name]; !ok {
			t.Errorf("UndoClass names %s, which is not a companion op", name)
		}
	}
	src := bridge.CompanionSource()
	set := func(name string) []string {
		m := regexp.MustCompile(`(?s)` + name + ` = frozenset\(\((.*?)\)\)`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in the companion", name)
		}
		var out []string
		for _, q := range regexp.MustCompile(`"([a-z_0-9]+)"`).FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
		sort.Strings(out)
		return out
	}
	want := func(class string) []string {
		var out []string
		for n, c := range UndoClass {
			if c == class {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	if got, w := set("_UNTRACKED_EDIT_OPS"), want("untracked"); !reflect.DeepEqual(got, w) {
		t.Errorf("_UNTRACKED_EDIT_OPS = %v, UndoClass untracked = %v", got, w)
	}
	if got, w := set("_UNTRACKED_WORLD_OPS"), want("untracked_world"); !reflect.DeepEqual(got, w) {
		t.Errorf("_UNTRACKED_WORLD_OPS = %v, UndoClass untracked_world = %v", got, w)
	}
}
