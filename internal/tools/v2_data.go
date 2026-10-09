package tools

import (
	"context"
	"fmt"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// Data and logic authoring (REMEDIATION_PLAN.md R3; toolset `data`): DataTables,
// curves, Blueprints (describe, add a variable — graph editing stays a non-goal),
// property edits on any asset, Enhanced Input actions and mapping contexts.

func dataSpecs() []*spec.Spec { return []*spec.Spec{dataQuerySpec(), dataEditSpec()} }

type dataQueryIn struct {
	Op       string   `json:"op" jsonschema:"table | curve | blueprint"`
	Asset    string   `json:"asset" jsonschema:"the DataTable, CurveFloat, Blueprint or InputMappingContext asset path"`
	RowNames []string `json:"row_names,omitempty" jsonschema:"table: only these rows (default all)"`
	Limit    int      `json:"limit,omitempty" jsonschema:"table: max rows returned (default 200)"`
}

func dataQuerySpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "table", Summary: "a DataTable's rows, typed (the engine's JSON forms)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"asset"}, Reaches: []string{"data_table_read"}},
		{Name: "curve", Summary: "a float curve's keys", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"asset"}, Rejects: []string{"row_names", "limit"}, Reaches: []string{"data_curve_read"}, Needs: []string{"plugin>=6"}},
		{Name: "blueprint", Summary: "a Blueprint's components, variables, functions, events, compile status + messages", Tier: spec.Ephemeral, Idempotent: true, Required: []string{"asset"}, Rejects: []string{"row_names", "limit"}, Reaches: []string{"data_blueprint"}, Needs: []string{"plugin>=6"}},
		{Name: "input_mapping", Summary: "an InputMappingContext's actions and their keys", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"asset"}, Rejects: []string{"row_names", "limit"}, Reaches: []string{"data_input_mapping_read"}},
	}
	return &spec.Spec{
		Name: "data_query", Title: "Read game data", Toolset: spec.Data, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Read game data. table: rows of a DataTable {row: {field: value}}. curve: a float curve's keys. " +
			"blueprint: components (Blueprint + native), variables (type, default, flags), functions, events, and the " +
			"status + messages of a fresh in-memory compile (not saved; refused during PIE). input_mapping: a mapping " +
			"context's actions and their keys. Change data with data_edit.",
		Schema:  spec.SchemaFor[dataQueryIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op", "asset"),
		Handler: dataQueryHandler,
	}
}

func dataQueryHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in dataQueryIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	py := map[string]string{"table": "data_table_read", "curve": "data_curve_read", "blueprint": "data_blueprint",
		"input_mapping": "data_input_mapping_read"}[c.Op.Name]
	out, err := v2Op(ctx, c, py, rename(map[string]any{}, c.Args, "asset", "asset", "row_names", "rows", "limit", "limit"))
	if err != nil {
		return nil, err
	}
	summary := c.Op.Name + " " + in.Asset
	switch c.Op.Name {
	case "table":
		summary = fmt.Sprintf("%v rows of %s", out["total"], in.Asset)
	case "blueprint":
		summary = fmt.Sprintf("%s: %v", in.Asset, out["status"])
	}
	return &spec.Result{Data: out, Summary: summary}, nil
}

type dataEditIn struct {
	Op               string         `json:"op" jsonschema:"set_properties | settings | table_upsert | table_delete | curve_keys | add_variable | input_mapping"`
	Asset            string         `json:"asset,omitempty" jsonschema:"the asset (not settings / input_mapping)"`
	Class            string         `json:"class,omitempty" jsonschema:"settings: the settings class, e.g. /Script/EngineSettings.GeneralProjectSettings"`
	Properties       map[string]any `json:"properties,omitempty" jsonschema:"set_properties: {property: value} on a non-Blueprint asset (Blueprints: asset_edit); settings: on the class default"`
	Rows             map[string]any `json:"rows,omitempty" jsonschema:"table_upsert: {row name: {field: value}}; fields not given keep their values"`
	RowNames         []string       `json:"row_names,omitempty" jsonschema:"table_delete: the rows to delete (all exist, or nothing is deleted)"`
	Points           []any          `json:"points,omitempty" jsonschema:"curve_keys: the new keys, [[time, value], ...] or [{time, value, interp: linear|constant|cubic}]"`
	Name             string         `json:"name,omitempty" jsonschema:"add_variable: the variable's name"`
	Type             string         `json:"type,omitempty" jsonschema:"add_variable: bool|byte|int|int64|float|double|name|string|text, object:<Class>, class:<Class>, struct:<Struct>, array:<type>, set:<type>"`
	Default          any            `json:"default,omitempty" jsonschema:"add_variable: the default value"`
	InstanceEditable *bool          `json:"instance_editable,omitempty" jsonschema:"add_variable: editable per instance"`
	ExposeOnSpawn    *bool          `json:"expose_on_spawn,omitempty" jsonschema:"add_variable: a spawn parameter"`
	Action           string         `json:"action,omitempty" jsonschema:"input_mapping: the InputAction asset, e.g. /Game/Input/IA_Dash (created if missing)"`
	Context          string         `json:"context,omitempty" jsonschema:"input_mapping: the InputMappingContext asset (created if missing)"`
	Keys             []string       `json:"keys,omitempty" jsonschema:"input_mapping: the action's keys in that context, exactly (e.g. [LeftShift]; [] unmaps)"`
	ValueType        string         `json:"value_type,omitempty" jsonschema:"input_mapping: digital | axis1d | axis2d | axis3d"`
	DryRun           bool           `json:"dry_run,omitempty" jsonschema:"check and report the change, change nothing (not settings / curve_keys: the plugin checks those while writing)"`
}

