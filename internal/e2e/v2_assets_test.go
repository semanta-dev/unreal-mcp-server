package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// T1 scenarios for the rest of the actor_edit world × op matrix (plan §2.3: every
// cell has a T1 test) and the P5b tools (assets, viewport, reflect, project, widgets).

func TestActorEditMatrixRemainingCells(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Crate"})
	// editor × transform, editor × set_properties
	res := structured(t, h.call(t, "actor_edit", map[string]any{"op": "transform", "world": "editor", "actor": "Crate", "location": []any{7.0, 8.0, 9.0}}))
	if res["world"] != "editor" || res["actor"].(map[string]any)["location"].([]any)[2] != 9.0 {
		t.Fatalf("editor transform = %v", res)
	}
	if res := structured(t, h.call(t, "actor_edit", map[string]any{"op": "set_properties", "world": "editor", "actor": "Crate", "properties": map[string]any{"health": 5.0}})); res["world"] != "editor" {
		t.Fatalf("editor set_properties = %v", res)
	}
	h.world.StartPIE()
	// pie × set_properties, pie × delete — the editor copy is untouched.
	if res := structured(t, h.call(t, "actor_edit", map[string]any{"op": "set_properties", "world": "pie", "actor": "Crate", "properties": map[string]any{"health": 1.0}})); res["world"] != "pie" {
		t.Fatalf("pie set_properties = %v", res)
	}
	if res := structured(t, h.call(t, "actor_edit", map[string]any{"op": "delete", "world": "pie", "actor": "Crate"})); res["world"] != "pie" {
		t.Fatalf("pie delete = %v", res)
	}
	if got := h.world.Labels(); len(got) != 1 || got[0] != "Crate" {
		t.Fatalf("a PIE delete removed the editor actor: %v", got)
	}
}

