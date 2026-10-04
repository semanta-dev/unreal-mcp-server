package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/perf"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R5.4: playtest perf=true plays the scenario a second time with nothing of ours
// running — no frame capture, no event recorder — under the engine's CsvProfiler, and
// scores perf_csv.* from that CSV (the game's frame times, not the recorder's).

// errUnseeded: a pass of a seeded run whose seed could not be applied (it FAILs the run).
var errUnseeded = errors.New("the pass was not seeded")

// perfPass is one CsvProfiler run of the scenario's beats. It returns the perf_csv
// values, the CSV's path, and the pass's beat errors (prefixed with the pass's name).
// withEvents runs the event recorder through the pass (to measure its overhead).
func perfPass(ctx context.Context, c *spec.Call, sc *eval.Scenario, duration float64, progress func(string), withEvents bool, seed *int) (map[string]float64, string, []string, error) {
	name := "perf pass"
	if withEvents {
		name = "perf pass with events"
	}
	pd := c.Deps.ProjectDir
	if pd == "" {
		return nil, "", nil, fmt.Errorf("no project directory: the CsvProfiler CSV is written under the project's Saved/Profiling/CSV")
	}
	csvDir := filepath.Join(pd, "Saved", "Profiling", "CSV")
	before := csvFiles(csvDir)
	stopPIE := func() error { return stopPIEAndWait(ctx, c) }
	progress(name + ": starting " + orStr(sc.Mode, "pie"))
	forgetGameWorld(c)
	if _, err := v2Op(ctx, c, "pie_start", map[string]any{"simulate": sc.Mode == "simulate"}); err != nil {
		return nil, "", nil, err
	}
	if err := waitPIE(ctx, c, true, 20*time.Second); err != nil {
		stopPIE()
		return nil, "", nil, err
	}
	var errs []string
	if seed != nil {
		if serrs := applySeed(ctx, c, sc, *seed); len(serrs) > 0 {
			_ = stopPIE()
			return nil, "", nil, fmt.Errorf("%w: %s: %s", errUnseeded, name, strings.Join(serrs, "; "))
		}
	}
	if sc.TimeDilation > 0 && sc.TimeDilation != 1 {
		_, _ = v2Op(ctx, c, "console", map[string]any{"command": fmt.Sprintf("slomo %g", sc.TimeDilation), "world": "pie"})
	}
	for _, sp := range sc.Setup.SetProps {
		if _, err := v2Op(ctx, c, "actor_set_properties", map[string]any{"world": "pie", "actor": beatTarget(sp.Target), "properties": sp.Properties}); err != nil {
			errs = append(errs, name+": setup "+sp.Target+": "+err.Error())
		}
	}
	if _, err := v2Op(ctx, c, "console", map[string]any{"command": "csvprofile start", "world": "pie"}); err != nil {
		stopPIE()
		return nil, "", errs, fmt.Errorf("csvprofile start: %w", err)
	}
	var timeline *eventTimeline
	if withEvents {
		var err error
		if timeline, err = startEventTimeline(ctx, c, fmt.Sprintf("perf_%d", time.Now().UnixNano())); err != nil {
			errs = append(errs, name+": record_events: "+err.Error())
		}
	}
	progress(fmt.Sprintf("%s: profiling for %.0fs", name, duration))
	for _, e := range runBeatsV2(ctx, c, sc.Beats, duration, progress, timeline) {
		errs = append(errs, name+": "+e)
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if timeline != nil {
		if _, _, err := timeline.stop(cctx, c); err != nil {
			errs = append(errs, name+": record_events: "+err.Error())
		}
	}
	_, stopErr := v2Op(cctx, c, "console", map[string]any{"command": "csvprofile stop", "world": "pie"})
	// The CSV is written after the capture ends (asynchronously): wait for a new file
	// whose size has settled, while PIE still runs.
	path, err := waitNewCSV(cctx, csvDir, before)
	if serr := stopPIE(); serr != nil {
		errs = append(errs, name+": "+serr.Error())
	}
	if stopErr != nil {
		return nil, "", errs, fmt.Errorf("csvprofile stop: %w", stopErr)
	}
	if err != nil {
		return nil, "", errs, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", errs, err
	}
	fs, err := perf.ParseCSV(string(raw), 0)
	if err != nil {
		return nil, filepath.ToSlash(path), errs, fmt.Errorf("the CsvProfiler CSV did not parse: %w", err)
	}
	return map[string]float64{"frames": float64(fs.Count), "mean_frame_ms": fs.MeanMs, "p50_frame_ms": fs.P50Ms,
		"p95_frame_ms": fs.P95Ms, "p99_frame_ms": fs.P99Ms, "max_frame_ms": fs.MaxMs, "hitch_count": float64(fs.HitchCount)}, filepath.ToSlash(path), errs, nil
}

func csvFiles(dir string) map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".csv") {
			out[e.Name()] = true
		}
	}
	return out
}

func waitNewCSV(ctx context.Context, dir string, before map[string]bool) (string, error) {
	var last string
	var lastSize int64 = -1
	for {
		for name := range csvFiles(dir) {
			if before[name] {
				continue
			}
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil {
				if p == last && st.Size() == lastSize && st.Size() > 0 {
					return p, nil
				}
				last, lastSize = p, st.Size()
			}
		}
		if sleepCtx(ctx, 500*time.Millisecond) != nil {
			if last != "" {
				return "", fmt.Errorf("the CsvProfiler CSV %s was still being written", filepath.Base(last))
			}
			return "", fmt.Errorf("no CsvProfiler CSV appeared in %s (is the CSV profiler compiled into this build?)", dir)
		}
	}
}
