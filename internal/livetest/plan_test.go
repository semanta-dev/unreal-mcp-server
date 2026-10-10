//go:build live

package livetest

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// The remediation plan's features, each against the real editor and game. Edits to the
// game's assets are restored (the original read first); actors are labelled LiveTest_*.

// R0.9 undo: an actor spawn is one server undo step; undo removes it, redo brings it
// back.
func TestLiveUndoRedoSpawn(t *testing.T) {
	l := session(t, "AESIR")
	l.call("pie", map[string]any{"op": "stop"}) // undo is refused during PIE
	label := fmt.Sprintf("LiveTest_Undo_%d", time.Now().UnixNano())
	exists := func() bool {
		return num(l.call("actor_query", map[string]any{"op": "find", "filter": label, "world": "editor"})["count"]) == 1
	}
	l.call("actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.PointLight", "label": label, "location": []any{0, 0, 800}})
	t.Cleanup(func() { _, _ = l.raw("actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": label}) })
	if !exists() {
		t.Fatal("spawned actor not found")
	}
	if u := l.call("undo", map[string]any{"op": "undo"}); !contains(u, "MCP:") || exists() {
		t.Fatalf("undo = %v; still exists = %v", u, exists())
	}
	if r := l.call("undo", map[string]any{"op": "redo"}); !contains(r, "MCP:") || !exists() {
		t.Fatalf("redo = %v; exists = %v", r, exists())
	}
}

// R1: the game's own API — capabilities, snapshot, a command in the opening
// intermission, its events — and actor_call parse=json on a game subsystem.
func TestLiveGameAPI(t *testing.T) {
	l := session(t, "AESIR")
	l.enable("game")
	l.freshPIE()
	caps := l.call("game", map[string]any{"op": "capabilities"})
	if !contains(caps, "start_wave") {
		t.Fatalf("capabilities = %v", caps)
	}
	before := l.call("game", map[string]any{"op": "snapshot"})
	if before["wave_state"] != "intermission" {
		t.Skipf("the game is not in its opening intermission (%v): start_wave would be refused", before["wave_state"])
	}
	l.call("game_command", map[string]any{"name": "start_wave", "request_id": fmt.Sprintf("livetest-%d", time.Now().UnixNano())})
	l.call("pie_wait", map[string]any{"predicate": "gamestate.wave_number >= 1", "timeout_s": 10})
	ev := l.call("game", map[string]any{"op": "events"})
	if !contains(ev["events"], "wave_start") {
		t.Fatalf("events after start_wave = %v", ev)
	}
	js := l.call("actor_call", map[string]any{"actor": "@subsystem:AesirAgentSubsystem", "function": "PeekSnapshotJson", "parse": "json"})
	res, _ := js["result"].(map[string]any)
	if num(res["wave_number"]) < 1 {
		t.Fatalf("PeekSnapshotJson parse=json = %v", js)
	}
}

// R2: an analog axis is sent every tick for its duration and turns the view.
func TestLiveAxisInput(t *testing.T) {
	l := session(t, "AESIR")
	l.freshPIE()
	before := l.call("pie", map[string]any{"op": "aim", "actor": "Cover_0"})
	r := l.call("pie", map[string]any{"op": "input", "key": "MouseX", "action": "axis", "value": 20.0, "duration_s": 0.3})
	if r["done"] != true || num(r["ticks"]) < 2 {
		t.Fatalf("axis input = %v", r)
	}
	after := l.call("pie", map[string]any{"op": "aim", "actor": "Cover_0"})
	if after["aimed"] != true || before["aimed"] != true {
		t.Fatalf("re-aim after the axis turn: before %v after %v", before, after)
	}
	if num(after["steps"]) < 1 {
		t.Fatalf("the axis input did not turn the view (re-aim took no step): %v", after)
	}
}

