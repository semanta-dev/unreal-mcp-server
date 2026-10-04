package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// T1 scenarios over the v2 core tools (editor, python, actor_*), end to end:
// MCP client → spec layer → bridge → real uexec → fake editor + op emulator.

func TestEditorStatusInstallsCompanionOnce(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	res := h.call(t, "editor", map[string]any{"op": "status"})
	if res.IsError {
		t.Fatalf("editor status failed: %s", text(res))
	}
	if got := structured(t, res)["current_level"]; got != "L_Test" {
		t.Fatalf("current_level = %v", got)
	}
	h.call(t, "editor", map[string]any{"op": "ping"})
	if n := h.emu.Installs(); n != 1 {
		t.Fatalf("companion installed %d times, want 1", n)
	}
	if h.emu.InstalledSource() != bridge.CompanionSource() {
		t.Fatal("editor received a module that differs from the embedded companion source")
	}
}

func TestActorLifecycle(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	res := h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor",
		"class": "/Script/Engine.StaticMeshActor", "label": "Cube1", "location": []any{100.0, 0.0, 0.0}})
	if res.IsError {
		t.Fatalf("spawn failed: %s", text(res))
	}
	sp := structured(t, res)["spawned"].(map[string]any)
	if sp["label"] != "Cube1" || sp["world"] != "editor" {
		t.Fatalf("spawned = %v", sp)
	}
	list := structured(t, h.call(t, "actor_query", map[string]any{"op": "list"}))
	if list["world"] != "editor" || list["count"] != float64(1) {
		t.Fatalf("list = %v", list)
	}
	if res := h.call(t, "actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": "Cube1"}); res.IsError {
		t.Fatalf("delete failed: %s", text(res))
	}
	if labels := h.world.Labels(); len(labels) != 0 {
		t.Fatalf("world still has actors: %v", labels)
	}
	want := []string{"actor_spawn", "actor_query", "actor_delete"}
	if got := h.emu.Calls(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dispatched ops = %v, want %v", got, want)
	}
}

func TestActorEditRequiresWorld(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "delete", "actor": "X"}))
	if e["code"] != "INVALID_ARGUMENT" || len(h.emu.Calls()) != 0 {
		t.Fatalf("a world-less edit must be rejected before reaching the editor: %v calls=%v", e, h.emu.Calls())
	}
}

func TestActorEditWorldMatrix(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Hero"})
	// No PIE yet: a pie edit is PIE_NOT_RUNNING.
	if e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "transform", "world": "pie", "actor": "Hero", "location": []any{1.0, 2.0, 3.0}})); e["code"] != "PIE_NOT_RUNNING" {
		t.Fatalf("code = %v", e["code"])
	}
	h.world.StartPIE()
	// Spawning into PIE needs plugin API 5 (R2.5); the spawn is in the game world only.
	if e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "pie", "class": "/Script/Engine.Actor"})); e["code"] != "PRECONDITION" {
		t.Fatalf("pie spawn with plugin API 3 = %v", e)
	}
	h.world.PluginAPI = 5
	if out := structured(t, h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "pie", "class": "/Script/Engine.Actor", "label": "Drop"})); out["world"] != "pie" {
		t.Fatalf("pie spawn = %v", out)
	}
	if e := errorOf(t, h.call(t, "actor_query", map[string]any{"op": "get", "world": "editor", "actor": "Drop"})); e["code"] != "NOT_FOUND" {
		t.Fatalf("a PIE spawn reached the editor level: %v", e)
	}
	h.world.PluginAPI = 3
	// Transform in PIE moves the PIE copy only; the editor actor stays put.
	res := h.call(t, "actor_edit", map[string]any{"op": "transform", "world": "pie", "actor": "Hero", "location": []any{5.0, 6.0, 7.0}})
	if res.IsError || structured(t, res)["world"] != "pie" {
		t.Fatalf("pie transform: %s", text(res))
	}
	ed := structured(t, h.call(t, "actor_query", map[string]any{"op": "get", "actor": "Hero"}))["actor"].(map[string]any)
	if loc := ed["location"].([]any); loc[0] != float64(0) {
		t.Fatalf("editing the PIE copy changed the editor actor: %v", loc)
	}
	// An editor object path resolves to the PIE copy.
	res = h.call(t, "actor_query", map[string]any{"op": "get", "world": "pie", "actor": ed["path"]})
	if res.IsError || !strings.Contains(structured(t, res)["actor"].(map[string]any)["path"].(string), "UEDPIE_0_") {
		t.Fatalf("editor path did not resolve in PIE: %s", text(res))
	}
}

func TestAmbiguousLabelIsConflict(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	for i := 0; i < 2; i++ {
		h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "Twin"})
	}
	if e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": "Twin"})); e["code"] != "CONFLICT" {
		t.Fatalf("an ambiguous label must never act on the first match: %v", e)
	}
	if len(h.world.Labels()) != 2 {
		t.Fatal("nothing should have been deleted")
	}
}

