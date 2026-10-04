package e2e

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/build"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// T1 scenarios for the P5d tools: the editor-aware git_revert (§2.5), jobs, playtest
// (the contact sheet reaches the agent through the job) and offline analysis.

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// revertRepo is a project repo with a checkpoint (umcp/cp/1) holding a level and a
// header, then changes since: a modified level, a new asset, a modified header.
func revertRepo(t *testing.T) string {
	t.Helper()
	if !build.Available() {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	gitRun(t, dir, "config", "tag.gpgsign", "false")
	write := func(p, body string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Content/Maps/L_Arena.umap", "v1")
	write("Source/Game/Hero.h", "v1")
	write("Game.uproject", "{}")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "cp")
	gitRun(t, dir, "tag", "-a", "umcp/cp/1", "-m", "cp")
	write("Content/Maps/L_Arena.umap", "v2")
	write("Content/BP/BP_New.uasset", "new")
	gitRun(t, dir, "add", "Content/BP/BP_New.uasset") // staged since the checkpoint
	write("Source/Game/Hero.h", "v2")
	return dir
}

func read(t *testing.T, dir, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestGitRevertWithEditorUpButAssetsNotLoaded(t *testing.T) {
	dir := revertRepo(t)
	h := startHarness(t, harnessOpts{project: dir})
	pk := &bridgetest.Packages{}
	pk.Install(h.emu, h.world)
	plan := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "dry_run": true}))
	if plan["editor_restart"] != false || plan["changes"] != 3.0 || plan["rebuild_required"] != true {
		t.Fatalf("dry run = %v", plan)
	}
	if read(t, dir, "Content/Maps/L_Arena.umap") != "v2" {
		t.Fatal("dry_run changed files")
	}
	out := structured(t, h.call(t, "git_revert", map[string]any{"to": "umcp/cp/1", "wait_s": 20}))
	res, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || res == nil {
		t.Fatalf("revert job = %v", out)
	}
	if read(t, dir, "Content/Maps/L_Arena.umap") != "v1" || read(t, dir, "Source/Game/Hero.h") != "v1" || read(t, dir, "Content/BP/BP_New.uasset") != "<missing>" {
		t.Fatal("files not reverted to the checkpoint")
	}
	if res["editor_restarted"] != false || !strings.Contains(strings.Join(anyStrings(res["possibly_stale"]), ","), "/Game/Maps/L_Arena") {
		t.Fatalf("revert result = %v", res)
	}
	if bk := res["backup_dir"].(string); read(t, bk, "Content/Maps/L_Arena.umap") != "v2" {
		t.Fatalf("the pre-revert version was not backed up in %s", bk)
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestGitRevertRefusesToDiscardUnsavedWork(t *testing.T) {
	dir := revertRepo(t)
	h := startHarness(t, harnessOpts{project: dir})
	pk := &bridgetest.Packages{}
	pk.Install(h.emu, h.world)
	// The level is open in the editor and an unrelated new asset is unsaved.
	pk.Set([]string{"/Game/BP/BP_Unsaved"}, "/Game/Maps/L_Arena")
	e := errorOf(t, h.call(t, "git_revert", map[string]any{"to": "1"}))
	d, _ := e["details"].(map[string]any)
	if e["code"] != "PRECONDITION" || !strings.Contains(strings.Join(anyStrings(d["dirty"]), ","), "BP_Unsaved") {
		t.Fatalf("revert with unsaved work = %v", e)
	}
	if read(t, dir, "Content/Maps/L_Arena.umap") != "v2" {
		t.Fatal("a refused revert changed files")
	}
}

func TestGitRevertOnlyToCheckpoints(t *testing.T) {
	dir := revertRepo(t)
	h := startHarness(t, harnessOpts{noEditor: true, project: dir})
	for _, to := range []string{"HEAD~1", "main", "umcp/cp/x"} {
		if e := errorOf(t, h.call(t, "git_revert", map[string]any{"to": to})); e["code"] != "PRECONDITION" {
			t.Fatalf("to=%s: %v", to, e["code"])
		}
	}
	if e := errorOf(t, h.call(t, "git_revert", map[string]any{"to": "9"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("missing checkpoint = %v", e["code"])
	}
	// No editor running: the revert proceeds offline.
	out := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "wait_s": 20}))
	if out["state"] != "succeeded" || read(t, dir, "Source/Game/Hero.h") != "v1" {
		t.Fatalf("offline revert = %v", out)
	}
}

