//go:build live

package livetest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

// project_map op=source reads and searches the project's text files, never outside it.
func TestLiveProjectSource(t *testing.T) {
	l := session(t, "AESIR")
	hits := l.call("project_map", map[string]any{"op": "source", "path": "Source", "match": "FirstWaveDelay"})
	if !contains(hits["hits"], "AesirWaveDefenseGameMode.h") {
		t.Fatalf("search = %v", hits)
	}
	file := l.call("project_map", map[string]any{"op": "source", "path": "Source/AesirWaveDefense/AesirWaveDefenseGameMode.h", "line": 1})
	if num(file["total_lines"]) < 10 || !contains(file["text"], "AAesirWaveDefenseGameMode") {
		t.Fatalf("read = %v", file["total_lines"])
	}
	l.fail("project_map", map[string]any{"op": "source", "path": "../outside.txt"}, "INVALID_ARGUMENT")
}

// The data reads agents scripted in python: a table, a data asset's properties, an input
// mapping context; and a class path in the wrong module names the real one.
func TestLiveDataReads(t *testing.T) {
	l := session(t, "AESIR")
	l.enable("data", "ui")
	table := l.call("data_query", map[string]any{"op": "table", "asset": "/Game/Data/DT_Waves"})
	if !contains(table["rows"], "Wave_02") {
		t.Fatalf("DT_Waves = %v", table)
	}
	tuning := l.call("reflect", map[string]any{"op": "object", "actor": "/Game/Data/DA_AesirTuning", "properties": []any{"player_damage_multiplier"}})
	if props, _ := tuning["properties"].(map[string]any); tuning["world"] != "asset" || props["player_damage_multiplier"] == nil {
		t.Fatalf("DA_AesirTuning = %v", tuning)
	}
	imc := l.call("data_query", map[string]any{"op": "input_mapping", "asset": "/Game/Input/IMC_Aesir"})
	if !contains(imc["mappings"], "/Game/Input/IA_Dash") {
		t.Fatalf("IMC_Aesir = %v", imc)
	}
	if d := l.call("widget_query", map[string]any{"op": "describe", "class": "MCPHUDWidget"}); d["class_path"] != "/Script/MCPCapture.MCPHUDWidget" {
		t.Fatalf("describe MCPHUDWidget = %v", d["class_path"])
	}
	e := l.fail("reflect", map[string]any{"op": "class", "class": "/Script/UnrealMCP.MCPHUDWidget"}, "NOT_FOUND")
	if !contains(e["message"], "did you mean /Script/MCPCapture.MCPHUDWidget") {
		t.Fatalf("wrong module = %v", e)
	}
	if py := l.call("python", map[string]any{"op": "run", "code": "print(1)"}); !contains(py["see_also"], "data_query") {
		t.Fatalf("python see_also = %v", py["see_also"])
	}
}

// A font prop with only a size keeps the font object (it used to draw boxes).
func TestLiveWidgetFontSizeKeepsTheFont(t *testing.T) {
	l := session(t, "AESIR")
	l.enable("ui")
	name := fmt.Sprintf("WBP_LiveFont_%d", time.Now().UnixNano())
	wbp := "/Game/LiveTest/" + name
	// The asset just made is still held in memory and refuses deletion; earlier runs'
	// are not, so each run clears what the last one left (at most one stays behind).
	clear := func() {
		l.python("lib = unreal.EditorAssetLibrary\n" +
			"for p in lib.list_assets('/Game/LiveTest'):\n" +
			"    a = lib.load_asset(p.split('.')[0])\n" +
			"    if a: lib.delete_loaded_asset(a)")
	}
	clear()
	t.Cleanup(clear)
	l.call("asset_create", map[string]any{"op": "create", "kind": "widget_blueprint", "dest": wbp, "class": "MCPHUDWidget"})
	l.call("widget_edit", map[string]any{"op": "compose", "asset": wbp, "tree": map[string]any{"class": "CanvasPanel", "name": "Root",
		"children": []any{map[string]any{"class": "TextBlock", "name": "Label", "props": map[string]any{"text": "WAVE 1", "font": map[string]any{"size": 31}}}}}})
	// The oracle reads the TextBlock by its object path inside the Widget Blueprint.
	out := l.python("unreal.load_asset('" + wbp + "')\n" +
		"w = unreal.find_object(None, '" + wbp + "." + name + ":WidgetTree.Label')\n" +
		"f = w.get_editor_property('font')\n" +
		"print(int(f.get_editor_property('size')), f.get_editor_property('font_object') is not None)")
	if !contains(out, "31 True") {
		t.Fatalf("font after compose: %q (want size 31 and a font object)", out)
	}
}

