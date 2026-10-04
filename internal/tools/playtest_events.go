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

// eventTimeline records a playtest's gameplay events (R5.1): the companion's events
// session drains the plugin's engine recorder and the game's journal while the run
// plays; the playtest adds its own input and game-command beats (source server).
type eventTimeline struct {
	session string
	server  []eval.Event
	gaps    []eval.Gap
	started map[string]any
}

// startEventTimeline starts the events session for a running PIE. Without a game_api
// the journal source is unavailable (and says why), never a failure.
func startEventTimeline(ctx context.Context, c *spec.Call, session string) (*eventTimeline, error) {
	args := map[string]any{"session": session, "engine": true}
	if api, err := gameAPIOf(c); err == nil {
		args["journal"] = map[string]any{"class": api.ClassPath(), "function": api.Events, "capabilities": api.Capabilities}
	} else {
		args["journal_why"] = err.Error()
	}
	out, err := v2Op(ctx, c, "events_start", args)
	if err != nil {
		// The session may exist although the call failed (a timeout): stop it, best
		// effort, so its recorder does not stay bound (cleanup, not a retry).
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if b, berr := v2Bridge(c); berr == nil {
			_, _ = b.Call(cctx, "events_stop", map[string]any{"session": session})
		}
		return nil, err
	}
	return &eventTimeline{session: session, started: out}, nil
}

// note records a beat the playtest played, stamped with the game's clock.
func (t *eventTimeline) note(ctx context.Context, c *spec.Call, kind string, byPlayer bool, data map[string]any) {
	if t == nil {
		return
	}
	now, _, err := gameTime(ctx, c)
	if err != nil {
		last := 0.0
		if n := len(t.server); n > 0 {
			last = t.server[n-1].T
		}
		t.gaps = append(t.gaps, eval.Gap{Source: eval.SourceServer, FromT: last, ToT: math.Inf(1),
			Reason: "the game clock was unreadable after a " + kind + " beat: " + err.Error()})
		return
	}
	t.server = append(t.server, eval.Event{T: now, Kind: kind, ByPlayer: byPlayer, Data: data, Source: eval.SourceServer})
}

// stop ends the session (on a detached context: a cancelled run still unbinds the
// recorder) and returns the merged timeline plus the engine recorder's unbind report.
func (t *eventTimeline) stop(ctx context.Context, c *spec.Call) (*eval.EventLog, map[string]any, error) {
	b, err := v2Bridge(c)
	if err != nil {
		return nil, nil, err
	}
	raw, err := b.Call(ctx, "events_stop", map[string]any{"session": t.session})
	if err != nil {
		return nil, nil, err
	}
	var out struct {
		StartT    float64           `json:"start_t"`
		EndT      float64           `json:"end_t"`
		Events    []eval.Event      `json:"events"`
		Gaps      []eval.Gap        `json:"gaps"`
		Sources   map[string]string `json:"sources"`
		SourceWhy map[string]string `json:"source_why"`
		Kinds     []string          `json:"journal_kinds"`
		Engine    map[string]any    `json:"engine"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil, fmt.Errorf("events_stop: %w", err)
	}
	log := &eval.EventLog{StartT: out.StartT, EndT: out.EndT, Sources: out.Sources, SourceWhy: out.SourceWhy, JournalKinds: out.Kinds,
		Events: append(out.Events, t.server...), Gaps: append(out.Gaps, t.gaps...)}
	for i := range log.Gaps {
		if math.IsInf(log.Gaps[i].ToT, 1) {
			log.Gaps[i].ToT = log.EndT // a server gap lasts to the end of the run
		}
	}
	for i, e := range log.Events {
		// GAME_CONTRACT: a hit's data.visual_t is lifted to visual_t.
		if e.VisualT == nil && e.Data != nil {
			if v, ok := e.Data["visual_t"].(float64); ok {
				log.Events[i].VisualT = &v
			}
		}
	}
	sort.SliceStable(log.Events, func(i, j int) bool { return log.Events[i].T < log.Events[j].T })
	return log, out.Engine, nil
}

// eventSummary is the playtest result's view of the timeline (the events themselves
// are in playtest.json).
func eventSummary(log *eval.EventLog, engine map[string]any) map[string]any {
	byKind := map[string]int{}
	for _, e := range log.Events {
		byKind[e.Kind]++
	}
	out := map[string]any{"count": len(log.Events), "by_kind": byKind, "sources": log.Sources,
		"window": []float64{log.StartT, log.EndT}}
	if len(log.SourceWhy) > 0 {
		out["source_why"] = log.SourceWhy
	}
	if len(log.Gaps) > 0 {
		out["gaps"] = log.Gaps
	}
	if engine != nil {
		out["engine"] = engine
	}
	return out
}

// writePlaytestJSON writes <capture dir>/playtest.json (GAME_CONTRACT, R5 contract): the
// verdict, rubric and the merged event timeline.
func writePlaytestJSON(dir string, result map[string]any, log *eval.EventLog) (string, error) {
	doc := map[string]any{"schema": "playtest/v1", "written": time.Now().UTC().Format(time.RFC3339)}
	for _, k := range []string{"scenario", "session", "verdict", "verdict_reasons", "rubric", "beat_errors", "logs", "crash", "perf_csv", "timeline"} {
		if v, ok := result[k]; ok {
			doc[k] = v
		}
	}
	if log != nil {
		doc["events"] = log.Events
		doc["event_window"] = []float64{log.StartT, log.EndT}
		doc["event_gaps"] = log.Gaps
		doc["event_sources"] = log.Sources
		doc["event_journal_kinds"] = log.JournalKinds
		if len(log.SourceWhy) > 0 {
			doc["event_source_why"] = log.SourceWhy
		}
	}
	raw, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "playtest.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(p), nil
}
