package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
)

// R5.2: the feel and decision audits read a playtest result (its playtest.json, R5.1)
// directly — the event timeline is the evidence, built here rather than by the agent.

// playtestDoc is the part of playtest.json the audits read.
type playtestDoc struct {
	Events    []eval.Event      `json:"events"`
	Window    []float64         `json:"event_window"`
	Gaps      []eval.Gap        `json:"event_gaps"`
	Sources   map[string]string `json:"event_sources"`
	SourceWhy map[string]string `json:"event_source_why"`
}

func loadPlaytest(path string) (*playtestDoc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, envelope.New(envelope.NotFound, "source: %v", err).WithHint("source is a playtest result's playtest_path")
	}
	var d playtestDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "source: not a playtest.json: %v", err)
	}
	if d.Sources == nil || len(d.Window) != 2 {
		return nil, insufficientEvidence([]string{"events"}, "the playtest did not record events (scenario record_events: true)")
	}
	return &d, nil
}

// window narrows the run's window to [from_t, to_t] and refuses one that a gap or an
// unavailable source of a needed kind touches (R0.8: never score lost evidence).
func (d *playtestDoc) window(fromT, toT *float64, kinds []string) (float64, float64, error) {
	lo, hi := d.Window[0], d.Window[1]
	if fromT != nil {
		lo = math.Max(lo, *fromT)
	}
	if toT != nil {
		hi = math.Min(hi, *toT)
	}
	if hi <= lo {
		return 0, 0, envelope.New(envelope.InvalidArgument, "the window [%g, %g] s is outside the recording [%g, %g] s", lo, hi, d.Window[0], d.Window[1])
	}
	for _, k := range kinds {
		src := eval.EventSource(k)
		if d.Sources[src] != eval.SourceRecorded {
			why := d.SourceWhy[src]
			if why == "" {
				why = "not recorded"
			}
			return 0, 0, insufficientEvidence([]string{"events." + k}, fmt.Sprintf("%q events come from the %s source, which was %s (%s)", k, src, orStr(d.Sources[src], "off"), why))
		}
		for _, g := range d.Gaps {
			if g.Source == src && g.ToT >= lo && g.FromT <= hi {
				return 0, 0, insufficientEvidence([]string{"events." + k}, fmt.Sprintf("the %s source lost events in [%g, %g] s (%s, %d dropped): narrow from_t/to_t", src, g.FromT, g.ToT, g.Reason, g.Dropped))
			}
		}
	}
	return lo, hi, nil
}

