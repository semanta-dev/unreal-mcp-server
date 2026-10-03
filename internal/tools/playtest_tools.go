package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/crash"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
)

type playtestExecBeat struct {
	Target    string         `json:"target" jsonschema:"actor label or 'gamestate'"`
	UFunction string         `json:"ufunction" jsonschema:"a BlueprintCallable UFUNCTION to invoke"`
	Args      map[string]any `json:"args,omitempty"`
}

type playtestBeat struct {
	AtS       float64           `json:"at_s,omitempty" jsonschema:"seconds after capture start to run this beat"`
	Exec      *playtestExecBeat `json:"exec,omitempty" jsonschema:"invoke a UFUNCTION at this beat"`
	WaitUntil string            `json:"wait_until,omitempty" jsonschema:"pie_observe predicate to wait for before continuing"`
	TimeoutS  float64           `json:"timeout_s,omitempty" jsonschema:"wait timeout; default 10"`
}

type rubricCheckIn struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind" jsonschema:"reached|entered|increased|decreased|changed|stayed|range|min|max|nonzero_count|log_zero|log_max"`
	Path     string         `json:"path" jsonschema:"dotted path into observed state, e.g. gamestate.wave_number, counts.EnemyCharacter, perf.fps (min), perf.hitch_ms (max); or errors|warnings|ensures for log checks"`
	Params   map[string]any `json:"params,omitempty" jsonschema:"reducer args, e.g. {value:...} or {min:..,max:..} or {by:..}"`
	Severity string         `json:"severity,omitempty" jsonschema:"fail|warn|info; default fail"`
}

type playtestCaptureIn struct {
	Mode         string          `json:"mode,omitempty" jsonschema:"pie (possessed) | simulate | editor; default pie"`
	Level        string          `json:"level,omitempty" jsonschema:"level to open first, e.g. /Game/Maps/L_Arena"`
	Beats        []playtestBeat  `json:"beats,omitempty" jsonschema:"deterministic script run over the capture window"`
	DurationS    float64         `json:"duration_s,omitempty" jsonschema:"total REAL-TIME capture window seconds; default 20"`
	IntervalS    float64         `json:"interval_s,omitempty" jsonschema:"seconds between frames; default 0.5"`
	TimeDilation float64         `json:"time_dilation,omitempty" jsonschema:"speed up the sim during capture via 'slomo N' (e.g. 8) so slow bottlenecks form in fewer real seconds; default 1 (real time). Use a shorter duration_s to match."`
	MaxFrames    int             `json:"max_frames,omitempty" jsonschema:"cap on frames; default 64"`
	FrameW       int             `json:"frame_w,omitempty" jsonschema:"per-frame width; default 256"`
	FrameH       int             `json:"frame_h,omitempty" jsonschema:"per-frame height; default 144"`
	TrackActors  []string        `json:"track_actors,omitempty" jsonschema:"actor labels to record detailed per-frame state for"`
	Include      []string        `json:"include,omitempty" jsonschema:"observe property include globs"`
	Properties   []string        `json:"properties,omitempty" jsonschema:"observe exact property names"`
	Rubric       []rubricCheckIn `json:"rubric,omitempty" jsonschema:"pass/fail checks evaluated over the recorded timeline"`
	Cols         int             `json:"cols,omitempty" jsonschema:"montage columns; default 8"`
}

type playtestEvaluateIn struct {
	Timeline []struct {
		Index  int            `json:"index"`
		TWorld float64        `json:"t_world"`
		State  map[string]any `json:"state"`
	} `json:"timeline" jsonschema:"recorded frames (index, t_world, observed state)"`
	Logs struct {
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
		Ensures  int `json:"ensures"`
	} `json:"logs,omitempty"`
	Rubric []rubricCheckIn `json:"rubric" jsonschema:"checks to evaluate"`
}

