package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

func TestP4VerificationToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range []string{"world_query", "perf_parse", "scenario_run", "scenario_list"} {
		if !names[want] {
			t.Errorf("missing P4 verification tool: %q", want)
		}
	}
}
