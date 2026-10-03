package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

type imageCompareIn struct {
	A        string   `json:"a" jsonschema:"path to a PNG image (e.g. a capture frame)"`
	B        string   `json:"b" jsonschema:"path to the other PNG / baseline"`
	MaxDHash *int     `json:"max_dhash,omitempty" jsonschema:"pass threshold on dHash distance; default 8. Set 0 for an exact match."`
	MaxLuma  *float64 `json:"max_luma_delta,omitempty" jsonschema:"pass threshold on luma delta; default 0.15. Set 0 for an exact match."`
}

// registerPWTools adds the poly-world / builder-sim verification workstream:
// instance-aware observation, a deterministic content-digest oracle, the
// pie_verify functional harness, and perceptual image compare — all game-agnostic.
func registerPWTools(s *registrar, d Deps) {
	add(s, "image_compare",
		"Perceptually compare two images (a capture frame vs a golden baseline): dHash/aHash distance + luma delta, with a pass verdict. For visual-regression / golden gates.",
		imageCompare())
}

func imageCompare() mcp.ToolHandlerFor[imageCompareIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in imageCompareIn) (*mcp.CallToolResult, map[string]any, error) {
		ia, err := visual.Load(in.A)
		if err != nil {
			return nil, nil, fmt.Errorf("load a: %w", err)
		}
		ib, err := visual.Load(in.B)
		if err != nil {
			return nil, nil, fmt.Errorf("load b: %w", err)
		}
		res := visual.Compare(ia, ib)
		maxD := 8
		if in.MaxDHash != nil {
			maxD = *in.MaxDHash
		}
		maxL := 0.15
		if in.MaxLuma != nil {
			maxL = *in.MaxLuma
		}
		pass := res.DHashDist <= maxD && res.LumaDelta <= maxL
		return nil, map[string]any{
			"pass": pass, "dhash_dist": res.DHashDist, "ahash_dist": res.AHashDist,
			"luma_delta": res.LumaDelta, "luma_a": res.LumaA, "luma_b": res.LumaB,
		}, nil
	}
}
