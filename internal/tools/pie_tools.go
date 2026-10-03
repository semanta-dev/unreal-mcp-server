package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
)

type pieObserveIn struct {
	ActorsOfInterest []string `json:"actors_of_interest,omitempty" jsonschema:"actor labels to read detailed state for"`
	Include          []string `json:"include,omitempty" jsonschema:"glob patterns of gamestate/actor property names to include; default all (minus a noise list)"`
	Exclude          []string `json:"exclude,omitempty" jsonschema:"glob patterns of property names to exclude"`
	Properties       []string `json:"properties,omitempty" jsonschema:"read exactly these gamestate/actor properties (preserves key names for predicate paths)"`
	MaxProps         int      `json:"max_props,omitempty" jsonschema:"cap on discovered properties per object; default 48"`
}
type pieExecIn struct {
	Target    string         `json:"target" jsonschema:"actor label, or 'gamestate'"`
	UFunction string         `json:"ufunction" jsonschema:"a BlueprintCallable UFUNCTION name"`
	Args      map[string]any `json:"args,omitempty"`
}
type pieWaitIn struct {
	Predicate  string   `json:"predicate" jsonschema:"a comparison over the pie_observe schema. gamestate keys are reflected snake_case, e.g. \"gamestate.wave_number >= 2\"; counts keys are class names, e.g. \"counts.EnemyCharacter >= 1\""`
	TimeoutS   float64  `json:"timeout_s,omitempty" jsonschema:"default 20"`
	Properties []string `json:"properties,omitempty" jsonschema:"pin exact gamestate property key names so a predicate can use them verbatim (e.g. [\"WaveNumber\"] to match gamestate.WaveNumber)"`
}
type pieScreenshotIn struct {
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

func registerPieTools(s *mcp.Server, b *bridge.Bridge) {
	add(s, "pie_observe",
		"Read live PIE game-world state: gamestate fields (allowlisted), a class histogram (counts), and detailed state for actors_of_interest. Only valid during PIE.",
		structHandler[pieObserveIn](b, "pie_observe", func(in pieObserveIn) map[string]any {
			m := map[string]any{}
			if len(in.ActorsOfInterest) > 0 {
				m["actors_of_interest"] = in.ActorsOfInterest
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
			return m
		}))

	add(s, "pie_exec",
		"Invoke a UFUNCTION on a live PIE actor by reflection (FindFunction/ProcessEvent) — this dispatches BlueprintCallable functions AND, in single-standalone PIE with authority, Server RPCs. target is an actor label or 'gamestate'.",
		structHandler[pieExecIn](b, "pie_exec", func(in pieExecIn) map[string]any {
			m := map[string]any{"target": in.Target, "ufunction": in.UFunction}
			if in.Args != nil {
				m["args"] = in.Args
			}
			return m
		}))

	add(s, "pie_screenshot",
		"Capture an exposure-correct PNG of the running PIE backbuffer via HighResShot. Only valid during PIE (use take_screenshot for the editor world).",
		pieScreenshot(b))

	add(s, "pie_wait_until",
		"Poll pie_observe until a predicate holds or timeout. Makes PIE tests deterministic (vs sleeping). Predicate is a single comparison over the observe schema.",
		pieWaitUntil(b))
}

func pieScreenshot(b *bridge.Bridge) mcp.ToolHandlerFor[pieScreenshotIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in pieScreenshotIn) (*mcp.CallToolResult, any, error) {
		fname := fmt.Sprintf("mcp_pie_%d_%d.png", os.Getpid(), time.Now().UnixNano())
		args := map[string]any{"filename": fname}
		if in.Width > 0 {
			args["width"] = in.Width
		}
		if in.Height > 0 {
			args["height"] = in.Height
		}
		raw, err := bridgeFromCtx(ctx, b).Call(ctx, "pie_screenshot", args)
		if err != nil {
			return nil, nil, err
		}
		var r struct {
			File  string `json:"file"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &r)
		if r.Error != "" {
			return nil, nil, fmt.Errorf("pie_screenshot: %s", r.Error)
		}
		data, err := readImageFile(r.File, 20*time.Second) // HighResShot is async
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: data, MIMEType: "image/png"}}}, nil, nil
	}
}

func pieWaitUntil(b *bridge.Bridge) mcp.ToolHandlerFor[pieWaitIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in pieWaitIn) (*mcp.CallToolResult, map[string]any, error) {
		pred, err := eval.ParsePredicate(in.Predicate)
		if err != nil {
			return nil, nil, err
		}
		timeout := 20 * time.Second
		if in.TimeoutS > 0 {
			timeout = time.Duration(in.TimeoutS * float64(time.Second))
		}
		observeArgs := map[string]any{}
		if len(in.Properties) > 0 {
			observeArgs["properties"] = in.Properties
		}
		start := time.Now()
		deadline := start.Add(timeout)
		var last map[string]any
		for {
			raw, cerr := bridgeFromCtx(ctx, b).Call(ctx, "pie_observe", observeArgs)
			if cerr == nil {
				_ = json.Unmarshal(raw, &last)
				if ok, _ := pred.Eval(last); ok {
					return nil, map[string]any{"met": true, "elapsed_s": time.Since(start).Seconds(), "final_state": last}, nil
				}
			}
			if time.Now().After(deadline) {
				return nil, map[string]any{"met": false, "elapsed_s": time.Since(start).Seconds(), "final_state": last}, nil
			}
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}

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

func registerLogTools(s *mcp.Server, d Deps) {
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
