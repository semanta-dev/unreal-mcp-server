package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// T1 scenarios for the P5c tools: PIE lifecycle, waits, snapshots + restore, capture
// path safety and scene pruning.

func TestPieStartStopWaitForTheState(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	if res := structured(t, h.call(t, "pie", map[string]any{"op": "start"})); res["pie"] != true {
		t.Fatalf("start = %v", res)
	}
	if res := structured(t, h.call(t, "editor", map[string]any{"op": "ping"})); res["pie"] != true {
		t.Fatalf("after start, ping = %v", res)
	}
	if res := structured(t, h.call(t, "pie", map[string]any{"op": "stop"})); res["pie"] != false {
		t.Fatalf("stop = %v", res)
	}
	if e := errorOf(t, h.call(t, "pie", map[string]any{"op": "input"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("input without key = %v", e["code"])
	}
}

// Found live (P7): the pre-flight's PRECONDITION reached the agent without the hint
// naming ignore_blueprint_errors (the handler looked for an envelope error the bridge
// does not return).
func TestPieStartBlueprintErrorsCarryTheHint(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.Handle("pie_start", func(args map[string]any) (any, *bridgetest.OpError) {
		return nil, &bridgetest.OpError{Code: "PRECONDITION", Message: "1 Blueprint(s) have compile errors",
			Details: map[string]any{"blueprints": []any{"/Game/BP_Bad.BP_Bad"}}}
	})
	e := errorOf(t, h.call(t, "pie", map[string]any{"op": "start"}))
	if e["code"] != "PRECONDITION" || !strings.Contains(fmt.Sprint(e["hint"]), "ignore_blueprint_errors") {
		t.Fatalf("want PRECONDITION with the ignore_blueprint_errors hint, got %v", e)
	}
}

func TestPieWait(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Pawn", "label": "Hero"})
	// PIE not running: the wait keeps polling and answers met:false, not an error.
	res := structured(t, h.call(t, "pie_wait", map[string]any{"predicate": "counts.Pawn >= 1", "timeout_s": 1}))
	if res["met"] != false || res["polls"].(float64) < 2 {
		t.Fatalf("wait without PIE = %v", res)
	}
	h.world.StartPIE()
	if res := structured(t, h.call(t, "pie_wait", map[string]any{"predicate": "counts.Pawn >= 1"})); res["met"] != true {
		t.Fatalf("wait in PIE = %v", res)
	}
	if e := errorOf(t, h.call(t, "pie_wait", map[string]any{"predicate": "counts.Pawn"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("bad predicate = %v", e["code"])
	}
}

func TestSnapshotTakeDiffRestore(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	for _, l := range []string{"A", "B"} {
		h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": l})
	}
	if res := structured(t, h.call(t, "snapshot", map[string]any{"op": "take", "name": "before"})); res["actors"] != 2.0 {
		t.Fatalf("take = %v", res)
	}
	h.world.Move("A", [3]float64{500, 0, 0})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "C"})

	d := structured(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "before"}))["diff"].(map[string]any)
	if labels(d["moved"]) != "A" || labels(d["added"]) != "C" || labels(d["removed"]) != "" {
		t.Fatalf("diff = %v", d)
	}
	res := structured(t, h.call(t, "snapshot_restore", map[string]any{"name": "before"}))
	nr := res["not_restored"].(map[string]any)
	if res["restored"] != 2.0 || len(nr["added"].([]any)) != 1 {
		t.Fatalf("restore = %v", res)
	}
	d = structured(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "before"}))["diff"].(map[string]any)
	if labels(d["moved"]) != "" {
		t.Fatalf("A not moved back: %v", d)
	}
	if l := structured(t, h.call(t, "snapshot", map[string]any{"op": "list"}))["snapshots"].([]any); len(l) != 1 {
		t.Fatalf("list = %v", l)
	}
}

func labels(v any) string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(map[string]any)["label"].(string))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestSnapshotGuards(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	if e := errorOf(t, h.call(t, "snapshot", map[string]any{"op": "take", "name": "../escape"})); e["code"] != "INVALID_ARGUMENT" || len(h.emu.Calls()) != 0 {
		t.Fatalf("path-y name = %v calls=%v", e, h.emu.Calls())
	}
	if e := errorOf(t, h.call(t, "snapshot_restore", map[string]any{"name": "nope"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("restore missing = %v", e["code"])
	}
	h2 := startHarness(t, harnessOpts{})
	if e := errorOf(t, h2.call(t, "snapshot", map[string]any{"op": "list"})); e["code"] != "PRECONDITION" {
		t.Fatalf("no project = %v", e["code"])
	}
}

func TestCaptureClearIsConfined(t *testing.T) {
	dir := t.TempDir()
	h := startHarness(t, harnessOpts{project: dir})
	victim := filepath.Join(dir, "Saved", "keep.txt")
	sess := filepath.Join(dir, "Saved", "MCP", "capture", "s1")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"op": "clear", "session": "../../keep.txt"},
		{"op": "clear"},
		{"op": "clear", "session": "s1", "all": true},
	} {
		if e := errorOf(t, h.call(t, "capture", args)); e["code"] != "INVALID_ARGUMENT" {
			t.Fatalf("%v = %v", args, e["code"])
		}
	}
	if res := structured(t, h.call(t, "capture", map[string]any{"op": "clear", "all": true})); res["removed"] != 1.0 {
		t.Fatalf("clear all = %v", res)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("clear touched a file outside Saved/MCP/capture: %v", err)
	}
}

