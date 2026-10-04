package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// R5.5: playtest op=batch plays the scenario once per seed and reports the spread —
// verdict counts, each check's outcomes, and the mean / stdev / range of what the runs
// measured — in the result and in Saved/MCP/playtest/batch-<id>.json.

// maxSeeds bounds a batch (each run is a full playtest).
const maxSeeds = 20

// applySeed seeds a run that just entered PIE: the engine's random streams (plugin 8),
// then the scenario's seed_command if it has one. Failures are setup errors.
func applySeed(ctx context.Context, c *spec.Call, sc *eval.Scenario, seed int) []string {
	var errs []string
	if _, err := v2Op(ctx, c, "seed_random", map[string]any{"seed": seed}); err != nil {
		errs = append(errs, fmt.Sprintf("seed %d: %v", seed, err))
	}
	if sc.SeedCommand != "" {
		id := fmt.Sprintf("seed-%d-%d", seed, time.Now().UnixNano())
		if _, err := runGameCommand(ctx, c, gameCommandIn{Name: sc.SeedCommand, Args: map[string]any{"seed": seed}, RequestID: id}); err != nil {
			errs = append(errs, fmt.Sprintf("seed_command %s: %v", sc.SeedCommand, err))
		}
	}
	return errs
}

type stat struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Stdev  float64 `json:"stdev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	values []float64
}

func (s *stat) add(v float64) { s.values = append(s.values, v) }

func (s *stat) done() *stat {
	s.N = len(s.values)
	if s.N == 0 {
		return s
	}
	s.Min, s.Max = math.Inf(1), math.Inf(-1)
	sum := 0.0
	for _, v := range s.values {
		sum += v
		s.Min, s.Max = math.Min(s.Min, v), math.Max(s.Max, v)
	}
	s.Mean = sum / float64(s.N)
	if s.N > 1 {
		ss := 0.0
		for _, v := range s.values {
			ss += (v - s.Mean) * (v - s.Mean)
		}
		s.Stdev = math.Sqrt(ss / float64(s.N-1)) // sample stdev
	}
	return s
}

type batchRun struct {
	Seed         int                `json:"seed"`
	Verdict      string             `json:"verdict"`
	Error        string             `json:"error,omitempty"`
	BeatErrors   []string           `json:"beat_errors,omitempty"`
	PlaytestPath string             `json:"playtest_path,omitempty"`
	Waves        []map[string]any   `json:"waves"`
	Checks       map[string]string  `json:"checks,omitempty"` // id -> passed | failed | insufficient
	Values       map[string]float64 `json:"values,omitempty"` // check id -> the number it measured
	Events       map[string]float64 `json:"events,omitempty"` // kind -> count
}

func runBatch(ctx context.Context, c *spec.Call, sc *eval.Scenario, in playtestIn, progress func(string)) (map[string]any, error) {
	id := time.Now().UTC().Format("20060102T150405")
	var runs []batchRun
	for i, seed := range in.Seeds {
		if ctx.Err() != nil {
			break
		}
		s := seed
		prefix := fmt.Sprintf("seed %d (%d/%d): ", seed, i+1, len(in.Seeds))
		progress(prefix + "starting")
		res, err := runPlaytest(ctx, c, sc, in, func(m string) { progress(prefix + m) }, &s)
		runs = append(runs, summarizeRun(seed, res, err))
	}
	out := aggregateBatch(id, sc.Name, runs)
	if len(runs) < len(in.Seeds) {
		out["incomplete"] = fmt.Sprintf("cancelled after %d of %d runs", len(runs), len(in.Seeds))
	}
	if pd := c.Deps.ProjectDir; pd != "" {
		dir := filepath.Join(pd, "Saved", "MCP", "playtest")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			p := filepath.Join(dir, "batch-"+id+".json")
			if raw, err := json.MarshalIndent(out, "", " "); err == nil && os.WriteFile(p, raw, 0o644) == nil {
				out["batch_path"] = filepath.ToSlash(p)
			}
		}
	}
	return out, nil
}

func summarizeRun(seed int, res map[string]any, err error) batchRun {
	r := batchRun{Seed: seed, Waves: []map[string]any{}, Checks: map[string]string{}, Values: map[string]float64{}, Events: map[string]float64{}}
	if err != nil {
		r.Verdict, r.Error = "FAIL", err.Error()
		return r
	}
	r.Verdict, _ = res["verdict"].(string)
	if e, ok := res["error"].(string); ok {
		r.Error = e
	}
	if be, ok := res["beat_errors"].([]string); ok {
		r.BeatErrors = be
	}
	if p, ok := res["playtest_path"].(string); ok {
		r.PlaytestPath = p
		if raw, err := os.ReadFile(p); err == nil {
			var doc struct {
				Events []eval.Event `json:"events"`
			}
			if json.Unmarshal(raw, &doc) == nil {
				for _, e := range doc.Events {
					r.Events[e.Kind]++
					if e.Kind == "wave_end" {
						r.Waves = append(r.Waves, map[string]any{"wave": e.Data["wave"], "clear_s": e.Data["clear_s"]})
					}
				}
			}
		}
	}
	if rb, ok := res["rubric"].(map[string]any); ok {
		checks, _ := rb["checks"].([]map[string]any)
		for _, ch := range checks {
			id := fmt.Sprint(ch["id"])
			switch {
			case ch["insufficient"] == true:
				r.Checks[id] = "insufficient"
			case ch["passed"] == true:
				r.Checks[id] = "passed"
			default:
				r.Checks[id] = "failed"
			}
			if ev, ok := ch["evidence"].(map[string]any); ok {
				if v, ok := measured(ev["value"]); ok {
					r.Values[id] = v
				}
			}
		}
	}
	return r
}

// measured is the one number a check's evidence carries: a plain value, or an event
// check's rate / statistic.
func measured(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case map[string]any:
		if r, ok := x["rate"].(float64); ok {
			return r, true
		}
		for k, val := range x {
			if f, ok := val.(float64); ok && len(k) > 2 && k[len(k)-2:] == "_s" {
				return f, true
			}
		}
	}
	return 0, false
}

func aggregateBatch(id, scenario string, runs []batchRun) map[string]any {
	verdicts := map[string]int{}
	worst := "PASS"
	checks := map[string]map[string]int{}
	values, events, waves := map[string]*stat{}, map[string]*stat{}, map[string]*stat{}
	kinds := map[string]bool{}
	for _, r := range runs {
		for k := range r.Events {
			kinds[k] = true
		}
	}
	for _, r := range runs {
		verdicts[r.Verdict]++
		if rank(r.Verdict) > rank(worst) {
			worst = r.Verdict
		}
		for id, o := range r.Checks {
			if checks[id] == nil {
				checks[id] = map[string]int{}
			}
			checks[id][o]++
		}
		for id, v := range r.Values {
			if values[id] == nil {
				values[id] = &stat{}
			}
			values[id].add(v)
		}
		if r.PlaytestPath != "" {
			for k := range kinds { // a kind a run never saw counts as 0 for it
				if events[k] == nil {
					events[k] = &stat{}
				}
				events[k].add(r.Events[k])
			}
		}
		for _, w := range r.Waves {
			cs, ok := w["clear_s"].(float64)
			if !ok {
				continue
			}
			key := fmt.Sprint(w["wave"])
			if waves[key] == nil {
				waves[key] = &stat{}
			}
			waves[key].add(cs)
		}
	}
	finish := func(m map[string]*stat) map[string]*stat {
		for _, s := range m {
			s.done()
		}
		return m
	}
	waveList := []map[string]any{}
	for k, s := range finish(waves) {
		waveList = append(waveList, map[string]any{"wave": k, "clear_s": s})
	}
	sort.Slice(waveList, func(i, j int) bool { return waveKey(waveList[i]["wave"]) < waveKey(waveList[j]["wave"]) })
	if worst == "" {
		worst = "FAIL"
	}
	return map[string]any{"schema": "playtest-batch/v1", "id": id, "scenario": scenario, "verdict": worst,
		"verdicts": verdicts, "checks": checks, "values": finish(values), "events": finish(events), "waves": waveList, "runs": runs}
}

func waveKey(v any) float64 {
	var f float64
	if _, err := fmt.Sscan(fmt.Sprint(v), &f); err != nil {
		return math.Inf(1)
	}
	return f
}
