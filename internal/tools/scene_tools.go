package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/scenespec"
)

type sceneApplyIn struct {
	SpecPath string `json:"spec_path,omitempty" jsonschema:"path to a unreal.scene/v1 JSON spec file"`
	SpecJSON string `json:"spec_json,omitempty" jsonschema:"the spec as inline JSON text (alternative to spec_path)"`
	Prune    bool   `json:"prune,omitempty" jsonschema:"destroy this scene's previously-applied actors that are no longer in the spec (tag-scoped; never touches hand-placed actors)"`
	Save     *bool  `json:"save,omitempty" jsonschema:"save after applying; default true"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"compute the add/update/prune diff without mutating the level"`
}

type sceneClearIn struct {
	SceneID string `json:"scene_id" jsonschema:"the scene id whose actors to remove"`
	Save    *bool  `json:"save,omitempty" jsonschema:"save after; default true"`
}

type envPresetIn struct {
	SceneID   string         `json:"scene_id,omitempty" jsonschema:"scene id to namespace the env actors; default 'env'"`
	Preset    string         `json:"preset" jsonschema:"daytime_clear|overcast|dusk|night|studio"`
	Overrides map[string]any `json:"overrides,omitempty" jsonschema:"e.g. {sun_rotation_pyr:[-45,30,0], sun_intensity_lux:75000, exposure_ev100:11}"`
	Save      *bool          `json:"save,omitempty"`
}

type designCheckIn struct {
	SpecPath string            `json:"spec_path,omitempty" jsonschema:"a spec whose checks block to evaluate"`
	SpecJSON string            `json:"spec_json,omitempty"`
	Checks   *scenespec.Checks `json:"checks,omitempty" jsonschema:"explicit invariants to require (overrides the spec's checks)"`
}

type layoutPreviewIn struct {
	Layout scenespec.Layout `json:"layout" jsonschema:"a layout block: {type:grid|ring|line|scatter, ...params}"`
}

type scenePlanIn struct {
	SpecPath string `json:"spec_path,omitempty"`
	SpecJSON string `json:"spec_json,omitempty"`
}

// registerSceneTools adds the high-level, declarative design tools (goal B):
// compile+realize a whole scene idempotently in one editor op, apply lighting
// presets, lint a level's design invariants, and preview layouts offline.
func registerSceneTools(s *registrar, b *bridge.Bridge) {
	add(s, "scene_apply",
		"Realize a declarative unreal.scene/v1 spec (blockout, prefabs, layouts, lighting) idempotently in ONE editor transaction. Re-applying only updates; prune (tag-scoped) removes this scene's stale actors. dry_run returns the add/update/prune diff without mutating.",
		sceneApply(b))

	add(s, "scene_plan",
		"Compile a scene spec and diff it against the current level (add/update/prune, missing asset refs) WITHOUT mutating — a dry run to review before scene_apply.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in scenePlanIn) (*mcp.CallToolResult, map[string]any, error) {
			return sceneApply(b)(ctx, nil, sceneApplyIn{SpecPath: in.SpecPath, SpecJSON: in.SpecJSON, DryRun: true})
		})

	add(s, "scene_clear",
		"Remove all actors a given scene_id previously applied (tag-scoped; leaves hand-placed actors untouched).",
		structHandler[sceneClearIn](b, "scene_clear", func(in sceneClearIn) map[string]any {
			m := map[string]any{"scene_id": in.SceneID}
			if in.Save != nil {
				m["save"] = *in.Save
			}
			return m
		}))

	add(s, "env_preset_apply",
		"Apply a lighting/sky/exposure preset (daytime_clear|overcast|dusk|night|studio) as scene-managed actors, with the sun always correctly pitched down so the level is lit (not black).",
		envPresetApply(b))

	add(s, "design_check",
		"Lint the current level's design invariants (lighting, missing meshes, player start, nav bounds) via one probe. Catches the failures that make a level unplayable/black before a playtest.",
		designCheck(b))

	add(s, "layout_preview",
		"Preview where a layout (grid|ring|line|scatter) would place instances — a pure planning tool that returns locations/rotations without touching the editor.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in layoutPreviewIn) (*mcp.CallToolResult, map[string]any, error) {
			pts := scenespec.Expand(in.Layout, scenespec.Transform{Scale: [3]float64{1, 1, 1}})
			out := make([]map[string]any, len(pts))
			for i, p := range pts {
				out[i] = map[string]any{"location": p.Location, "rotation_pyr": p.RotationPyr}
			}
			return nil, map[string]any{"count": len(out), "instances": out}, nil
		})
}

func loadSpecBytes(specPath, specJSON string) ([]byte, error) {
	if specPath != "" {
		return os.ReadFile(specPath)
	}
	if specJSON != "" {
		return []byte(specJSON), nil
	}
	return nil, errors.New("provide spec_path or spec_json")
}

func diagsToJSON(diags []scenespec.Diagnostic) []map[string]any {
	out := make([]map[string]any, len(diags))
	for i, d := range diags {
		out[i] = map[string]any{"severity": d.Severity, "field": d.Field, "message": d.Message}
	}
	return out
}

