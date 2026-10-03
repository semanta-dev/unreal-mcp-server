package tools

import (
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

func registerAuthoringTools(s *registrar, b *bridge.Bridge) {

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

}
