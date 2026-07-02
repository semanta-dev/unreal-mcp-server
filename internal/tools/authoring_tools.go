package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

type applyRecipeIn struct {
	ScriptPath string `json:"script_path" jsonschema:"path to an idempotent level-recipe .py (run in the editor)"`
	Save       *bool  `json:"save,omitempty" jsonschema:"save after; default true"`
	CleanSlate *bool  `json:"clean_slate,omitempty" jsonschema:"destroy non-WorldSettings actors first; default true"`
}
type levelDiffIn struct {
	BeforeToken string `json:"before_token,omitempty" jsonschema:"a token from level_snapshot; empty = diff vs the most recent snapshot"`
}
type assetPathIn struct {
	AssetPath string `json:"asset_path"`
}
type createMatIn struct {
	Parent string         `json:"parent" jsonschema:"parent material path"`
	Dest   string         `json:"dest" jsonschema:"destination asset path for the instance"`
	Params map[string]any `json:"params,omitempty" jsonschema:"{scalar:{}, vector:{}, texture:{}} parameter overrides"`
}

func registerAuthoringTools(s *mcp.Server, b *bridge.Bridge) {
	add(s, "apply_level_recipe",
		"Run an idempotent level-recipe .py in the editor (optionally clean-slating first), then save. Returns actor counts before/after and any MISSING/errors.",
		structHandler[applyRecipeIn](b, "apply_level_recipe", func(in applyRecipeIn) map[string]any {
			m := map[string]any{"script_path": in.ScriptPath}
			if in.Save != nil {
				m["save"] = *in.Save
			}
			if in.CleanSlate != nil {
				m["clean_slate"] = *in.CleanSlate
			}
			return m
		}))

	add(s, "level_snapshot",
		"Capture a snapshot of the current level's actors; returns a token for a later level_diff.",
		structHandler[noArgs](b, "level_snapshot", func(noArgs) map[string]any { return map[string]any{} }))

	add(s, "level_diff",
		"Diff the current level against a snapshot token: added, removed, and moved actors (verify a recipe did what was intended).",
		structHandler[levelDiffIn](b, "level_diff", func(in levelDiffIn) map[string]any {
			m := map[string]any{}
			if in.BeforeToken != "" {
				m["before_token"] = in.BeforeToken
			}
			return m
		}))

	add(s, "asset_info",
		"Get metadata for an asset (class, bounds, LODs, materials, Nanite) — recipes need mesh bounds for tiling.",
		structHandler[assetPathIn](b, "asset_info", func(in assetPathIn) map[string]any {
			return map[string]any{"asset_path": in.AssetPath}
		}))

	add(s, "asset_reimport",
		"Reimport an asset from its source file (source art changed mid-session).",
		structHandler[assetPathIn](b, "asset_reimport", func(in assetPathIn) map[string]any {
			return map[string]any{"asset_path": in.AssetPath}
		}))

	add(s, "create_material_instance",
		"Create a material instance from a parent, applying scalar/vector/texture parameter overrides.",
		structHandler[createMatIn](b, "create_material_instance", func(in createMatIn) map[string]any {
			m := map[string]any{"parent": in.Parent, "dest": in.Dest}
			if in.Params != nil {
				m["params"] = in.Params
			}
			return m
		}))
}
