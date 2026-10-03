package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
	"github.com/jdziat/unreal-mcp-server/internal/perf"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
)

type worldQueryIn struct {
	Kind   string    `json:"kind" jsonschema:"line_trace | sphere_overlap | nav_path | project_point"`
	Start  []float64 `json:"start,omitempty" jsonschema:"[x,y,z] for line_trace/nav_path"`
	End    []float64 `json:"end,omitempty" jsonschema:"[x,y,z] for line_trace/nav_path"`
	Center []float64 `json:"center,omitempty" jsonschema:"[x,y,z] for sphere_overlap"`
	Radius float64   `json:"radius,omitempty" jsonschema:"sphere_overlap radius; default 100"`
	Point  []float64 `json:"point,omitempty" jsonschema:"[x,y,z] for project_point"`
	World  string    `json:"world,omitempty" jsonschema:"auto|editor|game; default auto (game world in PIE)"`
}

type perfParseIn struct {
	File    string  `json:"file" jsonschema:"path to a CsvProfiler .csv (Saved/Profiling) or a .memreport dump"`
	HitchMs float64 `json:"hitch_ms,omitempty" jsonschema:"frames slower than this count as hitches; default 33.3"`
}

type scenarioRunIn struct {
	Path string `json:"path,omitempty" jsonschema:"path to a scenario/v1 .json file"`
	JSON string `json:"json,omitempty" jsonschema:"inline scenario/v1 JSON (alternative to path)"`
	Cols int    `json:"cols,omitempty" jsonschema:"montage columns; default 8"`
}

type scenarioListIn struct {
	Dir string `json:"dir,omitempty" jsonschema:"directory of scenario .json files; default <project>/.mcp/scenarios"`
}

// registerVerificationTools adds P4: spatial queries (world_query), a perf-as-data
// parser (perf_parse), and the saved/replayable scenario suite (scenario_run,
// scenario_list) — the regression gate for "iterate at scale". Frame visual luma
// is enriched into the timeline so a rubric can gate black/broken frames.
func registerVerificationTools(s *mcp.Server, d Deps) {
	b := d.Bridge

	add(s, "world_query",
		"Spatial verification in the game world (PIE): line_trace (is a shot/LOS clear?), sphere_overlap (what's near a point?), nav_path (can AI path A->B?), project_point (is a point on the navmesh?). Needs collision + a built navmesh.",
		structHandler[worldQueryIn](b, "world_query", func(in worldQueryIn) map[string]any {
			m := map[string]any{"kind": in.Kind}
			for k, v := range map[string][]float64{"start": in.Start, "end": in.End, "center": in.Center, "point": in.Point} {
				if len(v) > 0 {
					m[k] = v
				}
			}
			if in.Radius > 0 {
				m["radius"] = in.Radius
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))

	add(s, "perf_parse",
		"Parse a CsvProfiler CSV into frame-time percentiles (p50/p95/p99/max, hitch count) or a memreport dump into memory buckets — perf as DATA for a rubric, not viewport text.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in perfParseIn) (*mcp.CallToolResult, map[string]any, error) {
			data, err := os.ReadFile(in.File)
			if err != nil {
				return nil, nil, err
			}
			if strings.HasSuffix(strings.ToLower(in.File), ".memreport") || strings.Contains(strings.ToLower(in.File), "memreport") {
				return nil, map[string]any{"memory": perf.ParseMemReport(string(data))}, nil
			}
			fs, err := perf.ParseCSV(string(data), in.HitchMs)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"frames": fs}, nil
		})

	add(s, "scenario_list",
		"List saved scenario/v1 files (name + valid?) — the regression suite.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in scenarioListIn) (*mcp.CallToolResult, map[string]any, error) {
			dir := in.Dir
			if dir == "" {
				rd := resolveDeps(ctx, d)
				if rd.ProjectDir == "" {
					return nil, nil, errNoProject
				}
				dir = filepath.Join(rd.ProjectDir, ".mcp", "scenarios")
			}
			matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
			out := make([]map[string]any, 0, len(matches))
			for _, p := range matches {
				item := map[string]any{"file": p}
				if data, err := os.ReadFile(p); err == nil {
					if sc, diags, err := eval.ParseScenario(data); err == nil {
						item["name"] = sc.Name
						item["valid"] = !eval.HasErrors(diags)
					}
				}
				out = append(out, item)
			}
			return nil, map[string]any{"dir": dir, "scenarios": out}, nil
		})

	add(s, "scenario_run",
		"Run a saved scenario/v1 (arrange -> act beats -> assert rubric) and return the montage + timeline + PASS/WARN/FAIL verdict. The replayable functional/regression test.",
		scenarioRun(b, d))
}