// registerPlaytestTools adds the playtest capstone: playtest_capture (the
// end-to-end orchestrator that returns one montage + timeline + rubric verdict +
// log summary) and playtest_evaluate (pure re-scoring of a timeline, no editor).
func registerPlaytestTools(s *registrar, d Deps) {
	add(s, "playtest_capture",
		"Run an in-depth automated play test: open a level, enter play, capture a synchronized filmstrip of frames+state, drive deterministic beats, then return ONE contact-sheet montage + a per-frame timeline + a rubric PASS/WARN/FAIL verdict + a log summary. The token-efficient way for an agent to validate a game works.",
		playtestCapture(d.Bridge, d))

	add(s, "playtest_evaluate",
		"Re-score a recorded playtest timeline against a rubric WITHOUT re-running the game (pure). Returns the checklist + verdict, evidence pointing at frame indices.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in playtestEvaluateIn) (*mcp.CallToolResult, map[string]any, error) {
			samples := make([]eval.Sample, len(in.Timeline))
			for i, s := range in.Timeline {
				samples[i] = eval.Sample{Index: s.Index, TWorld: s.TWorld, State: s.State}
			}
			logSum := eval.LogSummary{Errors: in.Logs.Errors, Warnings: in.Logs.Warnings, Ensures: in.Logs.Ensures}
			report := eval.Evaluate(samples, logSum, toRubricSpec(in.Rubric))
			return nil, reportToJSON(report), nil
		})
}