func TestJobTool(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	if e := errorOf(t, h.call(t, "job", map[string]any{"op": "status", "job_id": "j999"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("unknown job = %v", e["code"])
	}
	if e := errorOf(t, h.call(t, "job", map[string]any{"op": "status", "job_id": "j1", "wait_s": 1})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("status with wait_s = %v", e["code"])
	}
	if res := structured(t, h.call(t, "job", map[string]any{"op": "list"})); res["jobs"] != nil {
		t.Fatalf("fresh project has jobs: %v", res)
	}
}

const scenarioJSON = `{"schema":"scenario/v1","name":"smoke","mode":"pie","duration_s":0.2,"interval_s":0.1,
 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`

func TestPlaytestRunReturnsVerdictAndContactSheet(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	res := h.call(t, "playtest", map[string]any{"op": "run", "json": scenarioJSON, "wait_s": 20})
	out := structured(t, res)
	r, _ := out["result"].(map[string]any)
	if out["state"] != "succeeded" || r == nil || r["verdict"] != "PASS" {
		t.Fatalf("playtest = %v", out)
	}
	img := false
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok && ic.MIMEType == "image/png" && len(ic.Data) > 0 {
			img = true
		}
	}
	if !img {
		t.Fatal("the contact sheet did not reach the agent as image content")
	}
	if e := errorOf(t, h.call(t, "playtest", map[string]any{"op": "run", "json": `{"schema":"scenario/v1"}`})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("invalid scenario = %v", e["code"])
	}
}

// TestPlaytestBeatErrorsFailTheVerdict: a scenario whose beat failed did not play as
// written, so a passing rubric must not make it PASS (R0.6); beat_errors=warn opts in to
// WARN.
func TestPlaytestBeatErrorsFailTheVerdict(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	sc := `{"schema":"scenario/v1","name":"beats","mode":"pie","duration_s":0.2,"interval_s":0.1,
 "beats":[{"at_s":0,"exec":{"target":"NoSuchActor","ufunction":"StartWave"}}],
 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	for _, tc := range []struct{ mode, want string }{{"", "FAIL"}, {"warn", "WARN"}} {
		args := map[string]any{"op": "run", "json": sc, "wait_s": 20}
		if tc.mode != "" {
			args["beat_errors"] = tc.mode
		}
		out := structured(t, h.call(t, "playtest", args))
		r, _ := out["result"].(map[string]any)
		if r == nil || r["verdict"] != tc.want || r["beat_errors"] == nil || r["verdict_reasons"] == nil {
			t.Fatalf("beat_errors=%q: playtest = %v", tc.mode, out)
		}
		if rb, _ := r["rubric"].(map[string]any); rb["verdict"] != "PASS" {
			t.Fatalf("the rubric itself should still pass: %v", r["rubric"])
		}
	}
	if e := errorOf(t, h.call(t, "playtest", map[string]any{"op": "run", "json": sc, "beat_errors": "ignore"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("beat_errors=ignore = %v", e["code"])
	}
}

func TestAnalyzeOffline(t *testing.T) {
	h := startHarness(t, harnessOpts{noEditor: true, project: t.TempDir()})
	timeline := []any{map[string]any{"index": 0, "t_world": 0, "state": map[string]any{"gamestate": map[string]any{"wave": 1}}},
		map[string]any{"index": 1, "t_world": 1, "state": map[string]any{"gamestate": map[string]any{"wave": 2}}}}
	rubric := []any{map[string]any{"id": "w", "kind": "reached", "path": "gamestate.wave", "params": map[string]any{"value": 2}}}
	if res := structured(t, h.call(t, "analyze", map[string]any{"op": "rubric", "timeline": timeline, "rubric": rubric})); res["verdict"] != "PASS" {
		t.Fatalf("rubric = %v", res)
	}
	if res := structured(t, h.call(t, "analyze", map[string]any{"op": "scenarios"})); len(res["scenarios"].([]any)) != 0 {
		t.Fatalf("scenarios = %v", res)
	}
}

// TestGitRevertProjectInASubdirectory: the project is MyGame/ inside the repository —
// paths must be resolved relative to the project, not the repository root.
func TestGitRevertProjectInASubdirectory(t *testing.T) {
	if !build.Available() {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	gitRun(t, repo, "config", "tag.gpgsign", "false")
	proj := filepath.Join(repo, "My Game")
	write := func(p, body string) {
		full := filepath.Join(proj, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Content/Maps/L_Arena.umap", "v1")
	write("Game.uproject", "{}")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "cp")
	gitRun(t, repo, "tag", "-a", "umcp/cp/1", "-m", "cp")
	write("Content/Maps/L_Arena.umap", "v2")
	write("Content/BP/BP_Committed Since.uasset", "added")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "after the checkpoint")
	write("Content/BP/BP_Ünïcode New.uasset", "new")

	h := startHarness(t, harnessOpts{project: proj})
	pk := &bridgetest.Packages{}
	pk.Install(h.emu, h.world)
	plan := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "dry_run": true}))
	if plan["changes"] != 2.0 || !strings.Contains(strings.Join(anyStrings(plan["untracked_kept"]), "|"), "BP_Ünïcode New.uasset") {
		t.Fatalf("dry run = %v", plan) // the untracked asset is kept (one entry, spaces and all)
	}
	out := structured(t, h.call(t, "git_revert", map[string]any{"to": "1", "wait_s": 20}))
	if out["state"] != "succeeded" || read(t, proj, "Content/Maps/L_Arena.umap") != "v1" {
		t.Fatalf("nested revert = %v", out)
	}
	if res := out["result"].(map[string]any); !strings.Contains(strings.Join(anyStrings(res["possibly_stale"]), ","), "/Game/Maps/L_Arena") {
		t.Fatalf("package mapping lost for a nested project: %v", res)
	}
	if read(t, proj, "Content/BP/BP_Committed Since.uasset") != "<missing>" {
		t.Fatal("a file committed after the checkpoint must be deleted")
	}
	if read(t, proj, "Content/BP/BP_Ünïcode New.uasset") != "new" {
		t.Fatal("an untracked file must be kept")
	}
}

// Review R1 #3: a wait_until beat reads object paths (it polled pie_observe only, so an
// object-path beat never held and failed the run).
func TestPlaytestWaitUntilReadsObjectPaths(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	h.world.SetObject("@subsystem:AgentSubsystem", map[string]any{"Mode": "defend"})
	scenario := strings.Replace(scenarioJSON, `"rubric"`, `"beats":[{"wait_until":"@subsystem:AgentSubsystem.Mode == 'defend'","timeout_s":2}],"rubric"`, 1)
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": scenario, "wait_s": 20}))
	if r, _ := out["result"].(map[string]any); out["state"] != "succeeded" || r == nil || r["verdict"] != "PASS" {
		t.Fatalf("playtest with an object-path wait_until = %v", out)
	}
}

// R2.4: input and game_command beats, scheduled on the game's clock.
func TestPlaytestInputAndGameCommandBeats(t *testing.T) {
	h := startHarness(t, harnessOpts{project: gameProject(t, gameAPIJSON, ""), toolsets: []spec.Toolset{spec.Game}})
	h.world.PluginAPI = 5
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	scenario := `{"schema":"scenario/v1","name":"beats","mode":"pie","duration_s":1.5,"interval_s":0.1,
	 "beats":[{"at_world_s":0.2,"input":{"key":"MouseX","action":"axis","value":2.5,"duration_s":0.3}},
	          {"at_world_s":0.3,"input":{"position":[100,200],"action":"click"}},
	          {"at_world_s":0.4,"input":{"widget":"StartButton"}},
	          {"at_world_s":0.5,"game_command":{"name":"start_wave"}}],
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": scenario, "wait_s": 20}))
	if r, _ := out["result"].(map[string]any); out["state"] != "succeeded" || r == nil || r["verdict"] != "PASS" {
		t.Fatalf("playtest with input/game_command beats = %v", out)
	}
	in := h.world.RecordedInputs()
	if len(in) != 3 || in[0]["op"] != "pie_input" || in[0]["value"] != 2.5 || in[1]["op"] != "pie_cursor" || in[2]["widget"] != "StartButton" {
		t.Fatalf("the game received %v", in)
	}
	if h.world.Game.Runs != 1 {
		t.Fatalf("the game_command beat ran %d times", h.world.Game.Runs)
	}
}

// A game clock that does not reach a beat's at_world_s within the window (paused or
// slowed) fails the beat — it never runs early — and mixing clocks is a scenario error.
func TestPlaytestWorldClock(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.PluginAPI = 5
	h.world.WorldTimeScale = 0.01 // a slowed game
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 2, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(2)}}
	}}
	rec.Install(h.emu)
	scenario := `{"schema":"scenario/v1","name":"slow","mode":"pie","duration_s":0.5,"interval_s":0.1,
	 "beats":[{"at_world_s":5,"input":{"key":"W"}}],
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": scenario, "wait_s": 20}))
	r, _ := out["result"].(map[string]any)
	if r == nil || r["verdict"] != "FAIL" || !strings.Contains(fmt.Sprint(r["beat_errors"]), "game clock") || len(h.world.RecordedInputs()) != 0 {
		t.Fatalf("a beat the game clock never reached = %v (inputs %v)", out, h.world.RecordedInputs())
	}
	mixed := `{"schema":"scenario/v1","name":"mixed","beats":[{"at_s":1,"console":"x"},{"at_world_s":1,"console":"y"}]}`
	if e := errorOf(t, h.call(t, "playtest", map[string]any{"op": "run", "json": mixed})); e["code"] != "INVALID_ARGUMENT" || !strings.Contains(fmt.Sprint(e), "one clock") {
		t.Fatalf("mixed clocks = %v", e)
	}
}

// R2 review: at_world_s counts game time in one world — a map travel fails the beat.
func TestPlaytestWorldClockAcrossTravel(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.PluginAPI = 5
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 2, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(2)}}
	}}
	rec.Install(h.emu)
	time.AfterFunc(500*time.Millisecond, func() { h.world.TravelTo("/Game/Maps/UEDPIE_0_L_City.L_City") })
	scenario := `{"schema":"scenario/v1","name":"travel","mode":"pie","duration_s":1.5,"interval_s":0.1,
	 "beats":[{"at_world_s":1.2,"input":{"key":"W"}}],
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": scenario, "wait_s": 20}))
	r, _ := out["result"].(map[string]any)
	if r == nil || !strings.Contains(fmt.Sprint(r["beat_errors"]), "world changed") || len(h.world.RecordedInputs()) != 0 {
		t.Fatalf("a beat after a map travel = %v", out)
	}
}

