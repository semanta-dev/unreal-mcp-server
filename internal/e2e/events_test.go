package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R5.1 / R5.3: a playtest with record_events merges the engine recorder, the game
// journal and its own beats into one timeline, writes it to playtest.json, and scores
// event rubric kinds over it — never over a source that was unavailable or had a gap.
const eventsScenario = `{"schema":"scenario/v1","name":"events","mode":"pie","duration_s":0.4,"interval_s":0.1,
 "record_events":true,
 "beats":[{"at_s":0.05,"game_command":{"name":"start_wave"}}],
 "rubric":[{"id":"kills","kind":"rate","path":"events.kill","params":{"min":0.5}},
           {"id":"ttk","kind":"time_between","path":"events.kill","params":{"from":"hit","stat":"max","max":3}},
           {"id":"deaths","kind":"histogram","path":"events.death","params":{"by":"data.cause","max_share":0.9}},
           {"id":"damage","kind":"rate","path":"events.damage","params":{"min":0.1}}]}`

func eventsHarness(t *testing.T, api int) *harness {
	h := startHarness(t, harnessOpts{project: gameProject(t, gameAPIJSON, ""), toolsets: []spec.Toolset{spec.Game}})
	h.world.PluginAPI = api
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	h.world.Game.Journal = []map[string]any{
		{"seq": 1.0, "t": 1.0, "kind": "hit", "actor": "Player", "target": "Brute_1", "by_player": true, "data": map[string]any{"visual_t": 1.03}},
		{"seq": 2.0, "t": 2.5, "kind": "kill", "actor": "Player", "target": "Brute_1", "by_player": true},
		{"seq": 3.0, "t": 9.0, "kind": "death", "target": "Player", "data": map[string]any{"cause": "brute"}},
		{"seq": 4.0, "t": 20.0, "kind": "death", "target": "Player", "data": map[string]any{"cause": "boss"}},
	}
	h.world.Events.Engine = []map[string]any{
		{"seq": 1.0, "t": 1.0, "kind": "point_damage", "actor": "Player", "target": "Brute_1", "by_player": true},
		{"seq": 2.0, "t": 1.0, "kind": "damage", "actor": "Player", "target": "Brute_1", "by_player": true},
		{"seq": 3.0, "t": 4.0, "kind": "damage", "actor": "Brute_2", "target": "Core"},
	}
	return h
}

func runEvents(t *testing.T, h *harness) map[string]any {
	t.Helper()
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": eventsScenario, "wait_s": 20}))
	r, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || r == nil {
		t.Fatalf("playtest = %v", out)
	}
	return r
}

func checks(r map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	rb, _ := r["rubric"].(map[string]any)
	for _, c := range rb["checks"].([]any) {
		m := c.(map[string]any)
		out[fmt.Sprint(m["id"])] = m
	}
	return out
}

