package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
)

// v7ToolNames are the tools added for playtest capture, high-level design, and
// tighter editor integration. Registration must not need a live editor.
var v7ToolNames = []string{
	"capture", "screenshot", "scene", "scene_clear",
	"playtest", "analyze",
}

func TestV7ToolsRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b, ProjectDir: t.TempDir()})
	for _, want := range v7ToolNames {
		if !names[want] {
			t.Errorf("missing v7 tool: %q", want)
		}
	}
}

func TestFailedFrameIndices(t *testing.T) {
	r := eval.Report{Checks: []eval.CheckResult{
		{ID: "a", Passed: true, Evidence: &eval.Evidence{FrameIndex: 1}},   // passed -> not marked
		{ID: "b", Passed: false, Evidence: &eval.Evidence{FrameIndex: 5}},  // failed -> 5
		{ID: "c", Passed: false, Evidence: &eval.Evidence{FrameIndex: 5}},  // dup -> once
		{ID: "d", Passed: false, Evidence: &eval.Evidence{FrameIndex: -1}}, // log check -> skip
		{ID: "e", Passed: false, Evidence: nil},                            // no evidence -> skip
		{ID: "f", Passed: false, Evidence: &eval.Evidence{FrameIndex: 8}},
	}}
	got := failedFrameIndices(r)
	if len(got) != 2 || got[0] != 5 || got[1] != 8 {
		t.Fatalf("failedFrameIndices = %v, want [5 8]", got)
	}
}

func TestReportToJSON(t *testing.T) {
	r := eval.Report{Verdict: "FAIL", Checks: []eval.CheckResult{
		{ID: "wave", Kind: "reached", Severity: "fail", Passed: false, Message: "never reached",
			Evidence: &eval.Evidence{FrameIndex: 3, TWorld: 1.5, Value: "Active"}},
	}}
	m := reportToJSON(r)
	if m["verdict"] != "FAIL" {
		t.Errorf("verdict = %v", m["verdict"])
	}
	checks := m["checks"].([]map[string]any)
	if len(checks) != 1 || checks[0]["id"] != "wave" || checks[0]["passed"] != false {
		t.Fatalf("unexpected checks: %v", checks)
	}
	ev := checks[0]["evidence"].(map[string]any)
	if ev["frame"] != 3 {
		t.Errorf("evidence frame = %v, want 3", ev["frame"])
	}
}