// R2 review round 2: the same map restarting (same world path, clock back to 0) fails
// the beat too — never a beat that silently runs world0 seconds late.
func TestPlaytestWorldClockRestart(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.PluginAPI = 5
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 2, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(2)}}
	}}
	rec.Install(h.emu)
	time.AfterFunc(600*time.Millisecond, h.world.RestartClock)
	scenario := `{"schema":"scenario/v1","name":"restart","mode":"pie","duration_s":1.5,"interval_s":0.1,
	 "beats":[{"at_world_s":1.2,"input":{"key":"W"}}],
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": scenario, "wait_s": 20}))
	r, _ := out["result"].(map[string]any)
	if r == nil || !strings.Contains(fmt.Sprint(r["beat_errors"]), "clock went back") || len(h.world.RecordedInputs()) != 0 {
		t.Fatalf("a beat after a level restart = %v", out)
	}
}

// pie op=aim turns the view onto a target only through MouseX/MouseY axis input,
// measuring the gain (here 0.175°/unit with an inverted Y) instead of assuming one.
func TestPieAimTurnsTheViewWithMouseInput(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 5
	h.world.AddActor("Enemy_1", "/Script/Game.EnemyCharacter", [3]float64{1000, 600, 300}, nil)
	h.world.AddActor("Enemy_2", "/Script/Game.EnemyCharacter", [3]float64{-3000, -100, 0}, nil)
	h.world.StartPIE()
	out := structured(t, h.call(t, "pie", map[string]any{"op": "aim", "class": "EnemyCharacter"}))
	if out["aimed"] != true || out["target"] != "Enemy_1" {
		t.Fatalf("aim = %v", out)
	}
	if math.Abs(out["yaw_error"].(float64)) > 1 || math.Abs(out["pitch_error"].(float64)) > 1 {
		t.Fatalf("aim errors = %v", out)
	}
	for _, in := range h.world.RecordedInputs() {
		if in["op"] != "pie_input" || in["action"] != "axis" || (in["key"] != "MouseX" && in["key"] != "MouseY") {
			t.Fatalf("aim sent %v", in)
		}
	}
	// The far target: a 180° turn, with the gain the first aim measured.
	out = structured(t, h.call(t, "pie", map[string]any{"op": "aim", "actor": "Enemy_2"}))
	if out["aimed"] != true || out["steps"].(float64) > 4 {
		t.Fatalf("second aim = %v", out)
	}
	if e := errorOf(t, h.call(t, "pie", map[string]any{"op": "aim"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("aim without a target = %v", e)
	}
	if e := errorOf(t, h.call(t, "pie", map[string]any{"op": "aim", "class": "EnemyCharacter", "key": "W"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("aim with key = %v", e)
	}
}

// A view the mouse cannot turn (a cursor mode, an unbound axis) is a PRECONDITION, not
// a silent miss.
func TestPieAimRefusesWhenTheMouseDoesNotTurnTheView(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 5
	h.world.LookIgnored = true
	h.world.AddActor("Enemy_1", "/Script/Game.EnemyCharacter", [3]float64{1000, 600, 300}, nil)
	h.world.StartPIE()
	if e := errorOf(t, h.call(t, "pie", map[string]any{"op": "aim", "actor": "Enemy_1"})); e["code"] != "PRECONDITION" {
		t.Fatalf("aim with look ignored = %v", e)
	}
}

// A playtest aim step turns the view onto the nearest target with mouse input and keeps
// on it for duration_s (aesir_ttk: kills inside a recorded playtest need aiming).
func TestPlaytestAimBeat(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 5
	h.world.AddActor("Enemy_1", "/Script/Game.EnemyCharacter", [3]float64{1000, 600, 300}, nil)
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	scenario := `{"schema":"scenario/v1","name":"aim","mode":"pie","duration_s":1.5,"interval_s":0.1,
	 "beats":[{"at_s":0.1,"input":{"key":"LeftMouseButton","action":"hold","duration_s":1}},
	          {"at_s":0.2,"input":{"class":"EnemyCharacter","duration_s":0.6}}],
	 "rubric":[{"id":"waves","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": scenario, "wait_s": 20}))
	if r, _ := out["result"].(map[string]any); out["state"] != "succeeded" || r == nil || r["verdict"] != "PASS" {
		t.Fatalf("playtest with an aim beat = %v", out)
	}
	axes := 0
	for _, in := range h.world.RecordedInputs() {
		if in["action"] == "axis" {
			axes++
		}
	}
	yaw := math.Atan2(600, 1000) * 180 / math.Pi
	if axes == 0 || math.Abs(h.world.ViewYaw-yaw) > 1 {
		t.Fatalf("aim beat: %d axis inputs, view yaw %.2f (want %.2f)", axes, h.world.ViewYaw, yaw)
	}
}

