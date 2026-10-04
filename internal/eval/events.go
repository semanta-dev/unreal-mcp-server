// events (merged into package eval) evaluates the telemetry rubric kinds that read a
// playtest's gameplay events rather than its sampled frames (R5.3): histogram, rate
// and time_between, plus the CsvProfiler pass's perf_csv.* values (R5.4).
//
// Evidence rules (R0.8): a check whose events may be incomplete — the recording was
// off, the source of the event kind was unavailable, or a gap (a dropped plugin
// buffer, a game-journal gap) overlaps its window — is never scored: it reports
// Insufficient and the verdict says INSUFFICIENT_EVIDENCE.
package eval

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Event is one gameplay event of a playtest timeline (GAME_CONTRACT.md, R5 contract).
// Source is engine (the plugin's recorder), journal (the game's GetEventsSince) or
// server (what the playtest itself did: input beats, game commands).
type Event struct {
	T        float64        `json:"t"`
	Kind     string         `json:"kind"`
	Actor    string         `json:"actor,omitempty"`
	Target   string         `json:"target,omitempty"`
	ByPlayer bool           `json:"by_player"`
	Data     map[string]any `json:"data,omitempty"`
	VisualT  *float64       `json:"visual_t,omitempty"`
	Source   string         `json:"source,omitempty"`
}

