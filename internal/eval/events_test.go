package eval

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

func waveLog() *EventLog {
	return &EventLog{
		StartT: 0, EndT: 120,
		Sources:      map[string]string{SourceEngine: SourceRecorded, SourceJournal: SourceRecorded, SourceServer: SourceRecorded},
		JournalKinds: []string{"dash", "death", "hit", "kill", "wave_end"},
		Events: []Event{
			{T: 1, Kind: "hit", Actor: "Player", Target: "E1", ByPlayer: true, VisualT: f64(1.02)},
			{T: 2, Kind: "hit", Actor: "Player", Target: "E2", ByPlayer: true},
			{T: 3, Kind: "kill", Actor: "Player", Target: "E1", ByPlayer: true},
			{T: 3.5, Kind: "hit", Actor: "Player", Target: "E1", ByPlayer: true}, // a later hit on the same name
			{T: 6, Kind: "kill", Actor: "Player", Target: "E2", ByPlayer: true},
			{T: 30, Kind: "death", Target: "Player", Data: map[string]any{"cause": "melee"}},
			{T: 60, Kind: "death", Target: "Player", Data: map[string]any{"cause": "melee"}},
			{T: 90, Kind: "death", Target: "Player", Data: map[string]any{"cause": "boss"}},
			{T: 40, Kind: "wave_end", Data: map[string]any{"wave": 1.0, "clear_s": 40.0}},
			{T: 100, Kind: "wave_end", Data: map[string]any{"wave": 2.0, "clear_s": 50.0}},
			{T: 10, Kind: "damage", Actor: "E1", Target: "Core"},
		},
	}
}

func run1(t *testing.T, log *EventLog, c Check) CheckResult {
	t.Helper()
	rep := EvaluateInputs(Inputs{Events: log}, RubricSpec{Checks: []Check{c}})
	return rep.Checks[0]
}

func TestEventKinds(t *testing.T) {
	cases := []struct {
		c    Check
		pass bool
		msg  string
	}{
		{Check{Kind: "rate", Path: "events.wave_end", Params: map[string]any{"min": 0.5, "max": 2.0}}, true, "per minute"},
		{Check{Kind: "rate", Path: "events.wave_end", Params: map[string]any{"min": 3.0}}, false, "below min"},
		{Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"per": "second", "max": 0.01}}, false, "above max"},
		{Check{Kind: "histogram", Path: "events.death", Params: map[string]any{"by": "data.cause", "min_buckets": 2.0}}, true, "2 distinct"},
		{Check{Kind: "histogram", Path: "events.death", Params: map[string]any{"by": "data.cause", "max_share": 0.5}}, false, `"melee" has 2 of 3`},
		{Check{Kind: "histogram", Path: "events.death", Params: map[string]any{"by": "data.weapon", "min_count": 1.0}}, false, "none of the 3"},
		// time-to-kill: E1 2 s (hit 1 → kill 3), E2 4 s (hit 2 → kill 6); the hit at 3.5 is after E1's kill.
		{Check{Kind: "time_between", Path: "events.kill", Params: map[string]any{"from": "hit", "stat": "max", "max": 4.0}}, true, "max of 2 spans"},
		{Check{Kind: "time_between", Path: "events.kill", Params: map[string]any{"from": "hit", "stat": "min", "min": 2.5}}, false, "below min"},
		{Check{Kind: "time_between", Path: "events.wave_end", Params: map[string]any{"stat": "mean", "min": 59.0, "max": 61.0}}, true, "mean of 1 spans"},
		{Check{Kind: "time_between", Path: "events.kill", Params: map[string]any{"from": "dash", "max": 1.0}}, false, "no events.kill span"},
		{Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"by_player": false, "max": 0.0}}, true, "0 kill events"},
		{Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"from_t": 4.0, "to_t": 64.0, "min": 1.0, "max": 1.0}}, true, "1 kill events in 60 s"},
	}
	for i, tc := range cases {
		r := run1(t, waveLog(), tc.c)
		if r.Passed != tc.pass || r.Insufficient || !strings.Contains(r.Message, tc.msg) {
			t.Errorf("case %d %s %s: passed=%v insufficient=%v %q", i, tc.c.Kind, tc.c.Path, r.Passed, r.Insufficient, r.Message)
		}
	}
}

