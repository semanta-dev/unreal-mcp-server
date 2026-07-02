package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/logtail"
)

type pieObserveIn struct {
	ActorsOfInterest []string `json:"actors_of_interest,omitempty" jsonschema:"actor labels to read detailed state for"`
}
type pieExecIn struct {
	Target    string         `json:"target" jsonschema:"actor label, or 'gamestate'"`
	UFunction string         `json:"ufunction" jsonschema:"a BlueprintCallable UFUNCTION name"`
	Args      map[string]any `json:"args,omitempty"`
}
type pieWaitIn struct {
	Predicate string  `json:"predicate" jsonschema:"a comparison over the pie_observe schema, e.g. \"gamestate.WaveNumber >= 2\" or \"counts.EnemyCharacter >= 1\""`
	TimeoutS  float64 `json:"timeout_s,omitempty" jsonschema:"default 20"`
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
			return m
		}))

	add(s, "pie_exec",
		"Invoke a BlueprintCallable UFUNCTION on a live PIE actor (deterministic driving). target is an actor label or 'gamestate'.",
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
		raw, err := b.Call(ctx, "pie_screenshot", args)
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
		pred, err := parsePredicate(in.Predicate)
		if err != nil {
			return nil, nil, err
		}
		timeout := 20 * time.Second
		if in.TimeoutS > 0 {
			timeout = time.Duration(in.TimeoutS * float64(time.Second))
		}
		start := time.Now()
		deadline := start.Add(timeout)
		var last map[string]any
		for {
			raw, cerr := b.Call(ctx, "pie_observe", map[string]any{})
			if cerr == nil {
				_ = json.Unmarshal(raw, &last)
				if ok, _ := pred.eval(last); ok {
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

// --- predicate: "<dotted.path> <op> <value>" over the observe JSON ---

type predicate struct {
	path  []string
	op    string
	num   float64
	isNum bool
	str   string
}

var predOps = []string{">=", "<=", "==", "!=", ">", "<"}

func parsePredicate(expr string) (*predicate, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range predOps {
		if i := strings.Index(expr, op); i > 0 {
			lhs := strings.TrimSpace(expr[:i])
			rhs := strings.TrimSpace(expr[i+len(op):])
			p := &predicate{path: strings.Split(lhs, "."), op: op}
			rhs = strings.Trim(rhs, `'"`)
			if f, err := strconv.ParseFloat(rhs, 64); err == nil {
				p.num = f
				p.isNum = true
			} else {
				p.str = rhs
			}
			return p, nil
		}
	}
	return nil, fmt.Errorf("predicate must be '<path> <op> <value>' with op in %v; got %q", predOps, expr)
}

func (p *predicate) eval(state map[string]any) (bool, error) {
	if state == nil {
		return false, nil
	}
	val, ok := lookupPath(state, p.path)
	if !ok {
		return false, nil // not present yet
	}
	if p.isNum {
		f, ok := toFloat(val)
		if !ok {
			return false, nil
		}
		switch p.op {
		case ">=":
			return f >= p.num, nil
		case "<=":
			return f <= p.num, nil
		case ">":
			return f > p.num, nil
		case "<":
			return f < p.num, nil
		case "==":
			return f == p.num, nil
		case "!=":
			return f != p.num, nil
		}
	}
	s := fmt.Sprintf("%v", val)
	switch p.op {
	case "==":
		return s == p.str, nil
	case "!=":
		return s != p.str, nil
	}
	return false, nil
}

func lookupPath(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, k := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
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
			if d.ProjectDir == "" {
				return nil, nil, errNoProject
			}
			path := logtail.LogPath(d.ProjectDir)
			return nil, map[string]any{"marker": strconv.FormatInt(logtail.Size(path), 10)}, nil
		})

	add(s, "logs_tail", "Return recent project log lines filtered by severity/category.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsTailIn) (*mcp.CallToolResult, map[string]any, error) {
			if d.ProjectDir == "" {
				return nil, nil, errNoProject
			}
			path := logtail.LogPath(d.ProjectDir)
			text, _, err := logtail.ReadFrom(path, 0)
			if err != nil {
				return nil, nil, err
			}
			sev := in.MinSeverity
			if sev == "" {
				sev = "Display"
			}
			lines := logtail.FilterLines(text, sev, in.Categories)
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
			if d.ProjectDir == "" {
				return nil, nil, errNoProject
			}
			off, _ := strconv.ParseInt(in.Marker, 10, 64)
			path := logtail.LogPath(d.ProjectDir)
			text, _, err := logtail.ReadFrom(path, off)
			if err != nil {
				return nil, nil, err
			}
			sev := in.MinSeverity
			if sev == "" {
				sev = "Warning"
			}
			lines := logtail.FilterLines(text, sev, nil)
			errs, warns, ensures := logtail.CountBySeverity(lines)
			return nil, map[string]any{"lines": lines, "errors": errs, "warnings": warns, "ensures": ensures}, nil
		})
}