// Gap is a stretch of world time in which a source's events may be missing.
type Gap struct {
	Source  string  `json:"source"`
	FromT   float64 `json:"from_t"`
	ToT     float64 `json:"to_t"`
	Dropped int     `json:"dropped,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

// Event sources and their states in EventLog.Sources.
const (
	SourceEngine  = "engine"
	SourceJournal = "journal"
	SourceServer  = "server"

	SourceRecorded    = "recorded"
	SourceUnavailable = "unavailable"
)

// EngineEventKinds are the kinds only the plugin's recorder produces (§5 matrix);
// ServerEventKinds the playtest's own. Every other kind is the game journal's.
var (
	EngineEventKinds = map[string]bool{"damage": true, "point_damage": true, "spawned": true, "destroyed": true}
	ServerEventKinds = map[string]bool{"input": true, "game_command": true}
)

// EventSource names the source an event kind comes from.
func EventSource(kind string) string {
	switch {
	case EngineEventKinds[kind]:
		return SourceEngine
	case ServerEventKinds[kind]:
		return SourceServer
	}
	return SourceJournal
}

// EventLog is a playtest's merged event timeline. StartT/EndT bound the recording
// window (world seconds); Sources maps engine/journal/server to recorded or
// unavailable (with SourceWhy saying why).
type EventLog struct {
	StartT    float64           `json:"start_t"`
	EndT      float64           `json:"end_t"`
	Events    []Event           `json:"events"`
	Gaps      []Gap             `json:"gaps"`
	Sources   map[string]string `json:"sources"`
	SourceWhy map[string]string `json:"source_why,omitempty"`
}

// Inputs is everything a rubric can read: the sampled frames, the log tally, the
// event timeline (nil when the run did not record events) and the CsvProfiler pass's
// values (nil when the run had no perf pass).
type Inputs struct {
	Timeline []Sample
	Logs     LogSummary
	Events   *EventLog
	PerfCSV  map[string]float64
}

// EventKinds are the rubric kinds that read Inputs.Events.
var EventKinds = map[string]bool{"histogram": true, "rate": true, "time_between": true}

// PerfCSVFields are the values a perf pass reports under perf_csv.*.
var PerfCSVFields = []string{"frames", "mean_frame_ms", "p50_frame_ms", "p95_frame_ms", "p99_frame_ms", "max_frame_ms", "hitch_count"}

// eventPathKind is the event kind of an events.<kind> path ("" when it is not one).
func eventPathKind(path string) string {
	k, ok := strings.CutPrefix(path, "events.")
	if !ok || k == "" || strings.Contains(k, ".") {
		return ""
	}
	return k
}

// checkEventParams validates an event check's params (shared by the lint, so a bad
// check is refused when the scenario is parsed, and by the evaluator).
func checkEventParams(kind, path string, p map[string]any) string {
	if eventPathKind(path) == "" {
		return fmt.Sprintf("%s reads path events.<kind> (e.g. events.kill), not %q", kind, path)
	}
	has := func(k string) bool { _, ok := p[k]; return ok }
	num := func(k string) bool { _, ok := rubricFloat(p[k]); return ok }
	for k := range p {
		if !eventParams[kind][k] {
			return fmt.Sprintf("%s takes no param %q (it takes %s)", kind, k, strings.Join(sortedKeys(eventParams[kind]), ", "))
		}
	}
	for _, k := range []string{"min", "max", "min_buckets", "max_share", "min_count", "from_t", "to_t"} {
		if has(k) && !num(k) {
			return fmt.Sprintf("params.%s must be a number", k)
		}
	}
	if has("from_t") && has("to_t") && toF(p["from_t"]) >= toF(p["to_t"]) {
		return "params.from_t must be before params.to_t"
	}
	if has("by_player") {
		if _, ok := p["by_player"].(bool); !ok {
			return "params.by_player must be true or false"
		}
	}
	for _, k := range []string{"by", "from", "per", "stat"} {
		if has(k) {
			if s, ok := p[k].(string); !ok || s == "" {
				return fmt.Sprintf("params.%s must be a non-empty string", k)
			}
		}
	}
	switch kind {
	case "histogram":
		if !has("by") {
			return "histogram needs params.by (the field to group by: actor, target or data.<name>)"
		}
		if !has("min_buckets") && !has("max_share") && !has("min_count") {
			return "histogram needs at least one of params.min_buckets, max_share, min_count"
		}
		if has("max_share") && (toF(p["max_share"]) <= 0 || toF(p["max_share"]) > 1) {
			return "params.max_share is a share in (0, 1]"
		}
	case "rate", "time_between":
		if !has("min") && !has("max") {
			return kind + " needs params.min and/or params.max"
		}
		if has("min") && has("max") && toF(p["min"]) > toF(p["max"]) {
			return "params.min is above params.max"
		}
		if kind == "rate" && has("per") && p["per"] != "second" && p["per"] != "minute" {
			return "params.per is second or minute"
		}
		if kind == "time_between" && has("stat") && !timeStats[fmt.Sprint(p["stat"])] {
			return "params.stat is one of " + strings.Join(sortedKeys(timeStats), ", ")
		}
	}
	return ""
}

var eventParams = map[string]map[string]bool{
	"histogram":    {"by": true, "min_buckets": true, "max_share": true, "min_count": true, "by_player": true, "from_t": true, "to_t": true},
	"rate":         {"per": true, "min": true, "max": true, "by_player": true, "from_t": true, "to_t": true},
	"time_between": {"from": true, "by": true, "stat": true, "min": true, "max": true, "by_player": true, "from_t": true, "to_t": true},
}

var timeStats = map[string]bool{"p50": true, "p95": true, "mean": true, "min": true, "max": true}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func toF(v any) float64 { f, _ := rubricFloat(v); return f }

// evalEventCheck runs one histogram / rate / time_between check.
func evalEventCheck(log *EventLog, c Check, res *CheckResult) {
	if msg := checkEventParams(c.Kind, c.Path, c.Params); msg != "" {
		res.Message = msg
		return
	}
	kind := eventPathKind(c.Path)
	kinds := []string{kind}
	if from, ok := c.Params["from"].(string); ok && from != kind {
		kinds = append(kinds, from)
	}
	if log == nil {
		insufficient(res, "the run recorded no events (set record_events: true in the scenario)")
		return
	}
	from, to := log.StartT, log.EndT
	if v, ok := paramFloat(c, "from_t"); ok {
		from = math.Max(from, v)
	}
	if v, ok := paramFloat(c, "to_t"); ok {
		to = math.Min(to, v)
	}
	if to <= from {
		insufficient(res, fmt.Sprintf("the window [%g, %g] s is outside the recording [%g, %g] s", from, to, log.StartT, log.EndT))
		return
	}
	for _, k := range kinds {
		src := EventSource(k)
		if state := log.Sources[src]; state != SourceRecorded {
			why := log.SourceWhy[src]
			if why == "" {
				why = "not recorded"
			}
			insufficient(res, fmt.Sprintf("%q events come from the %s source, which was %s (%s)", k, src, orDefault(state, "off"), why))
			return
		}
		for _, g := range log.Gaps {
			if g.Source == src && g.ToT >= from && g.FromT <= to {
				insufficient(res, fmt.Sprintf("the %s source has a gap in [%g, %g] s (%s): %q events may be missing",
					src, g.FromT, g.ToT, gapWhy(g), k))
				return
			}
		}
	}
	byPlayer, filterPlayer := c.Params["by_player"].(bool)
	pick := func(k string) []Event {
		var out []Event
		for _, e := range log.Events {
			if e.Kind == k && e.T >= from && e.T <= to && (!filterPlayer || e.ByPlayer == byPlayer) {
				out = append(out, e)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
		return out
	}
	switch c.Kind {
	case "rate":
		evalRate(pick(kind), to-from, c, res)
	case "histogram":
		evalHistogram(pick(kind), c, res)
	case "time_between":
		var fromEvents []Event
		if f, ok := c.Params["from"].(string); ok {
			fromEvents = pick(f)
		}
		evalTimeBetween(fromEvents, pick(kind), c, res)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func gapWhy(g Gap) string {
	switch {
	case g.Reason != "" && g.Dropped > 0:
		return fmt.Sprintf("%s, %d dropped", g.Reason, g.Dropped)
	case g.Dropped > 0:
		return fmt.Sprintf("%d dropped", g.Dropped)
	case g.Reason != "":
		return g.Reason
	}
	return "events lost"
}

func insufficient(res *CheckResult, why string) {
	res.Insufficient = true
	res.Message = "insufficient evidence: " + why
}

// bounds checks v against params.min / params.max, filling the message on failure.
func bounds(v float64, c Check, what string, res *CheckResult) {
	if lo, ok := paramFloat(c, "min"); ok && v < lo {
		res.Message = fmt.Sprintf("%s %.4g is below min %g", what, v, lo)
		return
	}
	if hi, ok := paramFloat(c, "max"); ok && v > hi {
		res.Message = fmt.Sprintf("%s %.4g is above max %g", what, v, hi)
		return
	}
	res.Passed = true
	res.Message = fmt.Sprintf("%s %.4g", what, v)
}

func evalRate(events []Event, seconds float64, c Check, res *CheckResult) {
	per, unit := 60.0, "minute"
	if c.Params["per"] == "second" {
		per, unit = 1, "second"
	}
	rate := float64(len(events)) / seconds * per
	res.Evidence = &Evidence{FrameIndex: -1, Value: map[string]any{"count": len(events), "seconds": seconds, "rate": rate, "per": unit}}
	bounds(rate, c, fmt.Sprintf("%d %s events in %.4g s: %s per %s", len(events), eventPathKind(c.Path), seconds, "rate", unit), res)
}

// eventField reads actor, target, by_player, kind or data.<name> off an event.
func eventField(e Event, field string) (string, bool) {
	switch field {
	case "actor":
		return e.Actor, e.Actor != ""
	case "target":
		return e.Target, e.Target != ""
	case "kind":
		return e.Kind, true
	case "by_player":
		return fmt.Sprint(e.ByPlayer), true
	}
	if name, ok := strings.CutPrefix(field, "data."); ok && e.Data != nil {
		v, ok := LookupPath(e.Data, strings.Split(name, "."))
		if ok && v != nil {
			return valueKey(v), true
		}
	}
	return "", false
}

func evalHistogram(events []Event, c Check, res *CheckResult) {
	by := c.Params["by"].(string)
	counts, missing := map[string]int{}, 0
	for _, e := range events {
		if k, ok := eventField(e, by); ok {
			counts[k]++
		} else {
			missing++
		}
	}
	total := len(events) - missing
	top, topN := "", 0
	for k, n := range counts {
		if n > topN || (n == topN && k < top) {
			top, topN = k, n
		}
	}
	res.Evidence = &Evidence{FrameIndex: -1, Value: map[string]any{"by": by, "counts": counts, "total": total, "without_field": missing}}
	if missing > 0 && total == 0 {
		res.Message = fmt.Sprintf("none of the %d %s events has a %s", missing, eventPathKind(c.Path), by)
		return
	}
	if n, ok := paramFloat(c, "min_count"); ok && float64(total) < n {
		res.Message = fmt.Sprintf("%d %s events, fewer than min_count %g", total, eventPathKind(c.Path), n)
		return
	}
	if n, ok := paramFloat(c, "min_buckets"); ok && float64(len(counts)) < n {
		res.Message = fmt.Sprintf("%d distinct %s, fewer than min_buckets %g", len(counts), by, n)
		return
	}
	if s, ok := paramFloat(c, "max_share"); ok && total > 0 && float64(topN)/float64(total) > s {
		res.Message = fmt.Sprintf("%s %q has %d of %d (%.0f%%), above max_share %g", by, top, topN, total, 100*float64(topN)/float64(total), s)
		return
	}
	res.Passed = true
	res.Message = fmt.Sprintf("%d events over %d distinct %s", total, len(counts), by)
}

// evalTimeBetween measures, for each event of the path's kind, the time since the
// first event of params.from with the same params.by value (default target) after
// the previous match for that value — time-to-kill is events.kill from hit by target.
// Without params.from it measures the time between consecutive events of the kind.
func evalTimeBetween(fromEvents, toEvents []Event, c Check, res *CheckResult) {
	stat := "p50"
	if s, ok := c.Params["stat"].(string); ok {
		stat = s
	}
	var spans []float64
	unmatched := 0
	if len(fromEvents) == 0 && c.Params["from"] == nil {
		for i := 1; i < len(toEvents); i++ {
			spans = append(spans, toEvents[i].T-toEvents[i-1].T)
		}
	} else {
		by := "target"
		if b, ok := c.Params["by"].(string); ok {
			by = b
		}
		used := map[string]float64{} // per key: the t after which a from event may start a new span
		for _, to := range toEvents {
			key, ok := eventField(to, by)
			if !ok {
				unmatched++
				continue
			}
			after, seen := used[key]
			start, found := 0.0, false
			for _, f := range fromEvents {
				if fk, ok := eventField(f, by); ok && fk == key && f.T <= to.T && (!seen || f.T > after) {
					start, found = f.T, true
					break
				}
			}
			used[key] = to.T
			if !found {
				unmatched++
				continue
			}
			spans = append(spans, to.T-start)
		}
	}
	res.Evidence = &Evidence{FrameIndex: -1, Value: map[string]any{"spans": len(spans), "unmatched": unmatched}}
	if len(spans) == 0 {
		res.Message = fmt.Sprintf("no %s span to measure (%d %s events, %d unmatched)", c.Path, len(toEvents), eventPathKind(c.Path), unmatched)
		return
	}
	v := spanStat(spans, stat)
	res.Evidence.Value.(map[string]any)[stat+"_s"] = v
	bounds(v, c, fmt.Sprintf("%s of %d spans: %s s", stat, len(spans), "time"), res)
}

// spanStat is the nearest-rank percentile / mean / min / max of the spans.
func spanStat(xs []float64, stat string) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	switch stat {
	case "min":
		return s[0]
	case "max":
		return s[len(s)-1]
	case "mean":
		sum := 0.0
		for _, x := range s {
			sum += x
		}
		return sum / float64(len(s))
	case "p95":
		return s[int(math.Ceil(0.95*float64(len(s))))-1]
	}
	return s[int(math.Ceil(0.5*float64(len(s))))-1]
}

// evalPerfCSV runs a min / max check on one perf_csv.* value.
func evalPerfCSV(perf map[string]float64, c Check, res *CheckResult) {
	field, _ := strings.CutPrefix(c.Path, "perf_csv.")
	if perf == nil {
		insufficient(res, "the run had no CsvProfiler pass (playtest perf: true)")
		return
	}
	v, ok := perf[field]
	if !ok {
		res.Message = fmt.Sprintf("no perf_csv.%s (the pass reports %s)", field, strings.Join(PerfCSVFields, ", "))
		return
	}
	bound, has := paramFloat(c, "value")
	if !has {
		res.Message = c.Kind + " requires numeric params.value"
		return
	}
	res.Evidence = &Evidence{FrameIndex: -1, Value: v}
	if (c.Kind == "min" && v < bound) || (c.Kind == "max" && v > bound) {
		res.Message = fmt.Sprintf("perf_csv.%s = %.4g violates %s %g", field, v, c.Kind, bound)
		return
	}
	res.Passed = true
	res.Message = fmt.Sprintf("perf_csv.%s = %.4g", field, v)
}