// pie op=aim turns the view with mouse input onto static targets (a 180-degree turn
// included), then aim + fire bursts kill an enemy.
func TestLiveAimAndKill(t *testing.T) {
	l := session(t, "AESIR")
	l.freshPIE()
	for _, target := range []string{"Cover_0", "Wall_South", "Cover_2"} {
		r := l.call("pie", map[string]any{"op": "aim", "actor": target})
		if r["aimed"] != true || math.Abs(num(r["yaw_error"])) > 1 || math.Abs(num(r["pitch_error"])) > 1 {
			t.Fatalf("aim %s = %v", target, r)
		}
	}
	l.enable("game")
	l.call("pie_wait", map[string]any{"predicate": "counts.EnemyCharacter >= 1", "timeout_s": 25})
	for i := 0; i < 12; i++ {
		if _, isErr := l.raw("pie", map[string]any{"op": "aim", "class": "EnemyCharacter"}); isErr {
			break // none left in play
		}
		l.call("pie", map[string]any{"op": "input", "key": "LeftMouseButton", "action": "hold", "duration_s": 0.8})
		time.Sleep(900 * time.Millisecond)
		if s := l.call("game", map[string]any{"op": "snapshot"}); num(s["kills"]) >= 1 {
			return
		}
	}
	t.Fatalf("no kill after aim + fire: %v", l.call("game", map[string]any{"op": "snapshot"}))
}

// A recorded playtest that aims with an input step: hits and kills in its events, the
// result names the analyze reads, rubric re-scores its frames, and the feel audit's
// visual median equals the one computed here from the same playtest.json.
func TestLivePlaytestAimEventsAndFeel(t *testing.T) {
	l := session(t, "AESIR")
	l.enable("game", "design")
	l.call("pie", map[string]any{"op": "stop"})
	sc := `{"schema":"scenario/v1","name":"livetest_aim","mode":"pie","level":"/Game/Maps/L_Arena","duration_s":25,"interval_s":0.5,
	 "record_events":true,
	 "beats":[{"at_s":1,"game_command":{"name":"start_wave"}},
	          {"at_s":2,"input":{"key":"LeftMouseButton","action":"hold","duration_s":20}},
	          {"at_s":2.2,"input":{"class":"EnemyCharacter","duration_s":20}}],
	 "rubric":[{"id":"player","kind":"nonzero_count","path":"counts.AesirCharacter"}]}`
	r := l.job(l.call("playtest", map[string]any{"op": "run", "json": sc, "wait_s": 25}), 5*time.Minute)
	path, _ := r["playtest_path"].(string)
	if r["verdict"] != "PASS" || path == "" || num(r["timeline_frames"]) < 10 || !contains(r["next"], "analyze op=events") {
		t.Fatalf("playtest = %v", r)
	}
	ev := l.call("analyze", map[string]any{"op": "events", "path": path, "kinds": []any{"hit", "kill"}})
	counts, _ := ev["counts"].(map[string]any)
	if num(counts["hit"]) < 5 || num(counts["kill"]) < 1 {
		t.Fatalf("recorded hits/kills = %v", counts)
	}
	rb := l.call("analyze", map[string]any{"op": "rubric", "path": path,
		"rubric": []any{map[string]any{"id": "player", "kind": "nonzero_count", "path": "counts.AesirCharacter"}}})
	if rb["verdict"] != "PASS" {
		t.Fatalf("rubric from playtest_path = %v", rb)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Events []struct {
			T       float64  `json:"t"`
			Kind    string   `json:"kind"`
			VisualT *float64 `json:"visual_t"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var d []float64
	for _, e := range doc.Events {
		if e.Kind == "hit" && e.VisualT != nil && *e.VisualT >= e.T {
			d = append(d, (*e.VisualT-e.T)*1000)
		}
	}
	if len(d) == 0 {
		t.Fatal("no hit with a visual response recorded")
	}
	sort.Float64s(d)
	want := d[len(d)/2]
	if len(d)%2 == 0 {
		want = (d[len(d)/2-1] + d[len(d)/2]) / 2
	}
	feel := l.call("design_audit", map[string]any{"kind": "feel", "input": map[string]any{"source": path}})
	rep, _ := feel["report"].(map[string]any)
	audit, _ := rep["audit"].(map[string]any)
	if got := num(audit["MedianVisualMs"]); math.Abs(got-want) > 0.01 {
		t.Fatalf("feel MedianVisualMs = %v, playtest.json says %v", got, want)
	}
}
