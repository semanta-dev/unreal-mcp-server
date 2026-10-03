package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// P0 baseline scenarios: they drive the v1 tool surface end-to-end so the
// P1–P4 refactors are guarded by real-path tests before any behaviour changes.

func TestEditorStatusInstallsCompanionOnce(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	res := h.call(t, "editor_status", nil)
	if res.IsError {
		t.Fatalf("editor_status failed: %s", text(res))
	}
	if got := structured(t, res)["level"]; got != "/Game/Maps/L_Test" {
		t.Fatalf("level = %v", got)
	}
	h.call(t, "editor_status", nil)
	if n := h.emu.Installs(); n != 1 {
		t.Fatalf("companion installed %d times, want 1", n)
	}
	if h.emu.InstalledSource() != bridge.CompanionSource() {
		t.Fatal("editor received a module that differs from the embedded companion source")
	}
}

func TestActorLifecycle(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	res := h.call(t, "spawn_actor", map[string]any{
		"class_path": "/Script/Engine.StaticMeshActor", "label": "Cube1", "x": 100.0, "y": 0.0,
	})
	if res.IsError {
		t.Fatalf("spawn failed: %s", text(res))
	}
	if got := structured(t, res)["label"]; got != "Cube1" {
		t.Fatalf("spawned label = %v", got)
	}
	if out := text(h.call(t, "list_actors", nil)); !strings.Contains(out, "Cube1") {
		t.Fatalf("list_actors missing Cube1: %s", out)
	}
	if res := h.call(t, "delete_actor", map[string]any{"actor_label": "Cube1"}); res.IsError {
		t.Fatalf("delete failed: %s", text(res))
	}
	if labels := h.world.Labels(); len(labels) != 0 {
		t.Fatalf("world still has actors: %v", labels)
	}
	want := []string{"spawn_actor", "list_actors", "delete_actor"}
	if got := h.emu.Calls(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dispatched ops = %v, want %v", got, want)
	}
}

func TestOpErrorSurfacesCode(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	res := h.call(t, "spawn_actor", map[string]any{"class_path": "/Game/Missing.Missing_C"})
	if !res.IsError {
		t.Fatalf("expected tool error, got: %s", text(res))
	}
	e := errorOf(t, res)
	if e["code"] != "NOT_FOUND" {
		t.Fatalf("code = %v, want NOT_FOUND (%s)", e["code"], text(res))
	}
	if d, _ := e["details"].(map[string]any); d["editor_code"] != "CLASS_UNRESOLVED" {
		t.Fatalf("details.editor_code = %v, want CLASS_UNRESOLVED", d["editor_code"])
	}
}

func TestExecutePythonReachesEditor(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.HandlePython(func(req uexectest.CommandRequest) uexectest.CommandResponse {
		return uexectest.CommandResponse{Success: true, Result: "None",
			Output: []uexectest.OutputEntry{{Type: "Info", Output: "hello from editor"}}}
	})
	res := h.call(t, "execute_python", map[string]any{"code": "print('hello from editor')"})
	if res.IsError {
		t.Fatalf("execute_python failed: %s", text(res))
	}
	if out := text(res); !strings.Contains(out, "hello from editor") {
		t.Fatalf("output = %q", out)
	}
	scripts := h.emu.PythonScripts()
	if len(scripts) == 0 || scripts[len(scripts)-1] != "print('hello from editor')" {
		t.Fatalf("editor did not receive the script: %q", scripts)
	}
}

func TestEditorNotRunning(t *testing.T) {
	h := startHarness(t, harnessOpts{noEditor: true, cfg: func(c *uexec.Config) {
		c.DiscoveryTimeout = 300 * time.Millisecond
	}})
	res := h.call(t, "editor_status", nil)
	if !res.IsError {
		t.Fatalf("expected an error with no editor, got: %s", text(res))
	}
	e := errorOf(t, res)
	if e["code"] != "EDITOR_UNREACHABLE" || e["retryable"] != true {
		t.Fatalf("want retryable EDITOR_UNREACHABLE, got %v", e)
	}
	if out := text(res); !strings.Contains(out, uexec.ErrEditorNotFound.Error()) {
		t.Fatalf("error text does not identify a missing editor: %q", out)
	}
}

// TestCompanionReinstalledAfterEditorRestart: the editor loses the hot-loaded module
// (restart) — the next call detects the NameError, reinstalls once, and succeeds.
func TestCompanionReinstalledAfterEditorRestart(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	if res := h.call(t, "editor_status", nil); res.IsError {
		t.Fatal(text(res))
	}
	h.emu.Restart()
	if res := h.call(t, "editor_status", nil); res.IsError {
		t.Fatalf("call after restart failed: %s", text(res))
	}
	if n := h.emu.Installs(); n != 2 {
		t.Fatalf("installs = %d, want 2 (initial + after restart)", n)
	}
}

// TestCallsSurviveChannelDrops: the editor drops the command channel once (a
// hiccup, no second client); calls reconnect and succeed, and the module is
// re-verified on the new channel but not reinstalled. (An editor that drops after
// EVERY reply now reads as channel theft — plan §2.8 case 4 — and fails fast.)
func TestCallsSurviveChannelDrops(t *testing.T) {
	h := startHarness(t, harnessOpts{fake: func(o *uexectest.Options) { o.CloseAfterReplies, o.CloseChannels = 2, 1 }})
	for i := 0; i < 3; i++ {
		if res := h.call(t, "editor_status", nil); res.IsError {
			t.Fatalf("call %d failed: %s", i, text(res))
		}
	}
	if n := h.emu.Installs(); n != 1 {
		t.Fatalf("installs = %d, want 1", n)
	}
	// The drop forced one reconnect, so the sentinel was re-checked on the new channel:
	// initial check + post-install confirm + one re-verification.
	if n := h.emu.VersionChecks(); n < 3 {
		t.Fatalf("version re-verified %d times, want >= 3", n)
	}
}
