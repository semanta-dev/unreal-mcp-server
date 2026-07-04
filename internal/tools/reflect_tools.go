package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

type reflectObjectIn struct {
	Target     string   `json:"target,omitempty" jsonschema:"'gamestate' (default), an actor label, 'playercontroller'/'pawn', or a class path like /Script/Engine.PointLight"`
	Include    []string `json:"include,omitempty" jsonschema:"glob patterns of property names to include; default ['*']"`
	Exclude    []string `json:"exclude,omitempty" jsonschema:"glob patterns of property names to exclude (on top of a built-in noise list)"`
	Properties []string `json:"properties,omitempty" jsonschema:"read exactly these named properties (preserves your key names for predicate paths); overrides include/exclude"`
	MaxProps   int      `json:"max_props,omitempty" jsonschema:"cap on discovered properties; default 64"`
	MaxStr     int      `json:"max_str,omitempty" jsonschema:"truncate string/repr values to this length; default 512"`
}

// registerReflectTools adds the game-agnostic reflection tool. It discovers an
// object's exposed properties dynamically instead of a hardcoded allowlist, so
// pie_observe and the capture recorder work for any game (not just aesir).
func registerReflectTools(s *mcp.Server, b *bridge.Bridge) {
	add(s, "reflect_object",
		"Discover and read an editor/game object's exposed properties WITHOUT a hardcoded allowlist (works for any game). Target 'gamestate', an actor label, 'pawn'/'playercontroller', or a class path. Returns {class, path, properties, functions}.",
		structHandler[reflectObjectIn](b, "reflect_object", func(in reflectObjectIn) map[string]any {
			m := map[string]any{}
			if in.Target != "" {
				m["target"] = in.Target
			}
			if len(in.Include) > 0 {
				m["include"] = in.Include
			}
			if len(in.Exclude) > 0 {
				m["exclude"] = in.Exclude
			}
			if len(in.Properties) > 0 {
				m["properties"] = in.Properties
			}
			if in.MaxProps > 0 {
				m["max_props"] = in.MaxProps
			}
			if in.MaxStr > 0 {
				m["max_str"] = in.MaxStr
			}
			return m
		}))
}
