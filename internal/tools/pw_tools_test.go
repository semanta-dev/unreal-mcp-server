package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestPWToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range []string{"instances_count", "instances_list", "scene_digest", "pie_verify", "image_compare"} {
		if !names[want] {
			t.Errorf("missing PW tool: %q", want)
		}
	}
}