func TestPlaytestRecordsTheEventTimeline(t *testing.T) {
	h := eventsHarness(t, 8)
	r := runEvents(t, h)
	if r["verdict"] != "PASS" {
		t.Fatalf("verdict %v: %v", r["verdict"], r["rubric"])
	}
	ev, _ := r["events"].(map[string]any)
	byKind, _ := ev["by_kind"].(map[string]any)
	if byKind["kill"] != 1.0 || byKind["damage"] != 2.0 || byKind["game_command"] != 1.0 {
		t.Fatalf("events = %v", ev)
	}
	if eng, _ := ev["engine"].(map[string]any); eng["still_bound"] != 0.0 {
		t.Fatalf("engine report = %v", ev["engine"])
	}
	if len(h.world.Events.Started) != 1 || h.world.Events.Stopped != 1 {
		t.Fatalf("events session started %d / stopped %d", len(h.world.Events.Started), h.world.Events.Stopped)
	}
	j, _ := h.world.Events.Started[0]["journal"].(map[string]any)
	if j["function"] != "GetEventsSince" {
		t.Fatalf("journal args = %v", h.world.Events.Started[0])
	}
	raw, err := os.ReadFile(fmt.Sprint(r["playtest_path"]))
	if err != nil {
		t.Fatalf("playtest.json: %v (%v)", err, r["playtest_path"])
	}
	var doc struct {
		Verdict string           `json:"verdict"`
		Events  []map[string]any `json:"events"`
		Sources map[string]any   `json:"event_sources"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var hit, cmd map[string]any
	for _, e := range doc.Events {
		switch e["kind"] {
		case "hit":
			hit = e
		case "game_command":
			cmd = e
		}
	}
	if doc.Verdict != "PASS" || hit == nil || hit["visual_t"] != 1.03 || cmd == nil || cmd["source"] != "server" ||
		doc.Sources["engine"] != "recorded" || doc.Sources["journal"] != "recorded" {
		t.Fatalf("playtest.json = %s", raw)
	}
}

// An old plugin: the engine source is unavailable, so a check on engine events is not
// scored (INSUFFICIENT_EVIDENCE), while the journal checks still are.
func TestPlaytestEventsWithoutTheEngineRecorder(t *testing.T) {
	h := eventsHarness(t, 7)
	r := runEvents(t, h)
	c := checks(r)
	if r["verdict"] != "INSUFFICIENT_EVIDENCE" || c["damage"]["insufficient"] != true || c["kills"]["passed"] != true ||
		!strings.Contains(fmt.Sprint(c["damage"]["message"]), "below 8") {
		t.Fatalf("verdict %v, checks %v", r["verdict"], c)
	}
}

// A journal gap in the window: the journal checks are not scored.
func TestPlaytestEventsWithAJournalGap(t *testing.T) {
	h := eventsHarness(t, 8)
	h.world.Events.Gaps = []map[string]any{{"source": "journal", "from_t": 0.0, "to_t": 1e9, "dropped": 40.0, "reason": "the game's journal overran"}}
	r := runEvents(t, h)
	c := checks(r)
	if r["verdict"] != "INSUFFICIENT_EVIDENCE" || c["kills"]["insufficient"] != true || c["damage"]["passed"] != true ||
		!strings.Contains(fmt.Sprint(c["kills"]["message"]), "40 dropped") {
		t.Fatalf("verdict %v, checks %v", r["verdict"], c)
	}
}

// Without a game_api the journal is unavailable and says why; without record_events no
// session starts and event checks are not scored.
func TestPlaytestEventsWithoutAGameAPI(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.PluginAPI = 8
	h.world.Events.Engine = []map[string]any{{"seq": 1.0, "t": 4.0, "kind": "damage", "actor": "Brute_2", "target": "Core"}}
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	sc := strings.Replace(eventsScenario, `"beats":[{"at_s":0.05,"game_command":{"name":"start_wave"}}],`, "", 1)
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": sc, "wait_s": 20}))
	r, _ := out["result"].(map[string]any)
	ev, _ := r["events"].(map[string]any)
	why, _ := ev["source_why"].(map[string]any)
	if r["verdict"] != "INSUFFICIENT_EVIDENCE" || why["journal"] == nil {
		t.Fatalf("no game_api: %v", r)
	}
	off := strings.Replace(sc, `"record_events":true,`, "", 1)
	out = structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": off, "wait_s": 20}))
	r, _ = out["result"].(map[string]any)
	if r["verdict"] != "INSUFFICIENT_EVIDENCE" || r["events"] != nil || len(h.world.Events.Started) != 1 {
		t.Fatalf("record_events off: %v", r)
	}
}

// R5.4: perf=true replays the scenario under CsvProfiler — no capture, no event
// recorder — and scores perf_csv.* from the CSV it wrote.
func TestPlaytestPerfPass(t *testing.T) {
	proj := t.TempDir()
	h := startHarness(t, harnessOpts{project: proj})
	h.world.PluginAPI = 8
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	var consoles []string
	captures := 0
	h.emu.Handle("capture_start", func(args map[string]any) (any, *bridgetest.OpError) {
		captures++
		return map[string]any{"session": "s1", "running": true}, nil
	})
	h.emu.Handle("console", func(args map[string]any) (any, *bridgetest.OpError) {
		cmd := fmt.Sprint(args["command"])
		consoles = append(consoles, cmd)
		if cmd == "csvprofile stop" {
			dir := proj + "/Saved/Profiling/CSV"
			_ = os.MkdirAll(dir, 0o755)
			rows := "FrameTime,GameThreadTime\n"
			for i := 0; i < 100; i++ {
				ms := 16.0
				if len(consoles) > 2 {
					ms = 16.5 // the pass with the event recorder
				}
				if i >= 95 {
					ms = 40
				}
				rows += fmt.Sprintf("%g,%g\n", ms, ms-2)
			}
			_ = os.WriteFile(fmt.Sprintf("%s/Profile(20261004_12000%d).csv", dir, len(consoles)), []byte(rows), 0o644)
		}
		return map[string]any{"output": []any{}}, nil
	})
	sc := `{"schema":"scenario/v1","name":"perf","mode":"pie","duration_s":0.3,"interval_s":0.1,"record_events":true,
	 "rubric":[{"id":"p95","kind":"max","path":"perf_csv.p95_frame_ms","params":{"value":20}},
	           {"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": sc, "perf": true, "wait_s": 25}))
	r, _ := out["result"].(map[string]any)
	pc, _ := r["perf_csv"].(map[string]any)
	vals, _ := pc["values"].(map[string]any)
	c := checks(r)
	if r["verdict"] != "PASS" || vals["p95_frame_ms"] != 16.0 || vals["frames"] != 100.0 || c["p95"]["passed"] != true {
		t.Fatalf("perf playtest = %v", r)
	}
	// Two profiled passes (record_events: the second measures the recorder): neither captures
	// frames; only the second records events.
	if strings.Join(consoles, "|") != "csvprofile start|csvprofile stop|csvprofile start|csvprofile stop" || captures != 1 || len(h.world.Events.Started) != 2 {
		t.Fatalf("the perf passes ran %v, %d captures, %d event sessions", consoles, captures, len(h.world.Events.Started))
	}
	if ov, _ := pc["recorder_overhead_ms"].(map[string]any); ov["p50"] != 0.5 {
		t.Fatalf("recorder overhead = %v", pc)
	}
	raw, _ := os.ReadFile(fmt.Sprint(r["playtest_path"]))
	if !strings.Contains(string(raw), `"perf_csv"`) {
		t.Fatalf("playtest.json has no perf_csv: %s", raw[:200])
	}
	// No perf pass: a perf_csv check is not scored.
	out = structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": sc, "wait_s": 25}))
	r, _ = out["result"].(map[string]any)
	if r["verdict"] != "INSUFFICIENT_EVIDENCE" || checks(r)["p95"]["insufficient"] != true {
		t.Fatalf("no perf pass = %v", r)
	}
	if e := errorOf(t, h.call(t, "playtest", map[string]any{"op": "run", "perf": true,
		"json": `{"schema":"scenario/v1","name":"ed","mode":"editor"}`})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("perf in editor mode = %v", e)
	}
}

// R5.5: a seeded batch runs the scenario once per seed (the engine's RNG seeded at each
// start) and reports verdict counts, check outcomes and the spread of what it measured.
func TestPlaytestSeededBatch(t *testing.T) {
	proj := gameProject(t, gameAPIJSON, "")
	h := startHarness(t, harnessOpts{project: proj, toolsets: []spec.Toolset{spec.Game}})
	h.world.PluginAPI = 8
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	// The engine source: one wave per run, cleared in 30 + seed seconds; seed 3 kills nothing.
	h.world.Events.OnSeed = func(seed int) {
		h.world.Events.Engine = []map[string]any{{"seq": 1.0, "t": 5.0, "kind": "damage", "target": "Core"}}
		if seed != 3 {
			h.world.Events.Engine = append(h.world.Events.Engine, map[string]any{"seq": 2.0, "t": 6.0, "kind": "destroyed", "actor": "Brute_1"})
		}
		h.world.Game.Journal = []map[string]any{{"seq": 1.0, "t": 40.0, "kind": "wave_end", "data": map[string]any{"wave": 1.0, "clear_s": 30.0 + float64(seed)}}}
	}
	sc := `{"schema":"scenario/v1","name":"batch","mode":"pie","duration_s":0.2,"interval_s":0.1,"record_events":true,
	 "rubric":[{"id":"destroyed","kind":"rate","path":"events.destroyed","params":{"min":0.1}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "batch", "json": sc, "seeds": []any{1, 2, 3}, "wait_s": 25}))
	r, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || r == nil {
		t.Fatalf("batch = %v", out)
	}
	verdicts, _ := r["verdicts"].(map[string]any)
	chk, _ := r["checks"].(map[string]any)
	dc, _ := chk["destroyed"].(map[string]any)
	evs, _ := r["events"].(map[string]any)
	destroyed, _ := evs["destroyed"].(map[string]any)
	if r["verdict"] != "FAIL" || verdicts["PASS"] != 2.0 || verdicts["FAIL"] != 1.0 || dc["passed"] != 2.0 || dc["failed"] != 1.0 ||
		destroyed["n"] != 3.0 || destroyed["mean"] == nil || fmt.Sprint(h.world.Events.Seeds) != "[1 2 3]" {
		t.Fatalf("batch = %v (seeds %v)", r, h.world.Events.Seeds)
	}
	waves, _ := r["waves"].([]any)
	w1, _ := waves[0].(map[string]any)
	cs, _ := w1["clear_s"].(map[string]any)
	if len(waves) != 1 || cs["mean"] != 32.0 || cs["stdev"] != 1.0 || cs["min"] != 31.0 || cs["max"] != 33.0 {
		t.Fatalf("batch = %v (seeds %v)", r, h.world.Events.Seeds)
	}
	raw, err := os.ReadFile(fmt.Sprint(r["batch_path"]))
	if err != nil || !strings.Contains(string(raw), `"seed": 3`) || !strings.HasPrefix(fmt.Sprint(r["batch_path"]), filepath.ToSlash(proj)) {
		t.Fatalf("batch file %v: %v", r["batch_path"], err)
	}
	for _, bad := range []map[string]any{{"op": "batch", "json": sc}, {"op": "batch", "json": sc, "seeds": make([]any, 21)},
		{"op": "run", "json": sc, "seeds": []any{1}}} {
		if e := errorOf(t, h.call(t, "playtest", bad)); e["code"] != "INVALID_ARGUMENT" {
			t.Fatalf("%v = %v", bad, e)
		}
	}
	// An old plugin: every run's seed fails, so every run fails (never an unseeded "seeded" run).
	h.world.PluginAPI = 7
	out = structured(t, h.call(t, "playtest", map[string]any{"op": "batch", "json": sc, "seeds": []any{9}, "wait_s": 25}))
	r, _ = out["result"].(map[string]any)
	runs, _ := r["runs"].([]any)
	if r["verdict"] != "FAIL" || len(runs) != 1 || !strings.Contains(fmt.Sprint(runs[0]), "seed 9") {
		t.Fatalf("old plugin batch = %v", r)
	}
}

// R5 review: a kind the game does not declare is never scored (a typo is not "zero").
func TestPlaytestUndeclaredEventKinds(t *testing.T) {
	h := eventsHarness(t, 8)
	h.world.Events.Kinds = []string{"hit", "death"} // no "kill"
	r := runEvents(t, h)
	c := checks(r)
	if r["verdict"] != "INSUFFICIENT_EVIDENCE" || c["kills"]["insufficient"] != true || !strings.Contains(fmt.Sprint(c["kills"]["message"]), `does not emit "kill"`) ||
		c["damage"]["passed"] != true {
		t.Fatalf("verdict %v, checks %v", r["verdict"], c)
	}
}

// R5 review: a cancelled batch is not a finished one, and runs without evidence for a
// kind stay out of that kind's spread.
func TestPlaytestBatchCancelAndExcludedRuns(t *testing.T) {
	proj := gameProject(t, gameAPIJSON, "")
	h := startHarness(t, harnessOpts{project: proj, toolsets: []spec.Toolset{spec.Game}})
	h.world.PluginAPI = 8
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	h.world.Events.Engine = []map[string]any{{"seq": 1.0, "t": 5.0, "kind": "damage", "target": "Core"}}
	h.world.Events.OnSeed = func(seed int) {
		h.world.Events.Gaps = nil
		if seed == 2 { // this run lost engine events: it cannot speak for damage
			h.world.Events.Gaps = []map[string]any{{"source": "engine", "from_t": 1.0, "to_t": 2.0, "dropped": 4.0}}
		}
	}
	sc := `{"schema":"scenario/v1","name":"batch","mode":"pie","duration_s":0.2,"interval_s":0.1,"record_events":true,
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "batch", "json": sc, "seeds": []any{1, 2, 3}, "wait_s": 25}))
	r, _ := out["result"].(map[string]any)
	evs, _ := r["events"].(map[string]any)
	dmg, _ := evs["damage"].(map[string]any)
	ex, _ := r["events_excluded_runs"].(map[string]any)
	if dmg["n"] != 2.0 || ex["damage"] != 1.0 {
		t.Fatalf("damage spread %v, excluded %v", dmg, ex)
	}
	// Cancel during the first run: the job is cancelled, never a PASS.
	h.world.Events.OnSeed = nil
	long := strings.Replace(sc, `"duration_s":0.2`, `"duration_s":3`, 1)
	job := structured(t, h.call(t, "playtest", map[string]any{"op": "batch", "json": long, "seeds": []any{1, 2}}))
	id := fmt.Sprint(job["job_id"])
	time.Sleep(700 * time.Millisecond)
	structured(t, h.call(t, "job", map[string]any{"op": "cancel", "job_id": id}))
	st := structured(t, h.call(t, "job", map[string]any{"op": "wait", "job_id": id, "wait_s": 20}))
	if st["state"] == "succeeded" || strings.Contains(fmt.Sprint(st), `verdict:PASS`) {
		t.Fatalf("a cancelled batch = %v", st)
	}
}

