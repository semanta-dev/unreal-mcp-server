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
	if out := text(res); !strings.Contains(out, "CLASS_UNRESOLVED") {
		t.Fatalf("error text lacks code: %s", out)
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
	// Baseline for the P3 envelope (→ EDITOR_UNREACHABLE): today the text is uexec's.
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

// TestCallsSurviveChannelDrops: the editor drops the command channel after every
// reply; each command reconnects transparently and the module is still installed
// only once (version re-verified, not reinstalled).
func TestCallsSurviveChannelDrops(t *testing.T) {
	h := startHarness(t, harnessOpts{fake: func(o *uexectest.Options) { o.CloseAfterReplies = 1 }})
	for i := 0; i < 3; i++ {
		if res := h.call(t, "editor_status", nil); res.IsError {
			t.Fatalf("call %d failed: %s", i, text(res))
		}
	}
	if n := h.emu.Installs(); n != 1 {
		t.Fatalf("installs = %d, want 1", n)
	}
	// Every command reconnects (CloseAfterReplies=1), so each call re-verifies the
	// sentinel once: initial check + post-install confirm, then one per later call.
	if n := h.emu.VersionChecks(); n < 4 {
		t.Fatalf("version re-verified %d times, want >= 4 (once per reconnect-bearing call)", n)
	}
}
