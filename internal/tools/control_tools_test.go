package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestP7ControlToolRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	if !names["pie_input"] {
		t.Error("missing P7 tool: pie_input")
	}
}
