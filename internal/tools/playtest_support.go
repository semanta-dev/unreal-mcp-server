package tools

import (
	"context"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/crash"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
)

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

func framesToSamples(frames []captureFrame) []eval.Sample {
	out := make([]eval.Sample, len(frames))
	for i, f := range frames {
		out[i] = eval.Sample{Index: f.Index, TWorld: f.TWorld, State: f.State}
	}
	return out
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
