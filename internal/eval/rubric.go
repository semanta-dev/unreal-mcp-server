// rubric (merged into package eval) is the declarative pass/fail engine for goal A. It evaluates a
// set of temporal Checks over an ordered timeline of observed PIE state (each
// Sample is one pie_observe-shaped frame) plus a log summary, and returns a
// Report whose Verdict is FAIL/WARN/PASS. Every failing check points at a
// specific frame via Evidence.FrameIndex (which the montage package turns into a
// visual cell), so an agent gets an attributable verdict rather than a bare bool.
//
// The reducers are pure and total: they never panic on missing paths or empty
// timelines, they just fail with a clear Message. Paths are dotted lookups into
// Sample.State resolved with LookupPath (the same evaluator pie_wait
// uses), so "gamestate.WaveState" and "counts.EnemyCharacter" resolve the same
// way here as in a live predicate.
package eval

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

)

// Sample is one recorded frame of observed PIE state. State is pie_observe-shaped:
// {"gamestate":{...}, "counts":{...}, "actors":[...]}. Index is the frame number
// used for evidence (and to index the montage), TWorld is the in-game timestamp.
type Sample struct {
	Index  int
	TWorld float64
	State  map[string]any
}

// LogSummary is the error/warning/ensure tally for the run (from logs_since).
type LogSummary struct {
	Errors   int
	Warnings int
	Ensures  int
}

// Check is one declarative assertion. Kind selects the temporal reducer, Path is
// a dotted lookup into Sample.State (or one of errors|warnings|ensures for the
// log_* kinds), Params carries reducer arguments, and Severity is fail|warn|info
// (empty defaults to "fail").
type Check struct {
	ID       string
	Kind     string
	Path     string
	Params   map[string]any
	Severity string
}

// RubricSpec is the full set of checks to evaluate for a run.
type RubricSpec struct {
	Checks []Check
}

// Evidence points at the frame that justifies a check's outcome. FrameIndex is
// the Sample.Index (or -1 for log_* checks, which are not tied to a frame).
type Evidence struct {
	FrameIndex int
	TWorld     float64
	Value      any
}

// CheckResult is the outcome of one Check. Severity is the effective severity
// (with the "fail" default applied). Evidence is nil when there is no frame to
// point at (e.g. an unknown kind or a malformed check).
type CheckResult struct {
	ID       string
	Kind     string
	Severity string
	Passed   bool
	Evidence *Evidence
	Message  string
}

// Report is the aggregate result. Verdict is "FAIL" if any fail-severity check
// failed, else "WARN" if any warn-severity check failed, else "PASS".
type Report struct {
	Checks  []CheckResult
	Verdict string
}

// Evaluate runs every check in spec over the timeline and log summary and folds
// the per-check severities into a single Verdict.
func Evaluate(timeline []Sample, logs LogSummary, spec RubricSpec) Report {
	rep := Report{}
	for _, c := range spec.Checks {
		rep.Checks = append(rep.Checks, evalCheck(timeline, logs, c))
	}
	rep.Verdict = verdict(rep.Checks)
	return rep
}

// verdict applies the FAIL > WARN > PASS precedence. Only fail- and warn-severity
// checks move the verdict; info-severity (and any unrecognized severity) checks
// are advisory and never change it.
func verdict(results []CheckResult) string {
	anyFail, anyWarn := false, false
	for _, r := range results {
		if r.Passed {
			continue
		}
		switch r.Severity {
		case sevFail:
			anyFail = true
		case sevWarn:
			anyWarn = true
		}
	}
	switch {
	case anyFail:
		return "FAIL"
	case anyWarn:
		return "WARN"
	default:
		return "PASS"
	}
}

const (
	sevFail = "fail"
	sevWarn = "warn"
	sevInfo = "info"
)

// timelineKinds are the reducers that read the timeline; they short-circuit to a
// clear "empty timeline" failure when there are no samples to reduce over.
var timelineKinds = map[string]bool{
	"reached": true, "entered": true, "increased": true, "decreased": true,
	"changed": true, "stayed": true, "range": true, "nonzero_count": true,
	"min": true, "max": true,
}

