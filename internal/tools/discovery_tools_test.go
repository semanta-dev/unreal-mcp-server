package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestDiscoveryToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range []string{
		"project_map", "asset_query", "asset_deps", "asset_tags",
		"reflect_class", "enum_values", "map_gameplay", "find_actors",
	} {
		if !names[want] {
			t.Errorf("missing P2 discovery tool: %q", want)
		}
	}
}