func scenarioRun(b *bridge.Bridge, d Deps) mcp.ToolHandlerFor[scenarioRunIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in scenarioRunIn) (*mcp.CallToolResult, any, error) {
		// Per-session routing (MP3 §4): resolve the captured fallbacks from the
		// request ctx. Shadowing b/d here routes every downstream read/pass —
		// b.Call/b.CallText, d.ProjectDir, and helper passes (stopPlay,
		// runScenarioBeats, detectCrash) — to this session's editor. Falls back to
		// the registration-captured values when the daemon set no ctx Deps.
		b := bridgeFromCtx(ctx, b)
		d := resolveDeps(ctx, d)
		var data []byte
		var err error
		if in.Path != "" {
			data, err = os.ReadFile(in.Path)
			if err != nil {
				return nil, nil, err
			}
		} else if in.JSON != "" {
			data = []byte(in.JSON)
		} else {
			return nil, nil, fmt.Errorf("provide path or json")
		}
		sc, diags, perr := eval.ParseScenario(data)
		if perr != nil {
			return nil, nil, fmt.Errorf("parse scenario: %w", perr)
		}
		if eval.HasErrors(diags) {
			return nil, map[string]any{"error": "scenario has errors", "diagnostics": diags}, nil
		}

		mode := sc.Mode
		if mode == "" {
			mode = "pie"
		}
		duration := orDefault(sc.DurationS, 20)
		runStart := time.Now()
		var logMarker int64
		if d.ProjectDir != "" {
			logMarker = logs.LogSize(logs.LogPath(d.ProjectDir))
		}
		if sc.Level != "" {
			if _, err := b.CallText(ctx, "open_level", map[string]any{"level_path": sc.Level}); err != nil {
				return nil, nil, err
			}
		}
		if mode == "pie" || mode == "simulate" {
			if _, err := b.CallText(ctx, "start_play", map[string]any{"simulate": mode == "simulate"}); err != nil {
				return nil, nil, err
			}
			if sleepCtx(ctx, 1500*time.Millisecond) != nil {
				return nil, nil, ctx.Err()
			}
			if sc.TimeDilation > 0 && sc.TimeDilation != 1 {
				_, _ = b.Call(ctx, "console", map[string]any{"command": fmt.Sprintf("slomo %g", sc.TimeDilation)})
			}
		}
		// setup: set-props on live actors (spawn is plugin-gated).
		for _, sp := range sc.Setup.SetProps {
			_, _ = b.Call(ctx, "pie_set_property", map[string]any{"target": sp.Target, "properties": sp.Properties})
		}
		source := "scene_capture"
		if mode == "pie" {
			source = "pie_highres"
		}
		world := "auto"
		if mode == "editor" {
			world = "editor"
		}
		sraw, err := b.Call(ctx, "capture_start", map[string]any{
			"world": world, "source": source, "interval_s": orDefault(sc.IntervalS, 0.5),
			"cell_width": 256, "cell_height": 144, "max_frames": 96, "max_seconds": duration + 5,
			"track_actors": sc.TrackActors,
		})
		if err != nil {
			stopPlay(ctx, b, mode)
			return nil, nil, err
		}
		var sres struct {
			Session string `json:"session"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(sraw, &sres)
		if sres.Error != "" {
			stopPlay(ctx, b, mode)
			return nil, nil, fmt.Errorf("capture_start: %s", sres.Error)
		}

		runScenarioBeats(ctx, b, sc.Beats, duration)

		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		stopRaw, stopErr := b.Call(cleanupCtx, "capture_stop", map[string]any{"session": sres.Session})
		stopPlay(cleanupCtx, b, mode)
		if stopErr != nil {
			if rep := detectCrash(d.ProjectDir, logMarker, runStart); rep != nil {
				return nil, map[string]any{"scenario": sc.Name, "verdict": "FAIL", "crash": rep, "error": stopErr.Error()}, nil
			}
			return nil, nil, stopErr
		}
		var cr captureStopResult
		if err := json.Unmarshal(stopRaw, &cr); err != nil {
			return nil, nil, err
		}
		if len(cr.Frames) == 0 {
			return nil, map[string]any{"scenario": sc.Name, "error": "0 frames captured"}, nil
		}

		logSum := eval.LogSummary{}
		if d.ProjectDir != "" {
			if text, _, rerr := logs.ReadFrom(logs.LogPath(d.ProjectDir), logMarker); rerr == nil {
				lines := logs.FilterLines(text, "Warning", nil)
				logSum.Errors, logSum.Warnings, logSum.Ensures = logs.CountBySeverity(lines)
			}
		}
		enrichVisual(cr.Dir, cr.Frames) // add per-frame visual.luma so a rubric can gate black frames
		samples := framesToSamples(cr.Frames)
		report := eval.Evaluate(samples, logSum, scenarioRubric(sc.Rubric))
		crashRep := detectCrash(d.ProjectDir, logMarker, runStart)
		extra := map[string]any{
			"scenario": sc.Name, "verdict": report.Verdict, "rubric_result": reportToJSON(report),
			"logs": map[string]any{"errors": logSum.Errors, "warnings": logSum.Warnings, "ensures": logSum.Ensures},
		}
		if crashRep != nil {
			extra["crash"] = crashRep
		}
		return buildMontageResult(cr.Dir, cr.Frames, orDefaultInt(in.Cols, 8), true, failedFrameIndices(report), extra)
	}
}

// runScenarioBeats runs the scenario's beats (exec / wait_until / console) in
// scheduled order over the window.
func runScenarioBeats(ctx context.Context, b *bridge.Bridge, beats []eval.Beat, duration float64) {
	start := time.Now()
	ordered := append([]eval.Beat(nil), beats...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].AtS < ordered[j].AtS })
	for _, bt := range ordered {
		if bt.AtS > 0 && sleepUntil(ctx, start.Add(secs(bt.AtS))) != nil {
			return
		}
		if bt.Exec != nil {
			_, _ = b.Call(ctx, "pie_exec", map[string]any{"target": bt.Exec.Target, "ufunction": bt.Exec.UFunction, "args": bt.Exec.Args})
		}
		if bt.Console != "" {
			_, _ = b.Call(ctx, "console", map[string]any{"command": bt.Console})
		}
		if bt.WaitUntil != "" {
			waitPredicate(ctx, b, bt.WaitUntil, orDefault(bt.TimeoutS, 10))
		}
	}
	sleepUntil(ctx, start.Add(secs(duration)))
}

func scenarioRubric(checks []eval.RubricCheck) eval.RubricSpec {
	spec := eval.RubricSpec{Checks: make([]eval.Check, len(checks))}
	for i, c := range checks {
		spec.Checks[i] = eval.Check{ID: c.ID, Kind: c.Kind, Path: c.Path, Params: c.Params, Severity: c.Severity}
	}
	return spec
}

// enrichVisual decodes each captured frame and stamps its mean luma into the
// frame's observed state as visual.luma, so a rubric "min visual.luma 0.02"
// catches a black/unrendered frame.
func enrichVisual(dir string, frames []captureFrame) {
	for i := range frames {
		img, err := visual.Load(filepath.Join(dir, frames[i].File))
		if err != nil {
			continue
		}
		if frames[i].State == nil {
			frames[i].State = map[string]any{}
		}
		frames[i].State["visual"] = map[string]any{"luma": visual.MeanLuma(img)}
	}
}
