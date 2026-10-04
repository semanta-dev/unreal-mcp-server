package e2e

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/app"
	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R3: the data toolset maps its parameters onto the companion ops, takes only each op's
// own parameters, and marks the edits without an undo step.
func TestDataToolsArgsAndUndoable(t *testing.T) {
	h := startHarness(t, harnessOpts{toolsets: []spec.Toolset{spec.Data}})
	got := map[string]map[string]any{}
	for _, op := range []string{"data_table_delete", "data_curve_keys", "data_input_mapping", "data_table_read", "data_set_settings"} {
		op := op
		h.emu.Handle(op, func(args map[string]any) (any, *bridgetest.OpError) {
			got[op] = args
			return map[string]any{"ok": true, "total": 2.0}, nil
		})
	}
	structured(t, h.call(t, "data_edit", map[string]any{"op": "table_delete", "asset": "/Game/Data/DT", "row_names": []any{"Wave_01"}}))
	if rows, _ := got["data_table_delete"]["rows"].([]any); len(rows) != 1 || rows[0] != "Wave_01" {
		t.Fatalf("table_delete args = %v", got["data_table_delete"])
	}
	structured(t, h.call(t, "data_edit", map[string]any{"op": "curve_keys", "asset": "/Game/Data/C", "points": []any{[]any{0.0, 1.0}}}))
	if keys, _ := got["data_curve_keys"]["keys"].([]any); len(keys) != 1 {
		t.Fatalf("curve_keys args = %v", got["data_curve_keys"])
	}
	out := structured(t, h.call(t, "data_edit", map[string]any{"op": "input_mapping", "action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC", "keys": []any{}}))
	if out["undoable"] != false || got["data_input_mapping"]["keys"] == nil {
		t.Fatalf("input_mapping = %v (args %v)", out, got["data_input_mapping"])
	}
	out = structured(t, h.call(t, "data_edit", map[string]any{"op": "settings", "class": "/Script/EngineSettings.GeneralProjectSettings",
		"properties": map[string]any{"ProjectVersion": "1.2"}}))
	if out["undoable"] != false || got["data_set_settings"]["class"] != "/Script/EngineSettings.GeneralProjectSettings" {
		t.Fatalf("settings = %v (args %v)", out, got["data_set_settings"])
	}
	// Each op takes only its own parameters; the required ones are required.
	for _, bad := range []map[string]any{
		{"op": "set_properties", "asset": "/Game/X", "properties": map[string]any{"a": 1}, "rows": map[string]any{}},
		{"op": "table_delete", "asset": "/Game/Data/DT"},
		{"op": "input_mapping", "action": "/Game/Input/IA_Dash", "context": "/Game/Input/IMC"},
	} {
		if e := errorOf(t, h.call(t, "data_edit", bad)); e["code"] != "INVALID_ARGUMENT" {
			t.Fatalf("data_edit %v = %v", bad, e)
		}
	}
	if e := errorOf(t, h.call(t, "data_query", map[string]any{"op": "curve", "asset": "/Game/Data/C", "row_names": []any{"x"}})); e["code"] != "INVALID_ARGUMENT" {
		t.Fatalf("data_query curve with rows = %v", e)
	}
	structured(t, h.call(t, "data_query", map[string]any{"op": "table", "asset": "/Game/Data/DT", "row_names": []any{"Wave_01"}, "limit": 5}))
	if got["data_table_read"]["limit"] != 5.0 || len(asAny(got["data_table_read"]["rows"])) != 1 {
		t.Fatalf("table read args = %v", got["data_table_read"])
	}
}

// table_delete is Destructive: refused under gate_policy require like every destructive op.
func TestDataTableDeleteIsGated(t *testing.T) {
	dir := gameProject(t, gameAPIJSON, `, "gate_policy": "require"`)
	_, gate, err := app.StartupPolicy(dir, "")
	if err != nil || gate == nil {
		t.Fatalf("startup policy: %v %v", gate, err)
	}
	h := startHarness(t, harnessOpts{project: dir, toolsets: []spec.Toolset{spec.Data}, gate: gate})
	if res := h.call(t, "data_edit", map[string]any{"op": "table_delete", "asset": "/Game/Data/DT", "row_names": []any{"Wave_01"}}); !res.IsError {
		t.Fatalf("table_delete under require: %s", text(res))
	}
}

func asAny(v any) []any {
	s, _ := v.([]any)
	return s
}
