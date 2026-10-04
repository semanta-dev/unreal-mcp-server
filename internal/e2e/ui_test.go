package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R4: HUD bindings, mounting / the live tree, and the UI screenshot route.
func TestUILoopTools(t *testing.T) {
	h := startHarness(t, harnessOpts{toolsets: []spec.Toolset{spec.UI}})
	got := map[string]map[string]any{}
	for _, op := range []string{"widget_bind", "widget_mount", "widget_unmount", "widget_live_tree"} {
		op := op
		h.emu.Handle(op, func(args map[string]any) (any, *bridgetest.OpError) {
			got[op] = args
			return map[string]any{"bindings": []any{map[string]any{"TargetWidget": "WaveText"}}, "widgets": []any{}, "mounted": "W_0"}, nil
		})
	}
	out := structured(t, h.call(t, "widget_edit", map[string]any{"op": "bind", "asset": "/Game/UI/WBP_Hud",
		"bindings": []any{map[string]any{"widget": "WaveText", "field": "Text", "source": "game_state", "path": "WaveNumber", "conversion": "int_to_text"}}}))
	if out["undoable"] != false || got["widget_bind"]["blueprint"] != "/Game/UI/WBP_Hud" || len(got["widget_bind"]["bindings"].([]any)) != 1 {
		t.Fatalf("bind = %v (args %v)", out, got["widget_bind"])
	}
	if e := errorOf(t, h.call(t, "widget_edit", map[string]any{"op": "bind", "asset": "/Game/UI/WBP_Hud", "bindings": []any{map[string]any{}},
		"tree": map[string]any{"name": "Root"}})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("bind with a tree = %v", e)
	}
	structured(t, h.call(t, "widget_query", map[string]any{"op": "mount", "class": "/Game/UI/WBP_Hud", "z_order": 50}))
	if got["widget_mount"]["class"] != "/Game/UI/WBP_Hud" || got["widget_mount"]["z_order"] != 50.0 {
		t.Fatalf("mount args = %v", got["widget_mount"])
	}
	structured(t, h.call(t, "widget_query", map[string]any{"op": "live_tree"}))
	structured(t, h.call(t, "widget_query", map[string]any{"op": "unmount"}))
	if got["widget_live_tree"] == nil || got["widget_unmount"] == nil {
		t.Fatalf("live_tree / unmount not reached: %v", got)
	}
	if e := errorOf(t, h.call(t, "widget_query", map[string]any{"op": "mount"})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("mount without class = %v", e)
	}
}

// screenshot op=pie ui=true takes one frame of the game viewport with its UI, now, from
// the plugin (no capture timer: works while paused); HighResShot leaves UMG/Slate out.
func TestScreenshotWithUI(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.world.StartPIE()
	defer h.world.StopPIE()
	shot := filepath.Join(t.TempDir(), "ui_1.png")
	if err := bridgetest.WritePNG(shot, 90); err != nil {
		t.Fatal(err)
	}
	fail := false
	h.emu.Handle("pie_ui_shot", func(args map[string]any) (any, *bridgetest.OpError) {
		if fail {
			return nil, &bridgetest.OpError{Code: "EDITOR_ERROR", Message: "no frame with the UI: the viewport has no pixels (is its window minimized?)"}
		}
		return map[string]any{"file": shot, "width": 1280.0, "height": 720.0}, nil
	})
	res := h.call(t, "screenshot", map[string]any{"op": "pie", "ui": true})
	out := structured(t, res)
	img := false
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok && len(ic.Data) > 0 {
			img = true
		}
	}
	if !img || out["ui"] != true || out["file"] != shot || out["width"] != 1280.0 {
		t.Fatalf("ui screenshot = %v (image %v)", out, img)
	}
	if e := errorOf(t, h.call(t, "screenshot", map[string]any{"op": "pie", "ui": true, "width": 640})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("ui with a width = %v", e)
	}
	fail = true
	if e := errorOf(t, h.call(t, "screenshot", map[string]any{"op": "pie", "ui": true})); e["code"] != "OPERATION_FAILED" ||
		!strings.Contains(fmt.Sprint(e["message"]), "minimized") {
		t.Fatalf("a failed ui shot = %v", e)
	}
	if e := errorOf(t, h.call(t, "screenshot", map[string]any{"op": "viewport", "ui": true})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("ui on viewport = %v", e)
	}
}
