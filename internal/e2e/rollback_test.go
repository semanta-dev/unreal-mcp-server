package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R6.1: a snapshot records chosen properties (refusing one no actor has), and diff
// reports their changes; snapshots that recorded different properties are not compared.
func TestSnapshotProperties(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.AddActor("Turret", "Turret", [3]float64{}, map[string]any{"Health": 80.0})
	h.world.AddActor("Rock", "StaticMeshActor", [3]float64{}, nil)
	if e := errorOf(t, h.call(t, "snapshot", map[string]any{"op": "take", "name": "s1", "properties": []any{"Health", "Armor"}})); e["code"] != "INVALID_ARGUMENT" ||
		!strings.Contains(fmt.Sprint(e["message"]), "Armor") {
		t.Fatalf("a property no actor has = %v", e)
	}
	out := structured(t, h.call(t, "snapshot", map[string]any{"op": "take", "name": "s1", "properties": []any{"Health"}}))
	if out["actors"] != 2.0 {
		t.Fatalf("take = %v", out)
	}
	structured(t, h.call(t, "actor_edit", map[string]any{"op": "set_properties", "world": "editor", "actor": "Turret", "properties": map[string]any{"Health": 5.0}}))
	out = structured(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "s1"}))
	d, _ := out["diff"].(map[string]any)
	ch, _ := d["changed"].([]any)
	if len(ch) != 1 || !strings.Contains(fmt.Sprint(ch[0]), "Health") {
		t.Fatalf("diff = %v", out)
	}
	structured(t, h.call(t, "snapshot", map[string]any{"op": "take", "name": "s2"}))
	if e := errorOf(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "s1", "against": "s2"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("different recorded properties = %v", e)
	}
	if e := errorOf(t, h.call(t, "snapshot", map[string]any{"op": "list", "properties": []any{"Health"}})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("properties on list = %v", e)
	}
}

// R6.2: dry_run and save=false reach the companion; settings / curve_keys refuse dry_run.
func TestDryRunAndSaveFalseArgs(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	var got map[string]any
	h.emu.Handle("asset_create", func(args map[string]any) (any, *bridgetest.OpError) {
		got = args
		return map[string]any{"dry_run": true, "asset": args["dest"], "kind": args["kind"], "would": "create"}, nil
	})
	out := structured(t, h.call(t, "asset_create", map[string]any{"op": "create", "kind": "blueprint", "dest": "/Game/BP/BP_X", "class": "Actor", "dry_run": true}))
	if got["dry_run"] != true || out["would"] != "create" || out["undoable"] != nil {
		t.Fatalf("asset_create dry_run = %v (args %v)", out, got)
	}
	h.emu.Handle("open_level", func(args map[string]any) (any, *bridgetest.OpError) {
		got = args
		return nil, &bridgetest.OpError{Code: "PRECONDITION", Message: "1 unsaved package(s)"}
	})
	if e := errorOf(t, h.call(t, "level", map[string]any{"op": "open", "level": "/Game/Maps/L_B", "save": false})); e["code"] != "PRECONDITION" || got["save"] != false {
		t.Fatalf("level open save=false = %v (args %v)", e, got)
	}
	h2 := startHarness(t, harnessOpts{project: t.TempDir(), toolsets: []spec.Toolset{spec.Data}})
	for _, op := range []string{"settings", "curve_keys"} {
		args := map[string]any{"op": op, "dry_run": true, "asset": "/Game/X", "class": "/Script/X.Y", "properties": map[string]any{"a": 1}, "points": []any{[]any{0, 1}}}
		if op == "settings" {
			delete(args, "asset")
			delete(args, "points")
		} else {
			delete(args, "class")
			delete(args, "properties")
		}
		if e := errorOf(t, h2.call(t, "data_edit", args)); e["code"] != "INVALID_ARGUMENT" || !strings.Contains(fmt.Sprint(e["message"]), "dry_run") {
			t.Fatalf("%s dry_run = %v", op, e)
		}
	}
}

// R6 review: diff against the level is lenient — deleting the only actor that had a
// recorded property is "removed", not a refusal.
func TestSnapshotDiffAfterTheOnlyHolderIsGone(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.AddActor("Turret", "Turret", [3]float64{}, map[string]any{"Health": 80.0})
	h.world.AddActor("Rock", "StaticMeshActor", [3]float64{}, nil)
	structured(t, h.call(t, "snapshot", map[string]any{"op": "take", "name": "s1", "properties": []any{"Health", "Health"}}))
	structured(t, h.call(t, "actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": "Turret"}))
	out := structured(t, h.call(t, "snapshot", map[string]any{"op": "diff", "name": "s1"}))
	d, _ := out["diff"].(map[string]any)
	if rm, _ := d["removed"].([]any); len(rm) != 1 || !strings.Contains(fmt.Sprint(rm[0]), "Turret") {
		t.Fatalf("diff = %v", out)
	}
}