// The player dies between aims: the view stops turning with a known gain. aim stops
// with a PRECONDITION instead of learning a near-zero gain and escalating the input
// (live: 5000-unit ticks at a dead player's view).
func TestPieAimStopsWhenTheViewStopsResponding(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 5
	h.world.AddActor("Enemy_1", "/Script/Game.EnemyCharacter", [3]float64{1000, 600, 300}, nil)
	h.world.AddActor("Enemy_2", "/Script/Game.EnemyCharacter", [3]float64{-3000, -100, 0}, nil)
	h.world.StartPIE()
	if out := structured(t, h.call(t, "pie", map[string]any{"op": "aim", "actor": "Enemy_1"})); out["aimed"] != true {
		t.Fatalf("calibrating aim = %v", out)
	}
	h.world.LookIgnored = true
	n := len(h.world.RecordedInputs())
	e := errorOf(t, h.call(t, "pie", map[string]any{"op": "aim", "actor": "Enemy_2"}))
	if e["code"] != "PRECONDITION" || !strings.Contains(fmt.Sprint(e["message"]), "look_ignored: true") {
		t.Fatalf("aim at a view that stopped turning = %v", e)
	}
	if sent := len(h.world.RecordedInputs()) - n; sent > 2 {
		t.Fatalf("aim kept sending input (%d) to a view that does not turn", sent)
	}
}