// evalCheck dispatches one check to its reducer and returns a fully populated
// CheckResult. Log checks and unknown kinds are handled before the timeline is
// touched (they either ignore it or need a clear empty-timeline message).
func evalCheck(timeline []Sample, logs LogSummary, c Check) CheckResult {
	res := CheckResult{ID: c.ID, Kind: c.Kind, Severity: normSeverity(c.Severity)}

	switch c.Kind {
	case "log_zero":
		evalLogZero(logs, c, &res)
		return res
	case "log_max":
		evalLogMax(logs, c, &res)
		return res
	}

	if !timelineKinds[c.Kind] {
		res.Message = "unknown kind"
		return res
	}
	if len(timeline) == 0 {
		res.Message = fmt.Sprintf("empty timeline: cannot evaluate %q over %q", c.Kind, c.Path)
		return res
	}

	path := splitPath(c.Path)
	switch c.Kind {
	case "reached":
		evalReached(timeline, path, c, &res)
	case "entered":
		evalEntered(timeline, path, c, &res)
	case "increased":
		evalMonotone(timeline, path, c, &res, true)
	case "decreased":
		evalMonotone(timeline, path, c, &res, false)
	case "changed":
		evalChanged(timeline, path, c, &res)
	case "stayed":
		evalStayed(timeline, path, c, &res)
	case "range":
		evalRange(timeline, path, c, &res)
	case "nonzero_count":
		evalNonzeroCount(timeline, path, c, &res)
	case "min":
		evalBound(timeline, path, c, &res, true)
	case "max":
		evalBound(timeline, path, c, &res, false)
	}
	return res
}

// evalBound is the numeric-invariant reducer behind "min" and "max": every
// present numeric sample must stay >= (min) or <= (max) Params.value. It is the
// perf gate — "min perf.fps 30" fails the run the first frame framerate dips, and
// "max perf.hitch_ms 20" fails on the first hitch. Evidence is the first violator.
func evalBound(timeline []Sample, path []string, c Check, res *CheckResult, isMin bool) {
	bound, has := paramFloat(c, "value")
	if !has {
		res.Message = c.Kind + " requires numeric params.value"
		return
	}
	op := "<="
	if isMin {
		op = ">="
	}
	sawAny := false
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		f, ok := rubricFloat(v)
		if !ok {
			continue
		}
		sawAny = true
		if (isMin && f < bound) || (!isMin && f > bound) {
			res.Evidence = &Evidence{FrameIndex: s.Index, TWorld: s.TWorld, Value: f}
			res.Message = fmt.Sprintf("%s = %g violated %s %g at frame %d", c.Path, f, op, bound, s.Index)
			return
		}
	}
	if !sawAny {
		res.Message = fmt.Sprintf("%s never observed as a number", c.Path)
		return
	}
	res.Passed = true
	res.Message = fmt.Sprintf("%s stayed %s %g", c.Path, op, bound)
}

// --- timeline reducers ---

// evalReached passes if any sample's path equals Params.value. Evidence is the
// first matching sample.
func evalReached(timeline []Sample, path []string, c Check, res *CheckResult) {
	want, ok := c.Params["value"]
	if !ok {
		res.Message = "reached requires params.value"
		return
	}
	for _, s := range timeline {
		v, present := resolve(s, path)
		if present && valuesEqual(v, want) {
			res.Passed = true
			res.Evidence = evidenceOf(s, v)
			res.Message = fmt.Sprintf("%s reached %v at frame %d", c.Path, want, s.Index)
			return
		}
	}
	res.Message = fmt.Sprintf("%s never reached %v", c.Path, want)
}

// evalEntered passes if a sample equal to Params.from is followed by a later
// sample equal to Params.to. Evidence is the "to" sample (the transition target).
func evalEntered(timeline []Sample, path []string, c Check, res *CheckResult) {
	from, okf := c.Params["from"]
	to, okt := c.Params["to"]
	if !okf || !okt {
		res.Message = "entered requires params.from and params.to"
		return
	}
	seenFrom := false
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		if seenFrom && valuesEqual(v, to) {
			res.Passed = true
			res.Evidence = evidenceOf(s, v)
			res.Message = fmt.Sprintf("%s entered %v->%v at frame %d", c.Path, from, to, s.Index)
			return
		}
		if valuesEqual(v, from) {
			seenFrom = true
		}
	}
	res.Message = fmt.Sprintf("%s never transitioned %v->%v", c.Path, from, to)
}

