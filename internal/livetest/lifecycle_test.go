//go:build live

package livetest

import (
	"math"
	"os"
	"testing"
	"time"
)

const tuning = "/Game/Data/DA_AesirTuning"

func (l *live) damageMultiplier() float64 {
	l.t.Helper()
	r := l.call("reflect", map[string]any{"op": "object", "actor": tuning, "properties": []any{"player_damage_multiplier"}})
	p, _ := r["properties"].(map[string]any)
	return num(p["player_damage_multiplier"])
}

// T4 6: a safe restart with nothing unsaved completes; with an unsaved asset it is
// refused naming it; with discard_dirty it completes and the unsaved change is gone.
func TestLiveSafeRestart(t *testing.T) {
	l := session(t, "AESIR")
	l.call("pie", map[string]any{"op": "stop"})
	// A clean start: earlier tests leave the open map changed in memory (a restart
	// refuses that, rightly, naming it).
	l.job(l.call("editor_lifecycle", map[string]any{"op": "restart", "discard_dirty": true, "wait_s": 25}), 10*time.Minute)
	l.job(l.call("editor_lifecycle", map[string]any{"op": "restart", "wait_s": 25}), 10*time.Minute) // nothing unsaved
	l.call("editor", map[string]any{"op": "ping"})

	orig := l.damageMultiplier()
	l.python("a = unreal.load_asset('" + tuning + "')\na.modify()\na.set_editor_property('player_damage_multiplier', " +
		"a.get_editor_property('player_damage_multiplier') + 7.0)") // changed in memory, not saved
	e := l.fail("editor_lifecycle", map[string]any{"op": "restart", "wait_s": 25}, "PRECONDITION")
	if !contains(e, "DA_AesirTuning") {
		t.Fatalf("restart with an unsaved asset = %v (want it named)", e)
	}
	l.job(l.call("editor_lifecycle", map[string]any{"op": "restart", "discard_dirty": true, "wait_s": 25}), 10*time.Minute)
	if got := l.damageMultiplier(); math.Abs(got-orig) > 1e-4 {
		t.Fatalf("after discard_dirty the multiplier is %v, want the saved %v", got, orig)
	}
}

// T4 7: git_revert to the scratch copy's checkpoint undoes a saved asset change (the
// editor relaunched when it had the asset loaded).
func TestLiveGitRevert(t *testing.T) {
	l := session(t, "AESIR")
	cp := os.Getenv("UMCP_LIVE_AESIR_CHECKPOINT")
	if cp == "" {
		t.Skip("UMCP_LIVE_AESIR_CHECKPOINT not set (the scratch copy's clean checkpoint, e.g. umcp/cp/4)")
	}
	l.enable("data")
	l.call("pie", map[string]any{"op": "stop"})
	orig := l.damageMultiplier()
	l.call("data_edit", map[string]any{"op": "set_properties", "asset": tuning, "properties": map[string]any{"player_damage_multiplier": orig + 2}})
	if got := l.damageMultiplier(); math.Abs(got-(orig+2)) > 1e-4 {
		t.Fatalf("the saved change did not land: %v", got)
	}
	r := l.job(l.call("git_revert", map[string]any{"to": cp, "discard_dirty": true, "wait_s": 25}), 15*time.Minute)
	if r["checkpoint"] == nil {
		t.Fatalf("git_revert = %v", r)
	}
	l.job(l.call("editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25}), 10*time.Minute)
	l.enable("data")
	if got := l.damageMultiplier(); math.Abs(got-orig) > 1e-4 {
		t.Fatalf("after git_revert the multiplier is %v, want the checkpoint's %v", got, orig)
	}
}