// R3: data_edit on the game's data, each change read back through data_query / reflect
// and restored.
func TestLiveDataEdits(t *testing.T) {
	l := session(t, "AESIR")
	l.call("pie", map[string]any{"op": "stop"})
	l.enable("data")

	// DataTable rows: upsert a new row, read it, delete it.
	row := fmt.Sprintf("LiveTest_%d", time.Now().UnixNano())
	l.call("data_edit", map[string]any{"op": "table_upsert", "asset": "/Game/Data/DT_Waves", "rows": map[string]any{row: map[string]any{"EnemyCount": 7}}})
	got := l.call("data_query", map[string]any{"op": "table", "asset": "/Game/Data/DT_Waves", "row_names": []any{row}})
	if !contains(got["rows"], "7") {
		t.Fatalf("upserted row = %v", got)
	}
	l.call("data_edit", map[string]any{"op": "table_delete", "asset": "/Game/Data/DT_Waves", "row_names": []any{row}})
	if got := l.call("data_query", map[string]any{"op": "table", "asset": "/Game/Data/DT_Waves"}); contains(got["rows"], row) {
		t.Fatalf("row still present after table_delete")
	}

	// A data asset property.
	prop := func() float64 {
		r := l.call("reflect", map[string]any{"op": "object", "actor": "/Game/Data/DA_AesirTuning", "properties": []any{"player_damage_multiplier"}})
		p, _ := r["properties"].(map[string]any)
		return num(p["player_damage_multiplier"])
	}
	orig := prop()
	t.Cleanup(func() {
		l.call("data_edit", map[string]any{"op": "set_properties", "asset": "/Game/Data/DA_AesirTuning", "properties": map[string]any{"player_damage_multiplier": orig}})
	})
	l.call("data_edit", map[string]any{"op": "set_properties", "asset": "/Game/Data/DA_AesirTuning", "properties": map[string]any{"player_damage_multiplier": orig + 0.5}})
	if got := prop(); math.Abs(got-(orig+0.5)) > 1e-4 {
		t.Fatalf("player_damage_multiplier = %v, want %v", got, orig+0.5)
	}

	// Curve keys.
	curve := l.call("data_query", map[string]any{"op": "curve", "asset": "/Game/Data/C_DamageFalloff"})
	keys, _ := curve["keys"].([]any)
	if len(keys) == 0 {
		t.Fatalf("C_DamageFalloff keys = %v", curve)
	}
	t.Cleanup(func() {
		l.call("data_edit", map[string]any{"op": "curve_keys", "asset": "/Game/Data/C_DamageFalloff", "points": keys})
	})
	l.call("data_edit", map[string]any{"op": "curve_keys", "asset": "/Game/Data/C_DamageFalloff", "points": []any{[]any{0, 1}, []any{1234, 0.25}}})
	if got := l.call("data_query", map[string]any{"op": "curve", "asset": "/Game/Data/C_DamageFalloff"}); !contains(got["keys"], "1234") {
		t.Fatalf("curve after curve_keys = %v", got)
	}

	// An input mapping.
	mapped := func() string {
		return fmt.Sprint(l.call("data_query", map[string]any{"op": "input_mapping", "asset": "/Game/Input/IMC_Aesir"})["mappings"])
	}
	t.Cleanup(func() {
		l.call("data_edit", map[string]any{"op": "input_mapping", "action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC_Aesir", "keys": []any{"LeftShift"}})
	})
	l.call("data_edit", map[string]any{"op": "input_mapping", "action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC_Aesir", "keys": []any{"E"}})
	if m := mapped(); !contains(m, "keys:[E]") {
		t.Fatalf("IA_Dash after remap = %v", m)
	}
}