func TestEventChecksNeverScoreMissingEvidence(t *testing.T) {
	rate := Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"min": 0.0}}
	if r := run1(t, nil, rate); !r.Insufficient || !strings.Contains(r.Message, "record_events") {
		t.Fatalf("no recording: %+v", r)
	}
	log := waveLog()
	log.Gaps = []Gap{{Source: SourceJournal, FromT: 50, ToT: 55, Dropped: 12}}
	if r := run1(t, log, rate); !r.Insufficient || !strings.Contains(r.Message, "12 dropped") {
		t.Fatalf("gap: %+v", r)
	}
	// A window that ends before the gap is scored.
	early := Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"to_t": 40.0, "min": 0.0}}
	if r := run1(t, log, early); !r.Passed {
		t.Fatalf("window before the gap: %+v", r)
	}
	// An engine gap does not touch a journal kind.
	log.Gaps = []Gap{{Source: SourceEngine, FromT: 0, ToT: 120, Dropped: 3}}
	if r := run1(t, log, rate); !r.Passed {
		t.Fatalf("engine gap on a journal kind: %+v", r)
	}
	// time_between needs both kinds' sources: damage (engine) from kill (journal).
	tb := Check{Kind: "time_between", Path: "events.damage", Params: map[string]any{"from": "kill", "max": 9.0}}
	if r := run1(t, log, tb); !r.Insufficient {
		t.Fatalf("engine gap on time_between: %+v", r)
	}
	log.Gaps = nil
	log.Sources[SourceEngine] = SourceUnavailable
	log.SourceWhy = map[string]string{SourceEngine: "plugin API 7 < 8"}
	dmg := Check{Kind: "rate", Path: "events.damage", Params: map[string]any{"min": 0.0}}
	if r := run1(t, log, dmg); !r.Insufficient || !strings.Contains(r.Message, "plugin API 7 < 8") {
		t.Fatalf("engine unavailable: %+v", r)
	}
}

func TestInsufficientVerdict(t *testing.T) {
	log := waveLog()
	log.Gaps = []Gap{{Source: SourceJournal, FromT: 0, ToT: 1, Reason: "world_changed"}}
	insuff := Check{ID: "k", Kind: "rate", Path: "events.kill", Params: map[string]any{"min": 0.0}}
	failing := Check{ID: "d", Kind: "rate", Path: "events.damage", Params: map[string]any{"min": 100.0}}
	info := Check{ID: "i", Kind: "rate", Path: "events.kill", Params: map[string]any{"min": 0.0}, Severity: "info"}
	cases := []struct {
		checks []Check
		want   string
	}{
		{[]Check{insuff}, VerdictInsufficient},
		{[]Check{insuff, failing}, "FAIL"},
		{[]Check{info}, "PASS"},
		{[]Check{{ID: "w", Kind: "rate", Path: "events.damage", Params: map[string]any{"min": 100.0}, Severity: "warn"}, insuff}, VerdictInsufficient},
	}
	for i, tc := range cases {
		if got := EvaluateInputs(Inputs{Events: log}, RubricSpec{Checks: tc.checks}).Verdict; got != tc.want {
			t.Errorf("case %d: %s, want %s", i, got, tc.want)
		}
	}
}

func TestPerfCSVChecks(t *testing.T) {
	c := Check{Kind: "max", Path: "perf_csv.p95_frame_ms", Params: map[string]any{"value": 20.0}}
	rep := EvaluateInputs(Inputs{PerfCSV: map[string]float64{"p95_frame_ms": 18.5}}, RubricSpec{Checks: []Check{c}})
	if !rep.Checks[0].Passed || rep.Verdict != "PASS" {
		t.Fatalf("%+v", rep)
	}
	rep = EvaluateInputs(Inputs{PerfCSV: map[string]float64{"p95_frame_ms": 25}}, RubricSpec{Checks: []Check{c}})
	if rep.Checks[0].Passed || rep.Verdict != "FAIL" {
		t.Fatalf("%+v", rep)
	}
	rep = EvaluateInputs(Inputs{}, RubricSpec{Checks: []Check{c}})
	if !rep.Checks[0].Insufficient || rep.Verdict != VerdictInsufficient {
		t.Fatalf("%+v", rep)
	}
}

