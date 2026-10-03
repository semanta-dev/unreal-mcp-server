package eval

import "testing"

func perfTimeline(fps ...float64) []Sample {
	tl := make([]Sample, len(fps))
	for i, f := range fps {
		tl[i] = Sample{Index: i, TWorld: float64(i), State: map[string]any{"perf": map[string]any{"fps": f}}}
	}
	return tl
}

func TestMinFPS(t *testing.T) {
	// Framerate floor: passes when every frame >= 30.
	pass := Evaluate(perfTimeline(60, 45, 31), LogSummary{}, RubricSpec{Checks: []Check{
		{ID: "fps", Kind: "min", Path: "perf.fps", Params: map[string]any{"value": 30.0}},
	}})
	if pass.Verdict != "PASS" {
		t.Fatalf("expected PASS, got %s (%s)", pass.Verdict, pass.Checks[0].Message)
	}
	// Dips to 12 at frame 1 -> FAIL with evidence there.
	fail := Evaluate(perfTimeline(60, 12, 40), LogSummary{}, RubricSpec{Checks: []Check{
		{ID: "fps", Kind: "min", Path: "perf.fps", Params: map[string]any{"value": 30.0}},
	}})
	if fail.Verdict != "FAIL" {
		t.Fatalf("expected FAIL, got %s", fail.Verdict)
	}
	if fail.Checks[0].Evidence == nil || fail.Checks[0].Evidence.FrameIndex != 1 {
		t.Fatalf("evidence should point at frame 1, got %+v", fail.Checks[0].Evidence)
	}
}

func TestMaxHitch(t *testing.T) {
	tl := []Sample{
		{Index: 0, State: map[string]any{"perf": map[string]any{"hitch_ms": 8.0}}},
		{Index: 1, State: map[string]any{"perf": map[string]any{"hitch_ms": 180.0}}},
	}
	r := Evaluate(tl, LogSummary{}, RubricSpec{Checks: []Check{
		{ID: "hitch", Kind: "max", Path: "perf.hitch_ms", Params: map[string]any{"value": 20.0}},
	}})
	if r.Verdict != "FAIL" || r.Checks[0].Evidence.FrameIndex != 1 {
		t.Fatalf("expected FAIL at frame 1, got %s %+v", r.Verdict, r.Checks[0].Evidence)
	}
}

func TestBoundMissingParam(t *testing.T) {
	r := Evaluate(perfTimeline(60), LogSummary{}, RubricSpec{Checks: []Check{
		{ID: "x", Kind: "min", Path: "perf.fps"},
	}})
	if r.Checks[0].Passed {
		t.Fatal("min without params.value should not pass")
	}
}