// R4: a HUD widget bound to the game state, mounted in play: its live text follows the
// wave number, and a PIE screenshot with UI is written.
func TestLiveHUDBindMountLiveTree(t *testing.T) {
	l := session(t, "AESIR")
	l.enable("ui", "game")
	l.call("pie", map[string]any{"op": "stop"})
	name := fmt.Sprintf("WBP_LiveHUD_%d", time.Now().UnixNano())
	wbp := "/Game/LiveTest/" + name
	l.call("asset_create", map[string]any{"op": "create", "kind": "widget_blueprint", "dest": wbp, "class": "MCPHUDWidget"})
	l.call("widget_edit", map[string]any{"op": "compose", "asset": wbp, "tree": map[string]any{"class": "CanvasPanel", "name": "Root",
		"children": []any{map[string]any{"class": "TextBlock", "name": "WaveText", "is_variable": true, "props": map[string]any{"text": "WAVE ?"}}}}})
	l.call("widget_edit", map[string]any{"op": "bind", "asset": wbp, "bindings": []any{map[string]any{"widget": "WaveText", "field": "Text",
		"source": "game_state", "path": "WaveNumber", "conversion": "format_text", "format": "WAVE {value}"}}})
	l.freshPIE()
	l.call("widget_query", map[string]any{"op": "mount", "class": wbp})
	snap := l.call("game", map[string]any{"op": "snapshot"})
	want := fmt.Sprintf("WAVE %v", snap["wave_number"])
	deadline := time.Now().Add(5 * time.Second)
	for {
		tree := l.call("widget_query", map[string]any{"op": "live_tree", "class": wbp})
		if contains(tree["widgets"], want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live tree never showed %q: %v", want, tree)
		}
		time.Sleep(300 * time.Millisecond)
	}
	shot := l.call("screenshot", map[string]any{"op": "pie", "ui": true})
	if !contains(shot, ".png") {
		t.Fatalf("pie screenshot = %v", shot)
	}
}

// R5: a seeded batch records gameplay events by default and reports one run per seed.
func TestLiveSeededBatch(t *testing.T) {
	l := session(t, "AESIR")
	l.call("pie", map[string]any{"op": "stop"})
	sc := `{"schema":"scenario/v1","name":"livetest_batch","mode":"pie","level":"/Game/Maps/L_Arena","duration_s":6,"interval_s":1,
	 "beats":[{"at_s":1,"game_command":{"name":"start_wave"}}],
	 "rubric":[{"id":"player","kind":"nonzero_count","path":"counts.AesirCharacter"}]}`
	r := l.job(l.call("playtest", map[string]any{"op": "batch", "json": sc, "seeds": []any{1, 2}, "wait_s": 25}), 10*time.Minute)
	runs, _ := r["runs"].([]any)
	if len(runs) != 2 || !contains(r["note"], "record_events") || !contains(r["events"], "wave_start") {
		t.Fatalf("batch = %v", r)
	}
}

// R6: a snapshot with properties restores a moved actor's transform; sphere_overlap sees
// the player's pawn.
func TestLiveSnapshotRestoreAndOverlap(t *testing.T) {
	l := session(t, "AESIR")
	l.call("pie", map[string]any{"op": "stop"})
	label := fmt.Sprintf("LiveTest_Snap_%d", time.Now().UnixNano())
	l.call("actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.PointLight", "label": label, "location": []any{100, 200, 300}})
	t.Cleanup(func() { _, _ = l.raw("actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": label}) })
	snap := "livetest_" + label
	l.call("snapshot", map[string]any{"op": "take", "name": snap})
	l.call("actor_edit", map[string]any{"op": "transform", "world": "editor", "actor": label, "location": []any{900, 900, 900}})
	if d := l.call("snapshot", map[string]any{"op": "diff", "name": snap}); !contains(d["diff"], "moved:[map[from:[[100 200 300]") || !contains(d["diff"], label) {
		t.Fatalf("diff after the move = %v", d)
	}
	l.call("snapshot_restore", map[string]any{"name": snap, "save": false})
	got := l.call("actor_query", map[string]any{"op": "get", "actor": label, "world": "editor"})
	a, _ := got["actor"].(map[string]any)
	loc, _ := a["location"].([]any)
	if len(loc) != 3 || math.Abs(num(loc[0])-100) > 0.5 || math.Abs(num(loc[2])-300) > 0.5 {
		t.Fatalf("location after restore = %v", loc)
	}

	l.freshPIE()
	l.enable("world")
	pawn := l.call("pie_observe", map[string]any{"pawn": true})
	p, _ := pawn["pawn"].(map[string]any)
	center, _ := p["location"].([]any)
	ov := l.call("world_query", map[string]any{"op": "sphere_overlap", "center": center, "radius": 200, "world": "pie"})
	if !contains(ov["hits"], "AesirCharacter") {
		t.Fatalf("sphere_overlap at the pawn = %v", ov)
	}
}