// R5 review round 3: a PIE that will not stop fails its run and ends the batch — the next
// seed must not play inside it.
func TestPlaytestBatchStopsWhenPIEWillNotStop(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 8
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	h.world.StickyPIE = true
	sc := `{"schema":"scenario/v1","name":"sticky","mode":"pie","duration_s":0.2,"interval_s":0.1,
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "batch", "json": sc, "seeds": []any{1, 2, 3}, "wait_s": 25}))
	r, _ := out["result"].(map[string]any)
	runs, _ := r["runs"].([]any)
	if r == nil || len(runs) != 1 || r["verdict"] != "FAIL" || len(h.world.Events.Seeds) != 1 ||
		!strings.Contains(fmt.Sprint(runs[0]), "did not stop") {
		t.Fatalf("a sticky PIE batch = %v (seeds %v)", out, h.world.Events.Seeds)
	}
}

// R5 review round 4: no perf pass after a PIE that would not stop.
func TestPlaytestPerfSkippedWhenPIEWillNotStop(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 8
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	var consoles []string
	h.emu.Handle("console", func(args map[string]any) (any, *bridgetest.OpError) {
		consoles = append(consoles, fmt.Sprint(args["command"]))
		return map[string]any{"output": []any{}}, nil
	})
	h.world.StickyPIE = true
	sc := `{"schema":"scenario/v1","name":"sticky","mode":"pie","duration_s":0.2,"interval_s":0.1,
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": sc, "perf": true, "wait_s": 25}))
	r, _ := out["result"].(map[string]any)
	if r == nil || r["verdict"] != "FAIL" || r["teardown_error"] == nil || len(consoles) != 0 || r["perf_csv"] != nil {
		t.Fatalf("perf after a stuck PIE = %v (console %v)", out, consoles)
	}
}

// A batch records gameplay events even when its scenario did not ask: its summary (wave
// clears, kills) comes from them (record pass 3: five seeded runs without events read
// no wave clear).
func TestPlaytestBatchRecordsEventsByDefault(t *testing.T) {
	proj := gameProject(t, gameAPIJSON, "")
	h := startHarness(t, harnessOpts{project: proj, toolsets: []spec.Toolset{spec.Game}})
	h.world.PluginAPI = 8
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	h.world.Events.OnSeed = func(seed int) {
		h.world.Game.Journal = []map[string]any{{"seq": 1.0, "t": 40.0, "kind": "wave_end", "data": map[string]any{"wave": 1.0, "clear_s": 30.0 + float64(seed)}}}
	}
	sc := `{"schema":"scenario/v1","name":"batch","mode":"pie","duration_s":0.2,"interval_s":0.1,
	 "rubric":[{"id":"w","kind":"reached","path":"gamestate.wave","params":{"value":1}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "batch", "json": sc, "seeds": []any{1, 2}, "wait_s": 25}))
	r, _ := out["result"].(map[string]any)
	waves, _ := r["waves"].([]any)
	if out["state"] != "succeeded" || r == nil || len(waves) != 1 || !strings.Contains(fmt.Sprint(r["note"]), "record_events") {
		t.Fatalf("batch without record_events = %v", out)
	}
}