// analyze op=events reads a playtest's recorded events (the file or its capture folder),
// filtered by kind, so judging a run needs no python (final eval aesir_player_damage).
func TestAnalyzeEventsReadsAPlaytest(t *testing.T) {
	h := startHarness(t, harnessOpts{noEditor: true, project: t.TempDir()})
	dir := t.TempDir()
	doc := `{"event_window":[0,30],"event_sources":{"engine":"recorded"},"events":[
	 {"t":3,"kind":"damage","by_player":true,"target":"Enemy_1","data":{"damage":31.25}},
	 {"t":1,"kind":"damage","by_player":true,"target":"Wall_East","data":{"damage":25}},
	 {"t":2,"kind":"weapon_fire","by_player":true},
	 {"t":4,"kind":"kill","by_player":true,"target":"Enemy_1"}]}`
	if err := os.WriteFile(filepath.Join(dir, "playtest.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	out := structured(t, h.call(t, "analyze", map[string]any{"op": "events", "path": dir, "kinds": []any{"damage", "kill"}}))
	evs, _ := out["events"].([]any)
	if out["matched"] != 3.0 || len(evs) != 3 || evs[0].(map[string]any)["target"] != "Wall_East" {
		t.Fatalf("events = %v", out)
	}
	if c := out["counts"].(map[string]any); c["weapon_fire"] != 1.0 || c["damage"] != 2.0 {
		t.Fatalf("counts = %v", c)
	}
	if e := errorOf(t, h.call(t, "analyze", map[string]any{"op": "events", "path": filepath.Join(dir, "nope.json")})); e["code"] != "NOT_FOUND" {
		t.Fatalf("missing file = %v", e)
	}
}

// The playtest result carries the timeline's frame count and final state, not every
// frame (102 KB live, back through each job wait); playtest.json holds the frames and
// analyze op=rubric path= scores them.
func TestPlaytestResultCompactsTheTimeline(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	rec := &bridgetest.Recorder{Dir: t.TempDir(), Frames: 3, State: func(i int) map[string]any {
		return map[string]any{"gamestate": map[string]any{"wave": float64(i)}}
	}}
	rec.Install(h.emu)
	sc := `{"schema":"scenario/v1","name":"c","mode":"pie","duration_s":0.5,"interval_s":0.1,
	 "rubric":[{"id":"w","kind":"reached","path":"gamestate.wave","params":{"value":2}}]}`
	out := structured(t, h.call(t, "playtest", map[string]any{"op": "run", "json": sc, "wait_s": 20}))
	r, _ := out["result"].(map[string]any)
	if r == nil || r["timeline"] != nil || r["timeline_frames"] != 3.0 || r["final_state"] == nil || r["playtest_path"] == nil {
		t.Fatalf("playtest result = %v", out)
	}
	if next := fmt.Sprint(r["next"]); !strings.Contains(next, "analyze op=rubric path=") {
		t.Fatalf("next steps = %v", r["next"])
	}
	rubric := []any{map[string]any{"id": "w", "kind": "reached", "path": "gamestate.wave", "params": map[string]any{"value": 2}}}
	if res := structured(t, h.call(t, "analyze", map[string]any{"op": "rubric", "path": r["playtest_path"], "rubric": rubric})); res["verdict"] != "PASS" {
		t.Fatalf("rubric from playtest_path = %v", res)
	}
	if e := errorOf(t, h.call(t, "analyze", map[string]any{"op": "rubric", "rubric": rubric})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("rubric with neither timeline nor path = %v", e)
	}
}

// aim right after a wave starts: the enemies are still spawning. aim waits for the first
// one (live: a NOT_FOUND straight after start_wave cost the agent turns).
func TestPieAimWaitsForATargetToSpawn(t *testing.T) {
	h := startHarness(t, harnessOpts{project: t.TempDir()})
	h.world.PluginAPI = 5
	h.world.StartPIE()
	go func() {
		time.Sleep(600 * time.Millisecond)
		h.world.SpawnInPIE("EnemyCharacter0", "/Script/Game.EnemyCharacter", [3]float64{1000, 600, 300})
	}()
	if out := structured(t, h.call(t, "pie", map[string]any{"op": "aim", "class": "EnemyCharacter"})); out["aimed"] != true {
		t.Fatalf("aim at a spawning target = %v", out)
	}
}
