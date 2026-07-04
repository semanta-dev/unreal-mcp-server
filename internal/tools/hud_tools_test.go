package tools

import (
	"reflect"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestHUDToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range []string{
		"widget_create", "widget_compose", "widget_compile", "widget_tree", "widget_describe",
	} {
		if !names[want] {
			t.Errorf("missing HUD authoring tool: %q", want)
		}
	}
}

func TestWidgetCreateArgs(t *testing.T) {
	// Minimal: only dest -> Python fills parent_class/root_panel defaults.
	got := widgetCreateArgs(widgetCreateIn{Dest: "/Game/UI/WBP_HUD"})
	want := map[string]any{"dest": "/Game/UI/WBP_HUD"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("minimal widget_create args = %v, want %v", got, want)
	}
	// Full: overrides threaded through.
	got = widgetCreateArgs(widgetCreateIn{Dest: "/Game/UI/WBP_HUD", ParentClass: "/Script/UMG.UserWidget", RootPanel: "CanvasPanel"})
	want = map[string]any{"dest": "/Game/UI/WBP_HUD", "parent_class": "/Script/UMG.UserWidget", "root_panel": "CanvasPanel"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full widget_create args = %v, want %v", got, want)
	}
}

func TestWidgetComposeArgs(t *testing.T) {
	tree := map[string]any{"name": "Root", "class": "CanvasPanel", "children": []any{
		map[string]any{"name": "Ammo", "class": "TextBlock", "props": map[string]any{"Text": "24 / 30"}},
	}}
	// Defaults: no mode/prune/defer/remove keys (Python applies full/false).
	got := widgetComposeArgs(widgetComposeIn{Blueprint: "/Game/UI/WBP_HUD", Tree: tree})
	if got["mode"] != nil || got["prune"] != nil || got["defer"] != nil || got["remove"] != nil {
		t.Fatalf("default compose must omit optional keys, got %v", got)
	}
	if !reflect.DeepEqual(got["tree"], tree) || got["blueprint"] != "/Game/UI/WBP_HUD" {
		t.Fatalf("compose must pass tree+blueprint verbatim, got %v", got)
	}
	// Destructive patch with explicit removes + defer.
	got = widgetComposeArgs(widgetComposeIn{Blueprint: "/Game/UI/WBP_HUD", Tree: tree, Mode: "patch", Remove: []string{"Old"}, Prune: true, Defer: true})
	if got["mode"] != "patch" || got["prune"] != true || got["defer"] != true {
		t.Fatalf("compose flags not threaded: %v", got)
	}
	if rm, ok := got["remove"].([]string); !ok || len(rm) != 1 || rm[0] != "Old" {
		t.Fatalf("compose remove not threaded: %v", got["remove"])
	}
}

func TestWidgetTreeArgs(t *testing.T) {
	got := widgetTreeArgs(widgetTreeIn{Blueprint: "/Game/UI/WBP_HUD"})
	if got["mode"] != nil || got["restore_token"] != nil {
		t.Fatalf("default widget_tree (get) must omit mode/restore_token, got %v", got)
	}
	got = widgetTreeArgs(widgetTreeIn{Blueprint: "/Game/UI/WBP_HUD", Mode: "restore", RestoreToken: "snap-123"})
	if got["mode"] != "restore" || got["restore_token"] != "snap-123" {
		t.Fatalf("restore args not threaded: %v", got)
	}
}

func TestWidgetDescribeArgs(t *testing.T) {
	if got := widgetDescribeArgs(widgetDescribeIn{}); len(got) != 0 {
		t.Fatalf("empty widget_describe (full palette) must send no args, got %v", got)
	}
	if got := widgetDescribeArgs(widgetDescribeIn{WidgetClass: "ProgressBar"}); got["widget_class"] != "ProgressBar" {
		t.Fatalf("widget_describe class not threaded: %v", got)
	}
}