func TestActorCallWorldsAndPawn(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Pawn", "label": "Hero"})
	if e := errorOf(t, h.call(t, "actor_call", map[string]any{"actor": "Hero", "function": "GetHealth", "world": "editor"})); e["code"] != "UNSUPPORTED" {
		t.Fatalf("editor actor_call code = %v", e["code"])
	}
	if e := errorOf(t, h.call(t, "actor_query", map[string]any{"op": "get", "actor": "@pawn"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("@pawn in the editor world = %v", e["code"])
	}
	h.world.StartPIE()
	got := structured(t, h.call(t, "actor_query", map[string]any{"op": "get", "world": "pie", "actor": "@pawn"}))
	if a := got["actor"].(map[string]any); a["label"] != "Hero" || !strings.Contains(a["path"].(string), "UEDPIE_0_") {
		t.Fatalf("@pawn resolved to %v", got)
	}
	if e := errorOf(t, h.call(t, "actor_call", map[string]any{"actor": "@pawn", "function": "Nope"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("unknown function code = %v", e["code"])
	}
}

func TestAssetCreateConflictThenReplace(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.AddAsset("/Game/BP/BP_Turret", "blueprint")
	args := map[string]any{"op": "create", "kind": "blueprint", "dest": "/Game/BP/BP_Turret", "class": "Actor"}
	if e := errorOf(t, h.call(t, "asset_create", args)); e["code"] != "CONFLICT" {
		t.Fatalf("create over an existing asset = %v", e["code"])
	}
	args["op"] = "replace"
	if res := structured(t, h.call(t, "asset_create", args)); res["replaced"] != true {
		t.Fatalf("replace = %v", res)
	}
	// Per-kind required params are checked before the editor is reached.
	before := len(h.emu.Calls())
	e := errorOf(t, h.call(t, "asset_create", map[string]any{"op": "create", "kind": "data_table", "dest": "/Game/DT/DT_X"}))
	if e["code"] != "INVALID_ARGUMENT" || !strings.Contains(e["message"].(string), "row_struct") {
		t.Fatalf("missing row_struct = %v", e)
	}
	if len(h.emu.Calls()) != before {
		t.Fatal("an invalid create reached the editor")
	}
	if list := structured(t, h.call(t, "asset_query", map[string]any{"op": "list", "folder": "/Game/BP"})); list["total"] != 1.0 {
		t.Fatalf("list = %v", list)
	}
}

func TestAssetCreateReplaceIsTheOnlyDestructiveOp(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	tiers := map[string]string{}
	for _, op := range h.specs["asset_create"].Ops {
		tiers[op.Name] = op.Tier.String()
	}
	if tiers["create"] != "mutating" || tiers["replace"] != "destructive" {
		t.Fatalf("asset_create tiers = %v", tiers)
	}
}

func TestAssetImportValidatesBeforeTheEditor(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	missing := filepath.Join(t.TempDir(), "nope.fbx")
	e := errorOf(t, h.call(t, "asset_import", map[string]any{"op": "files", "files": []any{missing}}))
	if e["code"] != "NOT_FOUND" || len(h.emu.Calls()) != 0 {
		t.Fatalf("missing file: %v calls=%v", e, h.emu.Calls())
	}
	if e := errorOf(t, h.call(t, "asset_import", map[string]any{"op": "datatable", "asset": "/Game/DT", "json": "[]", "csv": "a"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("json+csv = %v", e["code"])
	}
}

func TestViewportSelectAndSelection(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	for _, l := range []string{"A", "B"} {
		h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": l})
	}
	if res := structured(t, h.call(t, "viewport", map[string]any{"op": "select", "actors": []any{"A", "B"}})); res["count"] != 2.0 {
		t.Fatalf("select = %v", res)
	}
	if e := errorOf(t, h.call(t, "viewport", map[string]any{"op": "select", "actors": []any{"Ghost"}})); e["code"] != "NOT_FOUND" {
		t.Fatalf("unknown actor = %v", e["code"])
	}
	if res := structured(t, h.call(t, "viewport", map[string]any{"op": "selection"})); res["count"] != 2.0 {
		t.Fatalf("selection = %v", res)
	}
	// Console passthrough moved to the console tool: an unknown property is rejected.
	if e := errorOf(t, h.call(t, "viewport", map[string]any{"op": "set", "console": []any{"stat fps"}})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("viewport console = %v", e["code"])
	}
}

func TestReflectObjectEchoesWorld(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Lamp"})
	if res := structured(t, h.call(t, "reflect", map[string]any{"op": "object", "actor": "Lamp"})); res["world"] != "editor" {
		t.Fatalf("reflect = %v", res)
	}
	if e := errorOf(t, h.call(t, "reflect", map[string]any{"op": "class", "class": "Actor", "actor": "Lamp"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("class with actor = %v", e["code"])
	}
}

func TestProjectToolsWorkOffline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Game.uproject"), []byte(`{"Modules":[{"Name":"Game"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "Config"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := startHarness(t, harnessOpts{noEditor: true, project: dir})
	if res := h.call(t, "project_config", map[string]any{"op": "gameplay_tag", "tag": "Ability.Dash"}); res.IsError {
		t.Fatalf("gameplay_tag: %s", text(res))
	}
	ini, _ := os.ReadFile(filepath.Join(dir, "Config", "DefaultGameplayTags.ini"))
	if !strings.Contains(string(ini), "Ability.Dash") {
		t.Fatalf("tag not written:\n%s", ini)
	}
	if res := structured(t, h.call(t, "project_map", map[string]any{"op": "project"})); res["project"] == nil {
		t.Fatalf("project_map = %v", res)
	}
	// The live half needs the editor.
	if e := errorOf(t, h.call(t, "project_map", map[string]any{"op": "level"})); e["code"] != "EDITOR_UNREACHABLE" {
		t.Fatalf("project_map level without an editor = %v", e["code"])
	}
}

func TestProjectToolsNeedAProject(t *testing.T) {
	h := startHarness(t, harnessOpts{noEditor: true})
	if e := errorOf(t, h.call(t, "project_config", map[string]any{"op": "gameplay_tag", "tag": "A.B"})); e["code"] != "PRECONDITION" {
		t.Fatalf("no project = %v", e["code"])
	}
}

func TestWidgetEditComposeRejectsRemoval(t *testing.T) {
	h := startHarness(t, harnessOpts{toolsets: []spec.Toolset{spec.UI}})
	e := errorOf(t, h.call(t, "widget_edit", map[string]any{"op": "compose", "asset": "/Game/UI/WBP", "tree": map[string]any{"name": "Root"}, "remove": []any{"Bar"}}))
	if e["code"] != "INVALID_ARGUMENT" || len(h.emu.Calls()) != 0 {
		t.Fatalf("compose+remove = %v calls=%v", e, h.emu.Calls())
	}
}

// TestUndoOnlyStepsTheServersEdits (R0.9): undo/redo step the editor's buffer only when
// the next step is the server's ("MCP: " title); a human's edit on top is CONFLICT and
// nothing changes; PIE and an older plugin refuse.
func TestUndoOnlyStepsTheServersEdits(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	count := func() int {
		res := structured(t, h.call(t, "actor_query", map[string]any{"op": "list", "world": "editor"}))
		actors, _ := res["actors"].([]any)
		return len(actors)
	}
	before := count()
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Undoable"})
	if res := structured(t, h.call(t, "undo", map[string]any{"op": "undo"})); !strings.HasPrefix(fmt.Sprint(res["undone"]), "MCP: spawn /Script/Engine.Actor [") || count() != before {
		t.Fatalf("undo = %v (actors %d, want %d)", res, count(), before)
	}
	if res := structured(t, h.call(t, "undo", map[string]any{"op": "redo"})); !strings.HasPrefix(fmt.Sprint(res["redone"]), "MCP: spawn /Script/Engine.Actor [") || count() != before+1 {
		t.Fatalf("redo = %v", res)
	}
	// A python run makes no undo step: undoing now would revert the spawn underneath it.
	h.call(t, "python", map[string]any{"op": "run", "code": "pass"})
	if e := errorOf(t, h.call(t, "undo", map[string]any{"op": "undo"})); e["code"] != "CONFLICT" || count() != before+1 {
		t.Fatalf("undo after an untracked python edit = %v", e)
	}
	h.world.UserEdit("MCP: spawn later") // (fresh transaction on top)
	h.world.UserEdit("Move Actor")
	e := errorOf(t, h.call(t, "undo", map[string]any{"op": "undo"}))
	if d, _ := e["details"].(map[string]any); e["code"] != "CONFLICT" || d["title"] != "Move Actor" || count() != before+1 {
		t.Fatalf("undo over a human edit = %v", e)
	}
	h.world.StartPIE()
	if e := errorOf(t, h.call(t, "undo", map[string]any{"op": "undo"})); e["code"] != "PRECONDITION" {
		t.Fatalf("undo in PIE = %v", e)
	}
	h.world.StopPIE()
	h.world.PluginAPI = 2
	if e := errorOf(t, h.call(t, "undo", map[string]any{"op": "undo"})); e["code"] != "PRECONDITION" {
		t.Fatalf("undo with an old plugin = %v", e)
	}
}