// evalMonotone handles both "increased" and "decreased". For increased it passes
// when max-first >= by; for decreased when first-min >= by. When Params.by is
// absent, any strictly positive delta passes. Evidence is the extremum sample.
func evalMonotone(timeline []Sample, path []string, c Check, res *CheckResult, up bool) {
	by, hasBy := paramFloat(c, "by")

	var first, ext float64
	var extSample Sample
	haveFirst, haveExt := false, false
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		f, ok := rubricFloat(v)
		if !ok {
			continue
		}
		if !haveFirst {
			first = f
			haveFirst = true
		}
		if !haveExt || (up && f > ext) || (!up && f < ext) {
			ext = f
			extSample = s
			haveExt = true
		}
	}
	if !haveFirst {
		res.Message = fmt.Sprintf("%s never observed as a number", c.Path)
		return
	}

	var delta float64
	if up {
		delta = ext - first
	} else {
		delta = first - ext
	}
	if hasBy {
		res.Passed = delta >= by
	} else {
		res.Passed = delta > 0
	}
	res.Evidence = &Evidence{FrameIndex: extSample.Index, TWorld: extSample.TWorld, Value: ext}

	dir := "increase"
	if !up {
		dir = "decrease"
	}
	if res.Passed {
		res.Message = fmt.Sprintf("%s %sd by %g (frame %d)", c.Path, dir, delta, extSample.Index)
	} else {
		res.Message = fmt.Sprintf("%s did not %s enough: delta %g", c.Path, dir, delta)
	}
}

// evalChanged passes if the path takes >=2 distinct values over the timeline.
// Evidence is the first sample whose value differs from the previous present one.
func evalChanged(timeline []Sample, path []string, c Check, res *CheckResult) {
	distinct := map[string]bool{}
	var firstChange *Evidence
	var prevKey string
	havePrev := false
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		k := valueKey(v)
		distinct[k] = true
		if havePrev && k != prevKey && firstChange == nil {
			firstChange = evidenceOf(s, v)
		}
		prevKey = k
		havePrev = true
	}
	switch {
	case len(distinct) == 0:
		res.Message = fmt.Sprintf("%s never observed", c.Path)
	case len(distinct) >= 2:
		res.Passed = true
		res.Evidence = firstChange
		res.Message = fmt.Sprintf("%s took %d distinct values", c.Path, len(distinct))
	default:
		res.Message = fmt.Sprintf("%s never changed (constant)", c.Path)
	}
}

// evalStayed passes if the path equals Params.value in every sample where it is
// present (an invariant). It fails if the path is never observed. Evidence on a
// failure is the first violating sample.
func evalStayed(timeline []Sample, path []string, c Check, res *CheckResult) {
	want, ok := c.Params["value"]
	if !ok {
		res.Message = "stayed requires params.value"
		return
	}
	sawAny := false
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		sawAny = true
		if !valuesEqual(v, want) {
			res.Evidence = evidenceOf(s, v)
			res.Message = fmt.Sprintf("%s left %v (=%v) at frame %d", c.Path, want, v, s.Index)
			return
		}
	}
	if !sawAny {
		res.Message = fmt.Sprintf("%s never observed", c.Path)
		return
	}
	res.Passed = true
	res.Message = fmt.Sprintf("%s stayed %v", c.Path, want)
}

// evalRange passes if the numeric path stays within [Params.min, Params.max] in
// every present sample. Evidence on a failure is the first out-of-range sample.
func evalRange(timeline []Sample, path []string, c Check, res *CheckResult) {
	min, hasMin := paramFloat(c, "min")
	max, hasMax := paramFloat(c, "max")
	if !hasMin || !hasMax {
		res.Message = "range requires numeric params.min and params.max"
		return
	}
	sawAny := false
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		f, ok := rubricFloat(v)
		if !ok {
			continue
		}
		sawAny = true
		if f < min || f > max {
			res.Evidence = &Evidence{FrameIndex: s.Index, TWorld: s.TWorld, Value: f}
			res.Message = fmt.Sprintf("%s = %g out of range [%g,%g] at frame %d", c.Path, f, min, max, s.Index)
			return
		}
	}
	if !sawAny {
		res.Message = fmt.Sprintf("%s never observed as a number", c.Path)
		return
	}
	res.Passed = true
	res.Message = fmt.Sprintf("%s stayed within [%g,%g]", c.Path, min, max)
}

