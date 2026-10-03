package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/snapshot"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

type instancesIn struct {
	Tag   string `json:"tag,omitempty" jsonschema:"restrict to ISM/HISM components carrying this component tag (e.g. 'static')"`
	Mesh  string `json:"mesh,omitempty" jsonschema:"restrict to components whose static-mesh path contains this substring"`
	World string `json:"world,omitempty" jsonschema:"editor|game; default editor"`
	Limit int    `json:"limit,omitempty" jsonschema:"instances_list cap; default 8192"`
}

type sceneDigestIn struct {
	Scope       string  `json:"scope,omitempty" jsonschema:"instances (HISM/ISM) | actors; default instances"`
	Tag         string  `json:"tag,omitempty" jsonschema:"instances scope: component tag filter"`
	Mesh        string  `json:"mesh,omitempty" jsonschema:"instances scope: mesh-path substring filter"`
	ClassFilter string  `json:"class_filter,omitempty" jsonschema:"actors scope: class/label substring filter"`
	World       string  `json:"world,omitempty"`
	PosBucket   float64 `json:"pos_bucket,omitempty" jsonschema:"position quantization in world units; default 1"`
	RotBucket   float64 `json:"rot_bucket,omitempty" jsonschema:"rotation quantization in degrees; default 1"`
	Limit       int     `json:"limit,omitempty" jsonschema:"instances scope: max instances to hash; default 5,000,000 (raise for a huge HISM city)"`
}

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
	b := d.Bridge

	add(s, "instances_count",
		"Count HISM/ISM instances (which list_actors and pie_observe.counts are blind to — a whole city can live as instances in ONE actor), grouped by mesh, filterable by component tag.",
		structHandler[instancesIn](b, "instances_count", func(in instancesIn) map[string]any {
			return instancesArgs(in)
		}))
	add(s, "instances_list",
		"List HISM/ISM instance transforms (mesh, loc, rot, scale) — the instance-level content list.",
		structHandler[instancesIn](b, "instances_list", func(in instancesIn) map[string]any {
			return instancesArgs(in)
		}))
	add(s, "scene_digest",
		"Deterministic quantize+SHA1 hash over actor OR HISM/ISM instance transforms — the authored-content oracle (verify a map hashes to an expected value; instance-aware where level_diff is not).",
		sceneDigest(b))
	add(s, "image_compare",
		"Perceptually compare two images (a capture frame vs a golden baseline): dHash/aHash distance + luma delta, with a pass verdict. For visual-regression / golden gates.",
		imageCompare())
}

func instancesArgs(in instancesIn) map[string]any {
	m := map[string]any{}
	if in.Tag != "" {
		m["tag"] = in.Tag
	}
	if in.Mesh != "" {
		m["mesh"] = in.Mesh
	}
	if in.World != "" {
		m["world"] = in.World
	}
	if in.Limit > 0 {
		m["limit"] = in.Limit
	}
	return m
}

func sceneDigest(b *bridge.Bridge) mcp.ToolHandlerFor[sceneDigestIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in sceneDigestIn) (*mcp.CallToolResult, map[string]any, error) {
		scope := in.Scope
		if scope == "" {
			scope = "instances"
		}
		var items []snapshot.Transform
		if scope == "actors" {
			raw, err := bridgeFromCtx(ctx, b).Call(ctx, "actor_transforms", map[string]any{"class_filter": in.ClassFilter})
			if err != nil {
				return nil, nil, err
			}
			var r struct {
				Transforms []snapshot.Transform `json:"transforms"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, nil, err
			}
			items = r.Transforms
		} else {
			// A content ORACLE must never hash a partial set silently: request an
			// effectively-unbounded list and fail loudly if the op still truncated.
			limit := in.Limit
			if limit <= 0 {
				limit = 5000000
			}
			args := map[string]any{"world": in.World, "limit": limit}
			if in.Tag != "" {
				args["tag"] = in.Tag
			}
			if in.Mesh != "" {
				args["mesh"] = in.Mesh
			}
			raw, err := bridgeFromCtx(ctx, b).Call(ctx, "instances_list", args)
			if err != nil {
				return nil, nil, err
			}
			var r struct {
				Instances []snapshot.Transform `json:"instances"`
				Truncated bool                 `json:"truncated"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, nil, err
			}
			if r.Truncated {
				return nil, map[string]any{
					"scope": scope, "truncated": true, "count": len(r.Instances),
					"error": "instance set exceeded the list cap; digest would cover only a partial set — narrow with tag/mesh or raise `limit`",
				}, nil
			}
			items = r.Instances
		}
		res := snapshot.Digest(items, snapshot.Quant{PosBucket: in.PosBucket, RotBucket: in.RotBucket})
		return nil, map[string]any{
			"scope": scope, "hash": res.Hash, "count": res.Count,
			"worst_pos_margin_uu": res.WorstPosMargin, "worst_rot_margin_deg": res.WorstRotMargin,
		}, nil
	}
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
