package tools

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/design"
)

func designCall(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := callToolDeps(t, Deps{}, name, args)
	if err != nil {
		t.Fatal(err)
	}
	return structuredMap(t, res)
}

func TestDesignAuditRepresentativeKinds(t *testing.T) {
	out := designCall(t, "design_audit", map[string]any{"kind": "decision", "input": map[string]any{"points": audit.MetronomeDecisions(30)}})
	if rep, _ := out["report"].(map[string]any); rep == nil || rep["pass"] == true {
		t.Fatalf("decision audit of a metronome must fail: %v", out)
	}
	out = designCall(t, "design_audit", map[string]any{"kind": "primitive", "input": map[string]any{"scene": audit.CubeScene()}})
	if rep, _ := out["report"].(map[string]any); rep == nil || rep["pass"] == true {
		t.Fatalf("primitive audit of a cube scene must fail: %v", out)
	}
	// Inputs are decoded strictly: a misspelled field is an INVALID_ARGUMENT, not a silent empty audit.
	out = designCall(t, "design_audit", map[string]any{"kind": "decision", "input": map[string]any{"pointz": []any{}}})
	if e, _ := out["error"].(map[string]any); e == nil || e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("unknown input field = %v", out)
	}
}

func TestDesignExploreSweepUsesSnakeCase(t *testing.T) {
	out := designCall(t, "design_explore", map[string]any{"op": "sweep", "scaffold": design.MetronomeScaffold()})
	gate, _ := out["gate"].(map[string]any)
	if gate == nil || gate["pass"] != false {
		t.Fatalf("a metronome scaffold must fail the balance gate: %v", out)
	}
	if _, ok := out["sweep"].(map[string]any)["axis_liveness"]; !ok {
		t.Fatalf("sweep keys must be snake_case: %v", out["sweep"])
	}
}