func hasErrorDiag(diags []scenespec.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func sceneApply(b *bridge.Bridge) mcp.ToolHandlerFor[sceneApplyIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in sceneApplyIn) (*mcp.CallToolResult, map[string]any, error) {
		data, err := loadSpecBytes(in.SpecPath, in.SpecJSON)
		if err != nil {
			return nil, nil, err
		}
		spec, diags, perr := scenespec.Parse(data)
		if perr != nil {
			return nil, nil, fmt.Errorf("parse spec: %w", perr)
		}
		plan, cdiags := scenespec.Compile(spec)
		diags = append(diags, cdiags...)
		if hasErrorDiag(diags) {
			// Refuse to realize a spec with errors (e.g. a sun pointing up).
			return nil, map[string]any{"ok": false, "diagnostics": diagsToJSON(diags),
				"error": "spec has errors; not applied"}, nil
		}
		if in.DryRun {
			before, berr := listActorsSnapshot(ctx, bridgeFromCtx(ctx, b))
			if berr != nil {
				return nil, nil, berr
			}
			diff := scenespec.Diff(spec.SceneID, plan, before)
			return nil, map[string]any{"dry_run": true, "scene_id": spec.SceneID,
				"diagnostics": diagsToJSON(diags), "diff": diff, "placements": len(plan.Placements)}, nil
		}
		return applyPlan(ctx, bridgeFromCtx(ctx, b), plan, in.Prune, in.Save, diags)
	}
}

// applyPlan sends a compiled plan to the editor's scene_apply op.
func applyPlan(ctx context.Context, b *bridge.Bridge, plan scenespec.Plan, prune bool, save *bool, diags []scenespec.Diagnostic) (*mcp.CallToolResult, map[string]any, error) {
	args := map[string]any{"scene_id": plan.SceneID, "placements": plan.Placements, "prune": prune}
	if save != nil {
		args["save"] = *save
	}
	raw, err := b.Call(ctx, "scene_apply", args)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(diags) > 0 {
		out["diagnostics"] = diagsToJSON(diags)
	}
	// ok reflects the editor result: a hard per-placement error (spawn/class
	// failure) means the apply did not fully succeed and nothing was saved.
	ok := true
	if errs, isArr := out["errors"].([]any); isArr && len(errs) > 0 {
		ok = false
	}
	out["ok"] = ok
	return nil, out, nil
}

func envPresetApply(b *bridge.Bridge) mcp.ToolHandlerFor[envPresetIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in envPresetIn) (*mcp.CallToolResult, map[string]any, error) {
		sceneID := in.SceneID
		if sceneID == "" {
			sceneID = "env"
		}
		spec := &scenespec.Spec{
			Schema:      "unreal.scene/v1",
			SceneID:     sceneID,
			Environment: &scenespec.Environment{Preset: in.Preset, Overrides: in.Overrides},
		}
		plan, diags := scenespec.Compile(spec)
		if hasErrorDiag(diags) {
			return nil, map[string]any{"ok": false, "diagnostics": diagsToJSON(diags),
				"error": "preset produced errors (e.g. sun pitch>=0); not applied"}, nil
		}
		return applyPlan(ctx, bridgeFromCtx(ctx, b), plan, false, in.Save, diags)
	}
}

func designCheck(b *bridge.Bridge) mcp.ToolHandlerFor[designCheckIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in designCheckIn) (*mcp.CallToolResult, map[string]any, error) {
		var checks scenespec.Checks
		switch {
		case in.Checks != nil:
			checks = *in.Checks
		case in.SpecPath != "" || in.SpecJSON != "":
			data, err := loadSpecBytes(in.SpecPath, in.SpecJSON)
			if err != nil {
				return nil, nil, err
			}
			spec, _, perr := scenespec.Parse(data)
			if perr != nil {
				return nil, nil, fmt.Errorf("parse spec: %w", perr)
			}
			checks = spec.Checks
		default:
			// Sensible default lint when no spec/checks are supplied.
			checks = scenespec.Checks{RequireEnvironmentLit: true, RequireNoMissingMeshes: true, RequirePlayerStart: true}
		}
		raw, err := bridgeFromCtx(ctx, b).Call(ctx, "design_probe", map[string]any{})
		if err != nil {
			return nil, nil, err
		}
		var facts scenespec.ProbeFacts
		if err := json.Unmarshal(raw, &facts); err != nil {
			return nil, nil, err
		}
		report := scenespec.EvaluateChecks(checks, facts)
		return nil, map[string]any{"ok": report.OK, "results": report.Results}, nil
	}
}

// listActorsSnapshot builds a label->SnapActor map from list_actors for the diff.
func listActorsSnapshot(ctx context.Context, b *bridge.Bridge) (map[string]scenespec.SnapActor, error) {
	raw, err := b.Call(ctx, "list_actors", map[string]any{"name_filter": ""})
	if err != nil {
		return nil, err
	}
	var arr []struct {
		Label    string    `json:"label"`
		Class    string    `json:"class"`
		Location []float64 `json:"location"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	out := make(map[string]scenespec.SnapActor, len(arr))
	for _, a := range arr {
		out[a.Label] = scenespec.SnapActor{Location: toVec3(a.Location), Class: a.Class}
	}
	return out, nil
}