const pruneSpec = `{"schema":"unreal.scene/v1","scene_id":"arena",
 "actors":[{"label":"hero","kind":"class","class_path":"/Script/Engine.Actor","location":[0,0,0]}]}`

func TestSceneClearPrune(t *testing.T) {
	h := startHarness(t, harnessOpts{toolsets: []spec.Toolset{spec.World}})
	var keep []any
	h.emu.Handle("scene_actors", func(map[string]any) (any, *bridgetest.OpError) {
		return map[string]any{"actors": []any{map[string]any{"label": "arena.hero"}, map[string]any{"label": "arena.old"}}}, nil
	})
	h.emu.Handle("scene_prune", func(args map[string]any) (any, *bridgetest.OpError) {
		keep, _ = args["keep"].([]any)
		return map[string]any{"pruned": []any{"arena.old"}}, nil
	})
	res := structured(t, h.call(t, "scene_clear", map[string]any{"op": "prune", "json": pruneSpec, "dry_run": true}))
	if w := res["would_delete"].([]any); len(w) != 1 || w[0] != "arena.old" {
		t.Fatalf("dry run = %v", res)
	}
	if res := h.call(t, "scene_clear", map[string]any{"op": "prune", "json": pruneSpec}); res.IsError {
		t.Fatalf("prune: %s", text(res))
	}
	if len(keep) != 1 || keep[0] != "arena.hero" {
		t.Fatalf("prune kept %v", keep)
	}
	if e := errorOf(t, h.call(t, "scene_clear", map[string]any{"op": "prune", "scene_id": "arena", "json": pruneSpec})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("prune with scene_id = %v", e["code"])
	}
}

func TestSceneApplyRejectsPruneAndBadSpecs(t *testing.T) {
	h := startHarness(t, harnessOpts{toolsets: []spec.Toolset{spec.World}})
	if e := errorOf(t, h.call(t, "scene", map[string]any{"op": "apply", "json": pruneSpec, "prune": true})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("apply prune = %v", e["code"])
	}
	e := errorOf(t, h.call(t, "scene", map[string]any{"op": "apply", "json": `{"schema":"unreal.scene/v1"}`}))
	if e["code"] != "INVALID_ARGUMENT" || len(h.emu.Calls()) != 0 {
		t.Fatalf("bad spec = %v calls=%v", e, h.emu.Calls())
	}
	if res := structured(t, h.call(t, "scene", map[string]any{"op": "preview", "layout": map[string]any{"type": "grid", "count": 4, "spacing": 100}})); res["count"] != 4.0 {
		t.Fatalf("preview = %v", res)
	}
}

func TestFilteredSnapshotDiffsLikeWithLike(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Pawn", "label": "Hero"})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Rock"})
	h.call(t, "snapshot", map[string]any{"op": "take", "name": "pawns", "class_filter": "Pawn"})
	h.call(t, "snapshot", map[string]any{"op": "take", "name": "all"})
	d := structured(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "pawns"}))["diff"].(map[string]any)
	if labels(d["added"]) != "" || labels(d["removed"]) != "" {
		t.Fatalf("a filtered snapshot vs now must not report the filtered-out actors: %v", d)
	}
	if e := errorOf(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "pawns", "against": "all"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("mixed filters = %v", e["code"])
	}
}

func TestPieWaitReturnsWhenPIEStops(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	calls := 0
	h.emu.Handle("pie_observe", func(map[string]any) (any, *bridgetest.OpError) {
		calls++
		if calls > 1 {
			return nil, &bridgetest.OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
		}
		return map[string]any{"counts": map[string]any{}}, nil
	})
	res := structured(t, h.call(t, "pie_wait", map[string]any{"predicate": "counts.Boss >= 1", "timeout_s": 20}))
	if res["met"] != false || res["pie_running"] != false || res["elapsed_s"].(float64) > 5 {
		t.Fatalf("wait after PIE stopped = %v", res)
	}
}

// TestMovedWorldToolsNeedTheirToolset (R0.1): scene, scene_clear and world_query left
// core; without toolset world a call is PRECONDITION with the enable hint.
func TestMovedWorldToolsNeedTheirToolset(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	for _, tool := range []string{"scene", "scene_clear", "world_query"} {
		e := errorOf(t, h.call(t, tool, map[string]any{"op": "preview"}))
		if e["code"] != "PRECONDITION" || !strings.Contains(fmt.Sprint(e["hint"]), "toolset=world") {
			t.Fatalf("%s without toolset world = %v", tool, e)
		}
	}
}
