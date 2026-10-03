package tools

import (
	"context"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/logs"
)

// --- log tools ---

type logsTailIn struct {
	Lines       int      `json:"lines,omitempty" jsonschema:"default 200"`
	MinSeverity string   `json:"min_severity,omitempty" jsonschema:"Verbose|Log|Display|Warning|Error; default Display"`
	Categories  []string `json:"categories,omitempty" jsonschema:"restrict to these log categories, e.g. LogLiveCoding"`
}
type logsSinceIn struct {
	Marker      string `json:"marker" jsonschema:"a marker from logs_mark"`
	MinSeverity string `json:"min_severity,omitempty"`
}

func registerLogTools(s *registrar, d Deps) {
	add(s, "logs_mark", "Return a marker (byte offset) into the project log for a later logs_since.",
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			path := logs.LogPath(resolveDeps(ctx, d).ProjectDir)
			return nil, map[string]any{"marker": strconv.FormatInt(logs.LogSize(path), 10)}, nil
		})

	add(s, "logs_tail", "Return recent project log lines filtered by severity/category.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsTailIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			path := logs.LogPath(resolveDeps(ctx, d).ProjectDir)
			text, _, err := logs.ReadFrom(path, 0)
			if err != nil {
				return nil, nil, err
			}
			sev := in.MinSeverity
			if sev == "" {
				sev = "Display"
			}
			lines := logs.FilterLines(text, sev, in.Categories)
			n := in.Lines
			if n <= 0 {
				n = 200
			}
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			return nil, map[string]any{"lines": lines}, nil
		})

	add(s, "logs_since", "Return project log lines since a marker, with error/warning/ensure counts (attributable output for a run).",
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsSinceIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			off, _ := strconv.ParseInt(in.Marker, 10, 64)
			path := logs.LogPath(resolveDeps(ctx, d).ProjectDir)
			text, _, err := logs.ReadFrom(path, off)
			if err != nil {
				return nil, nil, err
			}
			sev := in.MinSeverity
			if sev == "" {
				sev = "Warning"
			}
			lines := logs.FilterLines(text, sev, nil)
			errs, warns, ensures := logs.CountBySeverity(lines)
			return nil, map[string]any{"lines": lines, "errors": errs, "warnings": warns, "ensures": ensures}, nil
		})
}
