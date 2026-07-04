package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestP5RobustnessToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range []string{"editor_events", "editor_ping", "scene_snapshot", "scene_restore", "health_check"} {
		if !names[want] {
			t.Errorf("missing P5 robustness tool: %q", want)
		}
	}
}