// evalNonzeroCount passes if the numeric path is > 0 in at least one sample.
// Evidence is the first such sample (e.g. counts.EnemyCharacter first spawning).
func evalNonzeroCount(timeline []Sample, path []string, c Check, res *CheckResult) {
	for _, s := range timeline {
		v, present := resolve(s, path)
		if !present {
			continue
		}
		f, ok := rubricFloat(v)
		if !ok {
			continue
		}
		if f > 0 {
			res.Passed = true
			res.Evidence = &Evidence{FrameIndex: s.Index, TWorld: s.TWorld, Value: f}
			res.Message = fmt.Sprintf("%s = %g > 0 at frame %d", c.Path, f, s.Index)
			return
		}
	}
	res.Message = fmt.Sprintf("%s never observed > 0", c.Path)
}

// --- log reducers ---

// evalLogZero passes if the named LogSummary field is 0. Evidence points at the
// synthetic frame -1 (log checks are not tied to a captured frame).
func evalLogZero(logs LogSummary, c Check, res *CheckResult) {
	field, ok := logField(logs, c.Path)
	if !ok {
		res.Message = "log_zero path must be errors|warnings|ensures"
		return
	}
	res.Evidence = &Evidence{FrameIndex: -1, Value: field}
	res.Passed = field == 0
	if res.Passed {
		res.Message = fmt.Sprintf("%s == 0", c.Path)
	} else {
		res.Message = fmt.Sprintf("%s = %d (want 0)", c.Path, field)
	}
}

// evalLogMax passes if the named LogSummary field is <= Params.value.
func evalLogMax(logs LogSummary, c Check, res *CheckResult) {
	field, ok := logField(logs, c.Path)
	if !ok {
		res.Message = "log_max path must be errors|warnings|ensures"
		return
	}
	res.Evidence = &Evidence{FrameIndex: -1, Value: field}
	max, hasMax := paramFloat(c, "value")
	if !hasMax {
		res.Message = "log_max requires numeric params.value"
		return
	}
	res.Passed = float64(field) <= max
	if res.Passed {
		res.Message = fmt.Sprintf("%s = %d <= %g", c.Path, field, max)
	} else {
		res.Message = fmt.Sprintf("%s = %d exceeds %g", c.Path, field, max)
	}
}

// logField maps a log path token to its LogSummary field.
func logField(l LogSummary, name string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "errors":
		return l.Errors, true
	case "warnings":
		return l.Warnings, true
	case "ensures":
		return l.Ensures, true
	}
	return 0, false
}

// --- helpers ---

// normSeverity lowercases the severity and applies the "fail" default for the
// empty string. Unrecognized values are passed through (they are advisory).
func normSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return sevFail
	}
	return s
}

// splitPath turns a dotted path string into the segment slice LookupPath wants.
func splitPath(p string) []string {
	return strings.Split(p, ".")
}

// resolve looks a dotted path up in a sample's state, reporting presence.
func resolve(s Sample, path []string) (any, bool) {
	if s.State == nil {
		return nil, false
	}
	return LookupPath(s.State, path)
}

// evidenceOf builds an Evidence pointing at a sample and carrying its value.
func evidenceOf(s Sample, v any) *Evidence {
	return &Evidence{FrameIndex: s.Index, TWorld: s.TWorld, Value: v}
}

// paramFloat coerces Params[key] to a float64, reporting whether it was present
// and numeric.
func paramFloat(c Check, key string) (float64, bool) {
	if c.Params == nil {
		return 0, false
	}
	v, ok := c.Params[key]
	if !ok {
		return 0, false
	}
	return rubricFloat(v)
}

// valuesEqual compares two observed/param values: numerically if both coerce to
// a number, otherwise by their fmt string form (so UENUM enumerator names match).
func valuesEqual(a, b any) bool {
	if fa, ok := rubricFloat(a); ok {
		if fb, ok := rubricFloat(b); ok {
			return fa == fb
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// valueKey is a canonical string for distinct-value counting; numbers share a
// key regardless of their originating Go type (2 and 2.0 are the same value).
func valueKey(v any) string {
	if f, ok := rubricFloat(v); ok {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// rubricFloat coerces the JSON-decoded numeric kinds (and the Go integer kinds a
// hand-built Params map may hold) to float64.
func rubricFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