var dataEditParams = []string{"asset", "class", "properties", "rows", "row_names", "points", "name", "type", "default",
	"instance_editable", "expose_on_spawn", "action", "context", "keys", "value_type", "dry_run"}

// dataRejects is every dataEdit param except keep (each op takes only its own).
func dataRejects(keep ...string) []string {
	k := map[string]bool{}
	for _, p := range keep {
		k[p] = true
	}
	var out []string
	for _, p := range dataEditParams {
		if !k[p] {
			out = append(out, p)
		}
	}
	return out
}

func dataEditSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "set_properties", Summary: "set properties on an asset (per-property errors)", Tier: spec.Mutating, Required: []string{"asset", "properties"}, Rejects: dataRejects("asset", "properties", "dry_run"), Reaches: []string{"data_set_properties"}},
		{Name: "settings", Summary: "set a settings class's defaults and write its Default*.ini", Tier: spec.Mutating, Required: []string{"class", "properties"}, Rejects: dataRejects("class", "properties"), Reaches: []string{"data_set_settings"}, Needs: []string{"plugin>=6"}},
		{Name: "table_upsert", Summary: "insert or update DataTable rows by name (others untouched)", Tier: spec.Mutating, Required: []string{"asset", "rows"}, Rejects: dataRejects("asset", "rows", "dry_run"), Reaches: []string{"data_table_upsert"}},
		{Name: "table_delete", Summary: "delete DataTable rows by name", Tier: spec.Destructive, Required: []string{"asset", "row_names"}, Rejects: dataRejects("asset", "row_names", "dry_run"), Reaches: []string{"data_table_delete"}},
		{Name: "curve_keys", Summary: "replace a float curve's keys", Tier: spec.Mutating, Required: []string{"asset", "points"}, Rejects: dataRejects("asset", "points"), Reaches: []string{"data_curve_keys"}, Needs: []string{"plugin>=6"}},
		{Name: "add_variable", Summary: "add a member variable to a Blueprint", Tier: spec.Mutating, Required: []string{"asset", "name", "type"}, Rejects: dataRejects("asset", "name", "type", "default", "instance_editable", "expose_on_spawn", "dry_run"), Reaches: []string{"data_add_variable"}, Needs: []string{"plugin>=6"}},
		{Name: "input_mapping", Summary: "an InputAction's keys in a mapping context", Tier: spec.Mutating, Required: []string{"action", "context", "keys"}, Rejects: dataRejects("action", "context", "keys", "value_type", "dry_run"), Reaches: []string{"data_input_mapping"}},
	}
	return &spec.Spec{
		Name: "data_edit", Title: "Edit game data", Toolset: spec.Data, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Edit game data; each op saves. set_properties (any non-Blueprint asset), settings (a settings class, written to its Default*.ini), table_upsert / table_delete " +
			"(keyed, never replace-all), curve_keys (all-or-nothing): one undo step each. add_variable (Blueprint graphs are " +
			"not edited — logic goes in C++ via build), input_mapping (InputAction + mapping context, created if missing) and " +
			"settings say undoable:false.",
		Schema: spec.SchemaFor[dataEditIn](map[string][]any{"op": spec.OpEnum(ops...),
			"value_type": {"digital", "axis1d", "axis2d", "axis3d"}}, "op"),
		Handler: dataEditHandler,
	}
}

func dataEditHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in dataEditIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	args := map[string]any{}
	py := "data_" + c.Op.Name
	switch c.Op.Name {
	case "set_properties":
		args = pick(c.Args, "asset", "properties")
	case "settings":
		py = "data_set_settings"
		args = pick(c.Args, "class", "properties")
	case "table_upsert":
		args = pick(c.Args, "asset", "rows")
	case "table_delete":
		args = map[string]any{"asset": in.Asset, "rows": in.RowNames}
	case "curve_keys":
		args = map[string]any{"asset": in.Asset, "keys": in.Points}
	case "add_variable":
		args = pick(c.Args, "asset", "name", "type", "default", "instance_editable", "expose_on_spawn")
	case "input_mapping":
		if in.Keys == nil {
			return nil, envelope.New(envelope.InvalidArgument, "keys is required ([] unmaps the action)")
		}
		args = pick(c.Args, "action", "context", "keys", "value_type")
	}
	if in.DryRun {
		args["dry_run"] = true
	}
	out, err := v2Op(ctx, c, py, args)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		return &spec.Result{Data: out, Summary: c.Op.Name + " dry run: nothing changed"}, nil
	}
	if c.Op.Name == "add_variable" || c.Op.Name == "input_mapping" || c.Op.Name == "settings" {
		out["undoable"] = false // no editor transaction: roll back with git_revert
	}
	return &spec.Result{Data: out, Summary: c.Op.Name + " done"}, nil
}
