package tools

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/logs"
)

type editorEventsIn struct {
	SinceOffset int64  `json:"since_offset,omitempty" jsonschema:"byte marker from a previous call; 0 = from the start"`
	Type        string `json:"type,omitempty" jsonschema:"only return events of this type (e.g. issue)"`
	Limit       int    `json:"limit,omitempty" jsonschema:"max events returned (most recent kept); default 200"`
}

// registerRobustnessTools adds P5: non-blocking event observation (survives a
// busy command channel), a cheap liveness probe, snapshot/restore so an agent can
// undo a destructive experiment, and a post-rebuild health gate.
func registerRobustnessTools(s *registrar, d Deps) {
	add(s, "editor_events",
		"Tail the editor's structured event stream (issues, PIE transitions) by byte offset — observable even while a long op holds the command channel. Returns events + a new offset to poll from.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in editorEventsIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			evs, off, err := logs.Tail(logs.Path(resolveDeps(ctx, d).ProjectDir), in.SinceOffset)
			if err != nil {
				return nil, nil, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 200
			}
			out := make([]json.RawMessage, 0, len(evs))
			for _, e := range evs {
				if in.Type != "" && e.Type != in.Type {
					continue
				}
				out = append(out, e.Raw)
			}
			if len(out) > limit {
				out = out[len(out)-limit:]
			}
			return nil, map[string]any{"events": out, "offset": off, "count": len(out)}, nil
		})

}