func TestLintEventAndPerfCSVChecks(t *testing.T) {
	bad := []RubricCheck{
		{Kind: "rate", Path: "gamestate.kills", Params: map[string]any{"min": 1.0}},
		{Kind: "rate", Path: "events.kill"},
		{Kind: "histogram", Path: "events.death", Params: map[string]any{"min_buckets": 2.0}},
		{Kind: "histogram", Path: "events.death", Params: map[string]any{"by": "data.cause", "max_share": 1.5}},
		{Kind: "time_between", Path: "events.kill", Params: map[string]any{"from": "hit", "stat": "p99", "max": 1.0}},
		{Kind: "rate", Path: "events.kill", Params: map[string]any{"min": 1.0, "per": "hour"}},
		{Kind: "rate", Path: "events.kill", Params: map[string]any{"mni": 1.0}},
		{Kind: "rate", Path: "events.kill", Params: map[string]any{"min": 2.0, "max": 1.0}},
		{Kind: "reached", Path: "events.kill", Params: map[string]any{"value": 1.0}},
		{Kind: "range", Path: "perf_csv.p95_frame_ms"},
		{Kind: "max", Path: "perf_csv.fps", Params: map[string]any{"value": 1.0}},
	}
	for i, c := range bad {
		if d := LintRubric([]RubricCheck{c}); !HasErrors(d) {
			t.Errorf("case %d (%s %s) passed the lint", i, c.Kind, c.Path)
		}
	}
	good := []RubricCheck{
		{Kind: "rate", Path: "events.wave_end", Params: map[string]any{"min": 0.5}},
		{Kind: "time_between", Path: "events.kill", Params: map[string]any{"from": "hit", "by": "target", "stat": "p95", "max": 3.0}},
		{Kind: "max", Path: "perf_csv.p95_frame_ms", Params: map[string]any{"value": 20.0}},
	}
	if d := LintRubric(good); HasErrors(d) {
		t.Fatalf("%+v", d)
	}
	_, diags, _ := ParseScenario([]byte(`{"schema":"scenario/v1","name":"x","rubric":[{"id":"r","kind":"rate","path":"events.kill","params":{"min":1}}]}`))
	if len(diags) != 1 || diags[0].Severity != "warning" || !strings.Contains(diags[0].Message, "record_events") {
		t.Fatalf("%+v", diags)
	}
}

func TestEventKindsTheGameDoesNotEmitAreNeverScored(t *testing.T) {
	// A typo (deaths) or a kind the game never declared: zero events is not evidence of zero.
	r := run1(t, waveLog(), Check{Kind: "rate", Path: "events.deaths", Params: map[string]any{"max": 2.0}})
	if !r.Insufficient || !strings.Contains(r.Message, `does not emit "deaths"`) {
		t.Fatalf("undeclared kind: %+v", r)
	}
	log := waveLog()
	log.JournalKinds = nil
	if r := run1(t, log, Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"min": 0.0}}); !r.Insufficient {
		t.Fatalf("no declared kinds: %+v", r)
	}
	// Engine kinds need no declaration.
	if r := run1(t, log, Check{Kind: "rate", Path: "events.damage", Params: map[string]any{"min": 0.0}}); !r.Passed {
		t.Fatalf("engine kind: %+v", r)
	}
}

func TestEventWindowsAreRelativeToTheRecordingStart(t *testing.T) {
	log := waveLog()
	for i := range log.Events {
		log.Events[i].T += 1000 // the run started at world second 1000
	}
	log.StartT, log.EndT = 1000, 1120
	r := run1(t, log, Check{Kind: "rate", Path: "events.kill", Params: map[string]any{"from_t": 4.0, "to_t": 64.0, "min": 1.0, "max": 1.0}})
	if !r.Passed {
		t.Fatalf("relative window: %+v", r)
	}
	// A histogram over no events fails (there is nothing to share), it never passes.
	r = run1(t, waveLog(), Check{Kind: "histogram", Path: "events.death", Params: map[string]any{"by": "data.cause", "max_share": 0.9, "from_t": 100.0}})
	if r.Passed || r.Insufficient {
		t.Fatalf("empty histogram: %+v", r)
	}
}
