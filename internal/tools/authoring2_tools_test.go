package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestP3AuthoringToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range []string{
		"blueprint_create", "blueprint_set_defaults", "assign_subclass", "blueprint_add_component",
		"datatable_create", "datatable_import", "dataasset_create",
		"set_world_gamemode", "pie_set_property", "pie_destroy",
		"set_gamemode", "input_action", "input_axis", "gameplay_tag_add",
	} {
		if !names[want] {
			t.Errorf("missing P3 authoring tool: %q", want)
		}
	}
}