func TestOpErrorSurfacesCode(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	e := errorOf(t, h.call(t, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Game/Missing.Missing_C"}))
	if e["code"] != "NOT_FOUND" {
		t.Fatalf("code = %v", e["code"])
	}
	if d, _ := e["details"].(map[string]any); d["editor_code"] != "CLASS_UNRESOLVED" {
		t.Fatalf("details.editor_code = %v", d["editor_code"])
	}
}

func TestActorCallUntil(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	n := 0
	h.emu.Handle("actor_call", func(args map[string]any) (any, *bridgetest.OpError) {
		n++
		return map[string]any{"world": "pie", "result": n}, nil
	})
	res := h.call(t, "actor_call", map[string]any{"actor": "@gamestate", "function": "GetWave", "until": "result >= 3", "interval_s": 0.01})
	out := structured(t, res)
	if res.IsError || out["met"] != true || out["result"] != float64(3) {
		t.Fatalf("until should be met at the third call: %s", text(res))
	}
	// Never met → a normal met:false result, not an error.
	res = h.call(t, "actor_call", map[string]any{"actor": "@gamestate", "function": "GetWave", "until": "result < 0", "timeout_s": 0.3, "interval_s": 0.05})
	if res.IsError || structured(t, res)["met"] != false {
		t.Fatalf("unmet condition should be met:false: %s", text(res))
	}
}

func TestPythonRunReachesEditor(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.HandlePython(func(req uexectest.CommandRequest) uexectest.CommandResponse {
		return uexectest.CommandResponse{Success: true, Result: "None",
			Output: []uexectest.OutputEntry{{Type: "Info", Output: "hello from editor"}}}
	})
	res := h.call(t, "python", map[string]any{"op": "run", "code": "print('hello from editor')"})
	if res.IsError || !strings.Contains(text(res), "hello from editor") {
		t.Fatalf("python run: %s", text(res))
	}
	scripts := h.emu.PythonScripts()
	if len(scripts) == 0 || scripts[len(scripts)-1] != "print('hello from editor')" {
		t.Fatalf("editor did not receive the script: %q", scripts)
	}
	// recipe rejects run's parameters.
	if e := errorOf(t, h.call(t, "python", map[string]any{"op": "recipe", "path": "x.py", "code": "1"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("code = %v", e["code"])
	}
}

func TestEditorNotRunning(t *testing.T) {
	h := startHarness(t, harnessOpts{noEditor: true, cfg: func(c *uexec.Config) {
		c.DiscoveryTimeout = 300 * time.Millisecond
	}})
	e := errorOf(t, h.call(t, "editor", map[string]any{"op": "status"}))
	if e["code"] != "EDITOR_UNREACHABLE" || e["retryable"] != true {
		t.Fatalf("want retryable EDITOR_UNREACHABLE, got %v", e)
	}
	// health reports the outage as a result, not an error.
	res := h.call(t, "editor", map[string]any{"op": "health"})
	if res.IsError || structured(t, res)["healthy"] != false {
		t.Fatalf("health should report unhealthy: %s", text(res))
	}
}

// TestCompanionReinstalledAfterEditorRestart: the editor loses the module (restart);
// the next call detects the NameError, reinstalls once, and succeeds.
func TestCompanionReinstalledAfterEditorRestart(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	if res := h.call(t, "editor", map[string]any{"op": "status"}); res.IsError {
		t.Fatal(text(res))
	}
	h.emu.Restart()
	if res := h.call(t, "editor", map[string]any{"op": "status"}); res.IsError {
		t.Fatalf("call after restart failed: %s", text(res))
	}
	if n := h.emu.Installs(); n != 2 {
		t.Fatalf("installs = %d, want 2 (initial + after restart)", n)
	}
}

// TestCallsSurviveChannelDrops: the editor drops the channel once (a hiccup, no second
// client); calls reconnect and succeed, re-verifying but not reinstalling the module.
// (An editor that drops after EVERY reply reads as channel theft — §2.8 case 4.)
func TestCallsSurviveChannelDrops(t *testing.T) {
	h := startHarness(t, harnessOpts{fake: func(o *uexectest.Options) { o.CloseAfterReplies, o.CloseChannels = 2, 1 }})
	for i := 0; i < 3; i++ {
		if res := h.call(t, "editor", map[string]any{"op": "status"}); res.IsError {
			t.Fatalf("call %d failed: %s", i, text(res))
		}
	}
	if n := h.emu.Installs(); n != 1 {
		t.Fatalf("installs = %d, want 1", n)
	}
	if n := h.emu.VersionChecks(); n < 3 {
		t.Fatalf("version re-verified %d times, want >= 3", n)
	}
}

// TestHealthReportsThePluginAPI (R0.5): health reports the plugin API version the editor
// has and flags one older than expect_plugin.
func TestHealthReportsThePluginAPI(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	res := structured(t, h.call(t, "editor", map[string]any{"op": "health", "expect_plugin": 3}))
	if res["healthy"] != true || res["plugin_api"] != 3.0 {
		t.Fatalf("health with plugin API 3 = %v", res)
	}
	h.world.PluginAPI = 2
	res = structured(t, h.call(t, "editor", map[string]any{"op": "health", "expect_plugin": 3}))
	if res["healthy"] != false || !strings.Contains(fmt.Sprint(res["problems"]), "plugin API 2 < expected 3") {
		t.Fatalf("health with a stale plugin = %v", res)
	}
}