func playtestCapture(b *bridge.Bridge, d Deps) mcp.ToolHandlerFor[playtestCaptureIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in playtestCaptureIn) (*mcp.CallToolResult, any, error) {
		mode := in.Mode
		if mode == "" {
			mode = "pie"
		}
		duration := orDefault(in.DurationS, 20)

		// 1. mark the log so we can attribute this run's errors/warnings, and
		// stamp the start so a crash dump can be attributed to THIS run.
		runStart := time.Now()
		var logMarker int64
		if resolveDeps(ctx, d).ProjectDir != "" {
			logMarker = logs.LogSize(logs.LogPath(resolveDeps(ctx, d).ProjectDir))
		}
		// 2. open the level if requested.
		if in.Level != "" {
			if _, err := bridgeFromCtx(ctx, b).CallText(ctx, "open_level", map[string]any{"level_path": in.Level}); err != nil {
				return nil, nil, err
			}
		}
		// 3. enter play (unless editor-world capture).
		if mode == "pie" || mode == "simulate" {
			if _, err := bridgeFromCtx(ctx, b).CallText(ctx, "start_play", map[string]any{"simulate": mode == "simulate"}); err != nil {
				return nil, nil, err
			}
			if err := sleepCtx(ctx, 1500*time.Millisecond); err != nil {
				return nil, nil, err
			}
			// Speed the sim so slow bottlenecks form in fewer real seconds (a big
			// iteration-time win); the recorder still samples on real-time interval.
			if in.TimeDilation > 0 && in.TimeDilation != 1 {
				_, _ = bridgeFromCtx(ctx, b).Call(ctx, "console", map[string]any{"command": fmt.Sprintf("slomo %g", in.TimeDilation)})
			}
		}
		// 4. start the in-editor recorder.
		source := "scene_capture"
		if mode == "pie" {
			source = "pie_highres"
		}
		world := "auto"
		if mode == "editor" {
			world = "editor"
		}
		startArgs := map[string]any{
			"world": world, "source": source,
			"interval_s":  orDefault(in.IntervalS, 0.5),
			"cell_width":  orDefaultInt(in.FrameW, 256),
			"cell_height": orDefaultInt(in.FrameH, 144),
			"max_frames":  orDefaultInt(in.MaxFrames, 64),
			"max_seconds": duration + 5,
		}
		if len(in.TrackActors) > 0 {
			startArgs["track_actors"] = in.TrackActors
		}
		obs := map[string]any{}
		if len(in.Include) > 0 {
			obs["include"] = in.Include
		}
		if len(in.Properties) > 0 {
			obs["properties"] = in.Properties
		}
		if len(obs) > 0 {
			startArgs["observe"] = obs
		}
		sraw, err := bridgeFromCtx(ctx, b).Call(ctx, "capture_start", startArgs)
		if err != nil {
			stopPlay(ctx, bridgeFromCtx(ctx, b), mode)
			return nil, nil, err
		}
		var sres struct {
			Session string `json:"session"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(sraw, &sres)
		if sres.Error != "" {
			stopPlay(ctx, bridgeFromCtx(ctx, b), mode)
			return nil, nil, fmt.Errorf("capture_start: %s", sres.Error)
		}

		// 5. run the deterministic beats across the capture window (the recorder
		// ticks in-editor meanwhile; Go only issues sparse beats, never per-frame).
		runBeats(ctx, bridgeFromCtx(ctx, b), in.Beats, duration)

		// 6/7. Tear down on a DETACHED context so a mid-run cancellation still
		// stops the in-editor recorder and leaves play — otherwise the slate
		// callback + SceneCapture2D leak and PIE stays running.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		stopRaw, stopErr := bridgeFromCtx(ctx, b).Call(cleanupCtx, "capture_stop", map[string]any{"session": sres.Session})
		stopPlay(cleanupCtx, bridgeFromCtx(ctx, b), mode)
		if stopErr != nil {
			// A hard PIE crash IS the dropped channel that fails capture_stop, so
			// this is the headline self-correction case: diagnose from the crash
			// dump / log instead of returning a bare transport error.
			if rep := detectCrash(resolveDeps(ctx, d).ProjectDir, logMarker, runStart); rep != nil {
				return nil, map[string]any{"verdict": "FAIL", "error": stopErr.Error(), "crash": rep}, nil
			}
			return nil, nil, stopErr
		}
		var cr captureStopResult
		if err := json.Unmarshal(stopRaw, &cr); err != nil {
			return nil, nil, err
		}

		// 8. attribute the log window AND look for a crash (a hard crash kills
		// the in-editor bridge, so the Go-side crash reader is the only witness).
		logSum := eval.LogSummary{}
		if resolveDeps(ctx, d).ProjectDir != "" {
			if text, _, rerr := logs.ReadFrom(logs.LogPath(resolveDeps(ctx, d).ProjectDir), logMarker); rerr == nil {
				lines := logs.FilterLines(text, "Warning", nil)
				logSum.Errors, logSum.Warnings, logSum.Ensures = logs.CountBySeverity(lines)
			}
		}
		crashRep := detectCrash(resolveDeps(ctx, d).ProjectDir, logMarker, runStart)

		if len(cr.Frames) == 0 {
			// Zero frames usually means the run died early — surface the crash if there is one.
			out := map[string]any{"error": "playtest produced 0 frames (was the world ticking? for possessed PIE use mode=pie)",
				"logs": map[string]any{"errors": logSum.Errors, "warnings": logSum.Warnings, "ensures": logSum.Ensures}}
			if crashRep != nil {
				out["crash"] = crashRep
			}
			return nil, out, nil
		}

		// 9. evaluate the rubric over the recorded timeline.
		samples := framesToSamples(cr.Frames)
		report := eval.Evaluate(samples, logSum, toRubricSpec(in.Rubric))
		markCells := failedFrameIndices(report)

		// 10. one montage (failed-check frames get a red border) + full sidecar.
		cols := orDefaultInt(in.Cols, 8)
		extra := map[string]any{
			"verdict":       report.Verdict,
			"rubric_result": reportToJSON(report),
			"logs":          map[string]any{"errors": logSum.Errors, "warnings": logSum.Warnings, "ensures": logSum.Ensures},
			"session_dir":   cr.Dir,
		}
		if crashRep != nil {
			extra["crash"] = crashRep
		}
		return buildMontageResult(cr.Dir, cr.Frames, cols, true, markCells, extra)
	}
}

// runBeats plays the beats in scheduled order, then sleeps out the remaining
// window. Beats are relative to the capture start (now).
func runBeats(ctx context.Context, b *bridge.Bridge, beats []playtestBeat, duration float64) {
	start := time.Now()
	for _, beat := range orderBeats(beats) {
		if beat.AtS > 0 {
			if sleepUntil(ctx, start.Add(secs(beat.AtS))) != nil {
				return
			}
		}
		if beat.Exec != nil {
			_, _ = b.Call(ctx, "pie_exec", map[string]any{
				"target": beat.Exec.Target, "ufunction": beat.Exec.UFunction, "args": beat.Exec.Args})
		}
		if beat.WaitUntil != "" {
			waitPredicate(ctx, b, beat.WaitUntil, orDefault(beat.TimeoutS, 10))
		}
	}
	sleepUntil(ctx, start.Add(secs(duration)))
}

// orderBeats returns the beats sorted by at_s ascending (stable), so unset (0)
// beats run first in declaration order. Pure — unit tested.
func orderBeats(beats []playtestBeat) []playtestBeat {
	out := make([]playtestBeat, len(beats))
	copy(out, beats)
	sort.SliceStable(out, func(i, j int) bool { return out[i].AtS < out[j].AtS })
	return out
}

// waitPredicate polls pie_observe until the predicate holds or timeout.
func waitPredicate(ctx context.Context, b *bridge.Bridge, expr string, timeoutS float64) {
	pred, err := eval.ParsePredicate(expr)
	if err != nil {
		return
	}
	deadline := time.Now().Add(secs(timeoutS))
	for time.Now().Before(deadline) {
		if raw, cerr := b.Call(ctx, "pie_observe", map[string]any{}); cerr == nil {
			var st map[string]any
			if json.Unmarshal(raw, &st) == nil {
				if ok, _ := pred.Eval(st); ok {
					return
				}
			}
		}
		if sleepCtx(ctx, 250*time.Millisecond) != nil {
			return
		}
	}
}

// detectCrash diagnoses a run's death from the attributed log window and the
// crash dump, preferring whichever yields a source location. An access violation
// carries no [File:Line] in the log banner — only the symbolicated [Callstack]
// frames or the crash dump do — so a fileless log report is backfilled from the
// crash dir rather than shadowing it.
func detectCrash(projectDir string, marker int64, since time.Time) *crash.Report {
	if projectDir == "" {
		return nil
	}
	var rep *crash.Report
	if text, _, err := logs.ReadFrom(logs.LogPath(projectDir), marker); err == nil {
		rep = crash.ScanLog(text)
	}
	if rep == nil || rep.File == "" {
		if x, _ := crash.FromCrashDir(projectDir, since); x != nil {
			if rep == nil {
				rep = x
			} else {
				rep.File, rep.Line = x.File, x.Line
				if len(rep.Frames) == 0 {
					rep.Frames = x.Frames
				}
			}
		}
	}
	return rep
}

func stopPlay(ctx context.Context, b *bridge.Bridge, mode string) {
	if mode == "pie" || mode == "simulate" {
		_, _ = b.CallText(ctx, "stop_play", map[string]any{})
	}
}

func framesToSamples(frames []captureFrame) []eval.Sample {
	out := make([]eval.Sample, len(frames))
	for i, f := range frames {
		out[i] = eval.Sample{Index: f.Index, TWorld: f.TWorld, State: f.State}
	}
	return out
}

func toRubricSpec(checks []rubricCheckIn) eval.RubricSpec {
	spec := eval.RubricSpec{Checks: make([]eval.Check, len(checks))}
	for i, c := range checks {
		spec.Checks[i] = eval.Check{ID: c.ID, Kind: c.Kind, Path: c.Path, Params: c.Params, Severity: c.Severity}
	}
	return spec
}

func reportToJSON(r eval.Report) map[string]any {
	checks := make([]map[string]any, len(r.Checks))
	for i, c := range r.Checks {
		m := map[string]any{"id": c.ID, "kind": c.Kind, "severity": c.Severity, "passed": c.Passed, "message": c.Message}
		if c.Evidence != nil {
			m["evidence"] = map[string]any{"frame": c.Evidence.FrameIndex, "t": c.Evidence.TWorld, "value": c.Evidence.Value}
		}
		checks[i] = m
	}
	return map[string]any{"verdict": r.Verdict, "checks": checks}
}

// failedFrameIndices returns the distinct frame indices cited by failed checks,
// so the montage can red-border the visual evidence of each failure.
func failedFrameIndices(r eval.Report) []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range r.Checks {
		if !c.Passed && c.Evidence != nil && c.Evidence.FrameIndex >= 0 && !seen[c.Evidence.FrameIndex] {
			seen[c.Evidence.FrameIndex] = true
			out = append(out, c.Evidence.FrameIndex)
		}
	}
	return out
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func sleepUntil(ctx context.Context, t time.Time) error {
	d := time.Until(t)
	if d <= 0 {
		return nil
	}
	return sleepCtx(ctx, d)
}
