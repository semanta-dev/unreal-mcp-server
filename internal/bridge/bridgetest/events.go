package bridgetest

import "time"

// EventFixture is what the fake's events session reports (R5.1): engine events (with
// PluginAPI >= 8), gaps, and the game journal's entries that carry a t.
type EventFixture struct {
	Engine  []map[string]any
	Gaps    []map[string]any
	Started []map[string]any // events_start args, in order
	Stopped int
	Seeds   []int // seed_random calls, in order
	// OnSeed, if set, runs on each seed_random (e.g. to vary the journal per seed).
	OnSeed func(seed int)
}

func (w *World) installEvents(e *Emulator) {
	running := map[string]float64{}
	e.Handle("events_start", func(args map[string]any) (any, *OpError) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.pie == nil {
			return nil, &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
		}
		w.Events.Started = append(w.Events.Started, args)
		session, _ := args["session"].(string)
		running[session] = time.Since(w.pieStarted).Seconds()
		sources := map[string]any{"server": "recorded", "engine": "recorded", "journal": "unavailable"}
		why := map[string]any{}
		if w.PluginAPI < 8 {
			sources["engine"], why["engine"] = "unavailable", "the UnrealMCP plugin API is below 8"
		}
		if j, ok := args["journal"].(map[string]any); ok && j["class"] == w.Game.Class {
			sources["journal"] = "recorded"
		} else if s, ok := args["journal_why"].(string); ok {
			why["journal"] = s
		}
		w.eventSources, w.eventWhy = sources, why
		return map[string]any{"session": session, "sources": sources, "source_why": why}, nil
	})
	e.Handle("seed_random", func(args map[string]any) (any, *OpError) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.PluginAPI < 8 {
			return nil, &OpError{Code: "PLUGIN_MISSING", Message: "seeded runs need the UnrealMCP plugin API 8"}
		}
		if w.pie == nil {
			return nil, &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
		}
		seed, _ := args["seed"].(float64)
		w.Events.Seeds = append(w.Events.Seeds, int(seed))
		if w.Events.OnSeed != nil {
			w.Events.OnSeed(int(seed))
		}
		return map[string]any{"seeded": seed}, nil
	})
	e.Handle("events_stop", func(args map[string]any) (any, *OpError) {
		w.mu.Lock()
		defer w.mu.Unlock()
		session, _ := args["session"].(string)
		start, ok := running[session]
		if !ok {
			return nil, &OpError{Code: "NOT_FOUND", Message: "no event session " + session}
		}
		delete(running, session)
		w.Events.Stopped++
		var evs []map[string]any
		if w.eventSources["engine"] == "recorded" {
			for _, ev := range w.Events.Engine {
				evs = append(evs, with(ev, "source", "engine"))
			}
		}
		if w.eventSources["journal"] == "recorded" {
			for _, ev := range w.Game.Journal {
				if _, has := ev["t"]; has {
					evs = append(evs, with(ev, "source", "journal"))
				}
			}
		}
		gaps := w.Events.Gaps
		if gaps == nil {
			gaps = []map[string]any{}
		}
		return map[string]any{"session": session, "start_t": start, "end_t": start + 120, "events": evs, "gaps": gaps,
			"sources": w.eventSources, "source_why": w.eventWhy, "engine": map[string]any{"unbound": 7.0, "still_bound": 0.0}}, nil
	})
}

func with(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{k: v}
	for kk, vv := range m {
		out[kk] = vv
	}
	return out
}