func (d *playtestDoc) pick(kind string, lo, hi float64) []eval.Event {
	var out []eval.Event
	for _, e := range d.Events {
		if e.Kind == kind && e.T >= lo && e.T <= hi {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

type feelSourceIn struct {
	Source        string   `json:"source"`
	EventKinds    []string `json:"event_kinds,omitempty"`
	WithinMs      float64  `json:"within_ms,omitempty"`
	MaxFXPerEvent int      `json:"max_fx_per_event,omitempty"`
	FromT         *float64 `json:"from_t,omitempty"`
	ToT           *float64 `json:"to_t,omitempty"`
}

// feelFromPlaytest builds the feel audit's events: each event of event_kinds (default
// hit) with its visual response (visual_t, GAME_CONTRACT), the first sfx and
// camera_shake within the window after it, and the vfx in that window. A channel the
// run never journalled at all is missing evidence, not a missing response.
func feelFromPlaytest(raw []byte) (any, error) {
	var in feelSourceIn
	if err := strictDecode(raw, &in); err != nil {
		return nil, err
	}
	kinds := in.EventKinds
	if len(kinds) == 0 {
		kinds = []string{"hit"}
	}
	d, err := loadPlaytest(in.Source)
	if err != nil {
		return nil, err
	}
	lo, hi, err := d.window(in.FromT, in.ToT, append(append([]string{}, kinds...), "sfx", "camera_shake", "vfx"))
	if err != nil {
		return nil, err
	}
	within := orDefault(in.WithinMs, 120) / 1000
	sfx, cam, vfx := d.pick("sfx", lo, hi+within), d.pick("camera_shake", lo, hi+within), d.pick("vfx", lo, hi+within)
	var missing []string // channels the whole run never journalled (not: none in this window)
	for _, name := range []string{"camera_shake", "sfx", "vfx"} {
		if len(d.pick(name, math.Inf(-1), math.Inf(1))) == 0 {
			missing = append(missing, "events."+name)
		}
	}
	if len(missing) > 0 {
		return nil, insufficientEvidence(missing, "the run journalled no "+strings.Join(missing, ", ")+": the game may not report that channel")
	}
	first := func(evs []eval.Event, t float64) float64 {
		for _, e := range evs {
			if e.T >= t && e.T <= t+within {
				return e.T
			}
		}
		return 0
	}
	var events []audit.Event
	for _, k := range kinds {
		for _, e := range d.pick(k, lo, hi) {
			ae := audit.Event{T: e.T, Kind: audit.EventKind(k), Instigator: e.Actor, AudioT: first(sfx, e.T), CameraT: first(cam, e.T)}
			if e.VisualT != nil {
				ae.VisualT = *e.VisualT
			}
			for _, v := range vfx {
				if v.T >= e.T && v.T <= e.T+within {
					ae.FXCount++
				}
			}
			events = append(events, ae)
		}
	}
	if len(events) == 0 {
		return nil, insufficientEvidence([]string{"events." + strings.Join(kinds, "/")}, fmt.Sprintf("no %s events in [%g, %g] s", strings.Join(kinds, "/"), lo, hi))
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].T < events[j].T })
	return map[string]any{"source": in.Source, "window": []float64{lo, hi}, "events": len(events),
		"audit": audit.FeelAudit(events, orDefault(in.WithinMs, 120), orDefaultInt(in.MaxFXPerEvent, 4))}, nil
}

type decisionSourceIn struct {
	Source    string   `json:"source"`
	Verbs     []string `json:"verbs,omitempty"`
	Available []string `json:"available,omitempty"`
	MinGapS   float64  `json:"min_gap_s,omitempty"`
	FromT     *float64 `json:"from_t,omitempty"`
	ToT       *float64 `json:"to_t,omitempty"`
}

// consequences are event kinds that report what happened, not what the player chose.
var consequences = map[string]bool{"hit": true, "kill": true, "death": true, "damage": true, "point_damage": true,
	"vfx": true, "sfx": true, "camera_shake": true, "spawned": true, "destroyed": true, "wave_start": true, "wave_end": true}

// decisionFromPlaytest builds decision points from the player's own actions: events by
// the player whose kind is a verb (default: every by_player kind the game or engine
// recorded that is not a consequence; the playtest's input beats only when verbs names them). The choice is the kind (with data.weapon when present); repeats of one
// choice closer than min_gap_s (0.5) are one decision (held fire is one choice, not 30);
// available is the given list, else every distinct choice the run made.
func decisionFromPlaytest(raw []byte) (any, error) {
	var in decisionSourceIn
	if err := strictDecode(raw, &in); err != nil {
		return nil, err
	}
	d, err := loadPlaytest(in.Source)
	if err != nil {
		return nil, err
	}
	verbs := map[string]bool{}
	for _, v := range in.Verbs {
		verbs[v] = true
	}
	var needed []string
	if len(verbs) > 0 {
		needed = in.Verbs
	} else {
		for _, e := range d.Events {
			// The game's record of what the player did, not the playtest's own input beats
			// (held fire is an input beat and the weapon_fire events it caused).
			if e.ByPlayer && e.Source != eval.SourceServer && !consequences[e.Kind] && !verbs[e.Kind] {
				verbs[e.Kind] = true
				needed = append(needed, e.Kind)
			}
		}
	}
	lo, hi, err := d.window(in.FromT, in.ToT, needed)
	if err != nil {
		return nil, err
	}
	gap := orDefault(in.MinGapS, 0.5)
	var pts []audit.DecisionPoint
	lastT := map[string]float64{}
	seen := map[string]bool{}
	var order []string
	for _, e := range d.Events {
		if !e.ByPlayer || !verbs[e.Kind] || e.T < lo || e.T > hi {
			continue
		}
		choice := e.Kind
		if w, ok := e.Data["weapon"].(string); ok && w != "" {
			choice += ":" + w
		}
		if t, ok := lastT[choice]; ok && e.T-t < gap {
			lastT[choice] = e.T
			continue
		}
		lastT[choice] = e.T
		if !seen[choice] {
			seen[choice] = true
			order = append(order, choice)
		}
		pts = append(pts, audit.DecisionPoint{T: e.T, Chosen: choice})
	}
	if len(pts) == 0 {
		return nil, insufficientEvidence([]string{"events (player actions)"}, fmt.Sprintf("no player actions in [%g, %g] s", lo, hi))
	}
	avail := in.Available
	if len(avail) == 0 {
		avail = order
	}
	for i := range pts {
		pts[i].Available = avail
	}
	return map[string]any{"source": in.Source, "window": []float64{lo, hi}, "points": len(pts), "available": avail,
		"audit": audit.DecisionAudit(pts)}, nil
}

// withPlaytestSource runs from a playtest result when the input names one (source),
// else the audit's own input.
func withPlaytestSource(fromSource, own func([]byte) (any, error)) func([]byte) (any, error) {
	return func(raw []byte) (any, error) {
		var probe map[string]any
		if json.Unmarshal(raw, &probe) == nil {
			if _, ok := probe["source"]; ok {
				return fromSource(raw)
			}
		}
		return own(raw)
	}
}
