package rubric

import (
	"testing"
)

// gs builds a pie_observe-shaped state with a single gamestate field.
func gs(field string, val any) map[string]any {
	return map[string]any{"gamestate": map[string]any{field: val}}
}

// counts builds a pie_observe-shaped state with a single counts entry.
func counts(cls string, n any) map[string]any {
	return map[string]any{"counts": map[string]any{cls: n}}
}

// tl builds a timeline from a slice of states, assigning Index i and TWorld i.
func tl(states ...map[string]any) []Sample {
	out := make([]Sample, len(states))
	for i, st := range states {
		out[i] = Sample{Index: i, TWorld: float64(i), State: st}
	}
	return out
}

// one runs a single check over a timeline+logs and returns the sole result.
func one(t *testing.T, timeline []Sample, logs LogSummary, c Check) CheckResult {
	t.Helper()
	rep := Evaluate(timeline, logs, RubricSpec{Checks: []Check{c}})
	if len(rep.Checks) != 1 {
		t.Fatalf("expected 1 result, got %d", len(rep.Checks))
	}
	return rep.Checks[0]
}

func TestReached(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		value     any
		wantPass  bool
		wantFrame int // -2 = expect nil evidence
	}{
		{"string enum hit", tl(gs("WaveState", "Idle"), gs("WaveState", "Combat")), "Combat", true, 1},
		{"string enum miss", tl(gs("WaveState", "Idle"), gs("WaveState", "Prep")), "Combat", false, -2},
		{"numeric hit int vs float", tl(gs("WaveNumber", 1.0), gs("WaveNumber", 2.0)), 2, true, 1},
		{"numeric string-form hit", tl(gs("WaveNumber", 2.0)), "2", true, 0},
		{"first match wins", tl(gs("WaveState", "Combat"), gs("WaveState", "Combat")), "Combat", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Check{ID: "r", Kind: "reached", Path: "gamestate." + firstKey(tc.timeline), Params: map[string]any{"value": tc.value}}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
		})
	}
}

func TestEntered(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		from, to  any
		wantPass  bool
		wantFrame int
	}{
		{"clean transition", tl(gs("W", "Prep"), gs("W", "Combat")), "Prep", "Combat", true, 1},
		{"to before from does not count", tl(gs("W", "Combat"), gs("W", "Prep")), "Prep", "Combat", false, -2},
		{"from then unrelated then to", tl(gs("W", "Prep"), gs("W", "Idle"), gs("W", "Combat")), "Prep", "Combat", true, 2},
		{"never reaches to", tl(gs("W", "Prep"), gs("W", "Prep")), "Prep", "Combat", false, -2},
		{"numeric transition", tl(gs("W", 1.0), gs("W", 2.0)), 1, 2, true, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Check{Kind: "entered", Path: "gamestate.W", Params: map[string]any{"from": tc.from, "to": tc.to}}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
		})
	}
}

func TestIncreased(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		by        any // nil = omit
		wantPass  bool
		wantFrame int
		wantVal   float64
	}{
		{"rising any delta", tl(gs("S", 0.0), gs("S", 5.0), gs("S", 3.0)), nil, true, 1, 5},
		{"flat no delta", tl(gs("S", 4.0), gs("S", 4.0)), nil, false, 0, 4},
		{"meets by threshold", tl(gs("S", 10.0), gs("S", 40.0)), 30, true, 1, 40},
		{"below by threshold", tl(gs("S", 10.0), gs("S", 25.0)), 30, false, 1, 25},
		{"by zero passes flat", tl(gs("S", 7.0), gs("S", 7.0)), 0, true, 0, 7},
		{"max is first occurrence on ties", tl(gs("S", 1.0), gs("S", 9.0), gs("S", 9.0)), nil, true, 1, 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{}
			if tc.by != nil {
				params["by"] = tc.by
			}
			c := Check{Kind: "increased", Path: "gamestate.S", Params: params}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
			if got.Evidence != nil && got.Evidence.Value.(float64) != tc.wantVal {
				t.Fatalf("evidence value = %v, want %v", got.Evidence.Value, tc.wantVal)
			}
		})
	}
}

func TestDecreased(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		by        any
		wantPass  bool
		wantFrame int
		wantVal   float64
	}{
		{"falling any delta", tl(gs("H", 100.0), gs("H", 40.0), gs("H", 70.0)), nil, true, 1, 40},
		{"flat no delta", tl(gs("H", 50.0), gs("H", 50.0)), nil, false, 0, 50},
		{"meets by threshold", tl(gs("H", 100.0), gs("H", 20.0)), 50, true, 1, 20},
		{"below by threshold", tl(gs("H", 100.0), gs("H", 80.0)), 50, false, 1, 80},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{}
			if tc.by != nil {
				params["by"] = tc.by
			}
			c := Check{Kind: "decreased", Path: "gamestate.H", Params: params}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
			if got.Evidence != nil && got.Evidence.Value.(float64) != tc.wantVal {
				t.Fatalf("evidence value = %v, want %v", got.Evidence.Value, tc.wantVal)
			}
		})
	}
}

func TestChanged(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		wantPass  bool
		wantFrame int
	}{
		{"enum flips", tl(gs("W", "A"), gs("W", "A"), gs("W", "B")), true, 2},
		{"constant", tl(gs("W", "A"), gs("W", "A")), false, -2},
		{"single sample", tl(gs("W", "A")), false, -2},
		{"numeric change first at frame 1", tl(gs("W", 1.0), gs("W", 2.0), gs("W", 3.0)), true, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Check{Kind: "changed", Path: "gamestate.W"}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
		})
	}
}

func TestStayed(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		value     any
		wantPass  bool
		wantFrame int
	}{
		{"invariant holds", tl(gs("Mode", "Coop"), gs("Mode", "Coop")), "Coop", true, -2},
		{"violated at frame 1", tl(gs("Mode", "Coop"), gs("Mode", "Solo")), "Coop", false, 1},
		{"absent samples skipped", tl(gs("Mode", "Coop"), gs("Other", "x"), gs("Mode", "Coop")), "Coop", true, -2},
		{"never observed fails", tl(gs("Other", "x")), "Coop", false, -2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Check{Kind: "stayed", Path: "gamestate.Mode", Params: map[string]any{"value": tc.value}}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
		})
	}
}

func TestRange(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		min, max  float64
		wantPass  bool
		wantFrame int
	}{
		{"within bounds", tl(gs("FPS", 60.0), gs("FPS", 55.0)), 30, 120, true, -2},
		{"below min at frame 1", tl(gs("FPS", 60.0), gs("FPS", 10.0)), 30, 120, false, 1},
		{"above max at frame 0", tl(gs("FPS", 500.0)), 30, 120, false, 0},
		{"boundary inclusive", tl(gs("FPS", 30.0), gs("FPS", 120.0)), 30, 120, true, -2},
		{"never observed fails", tl(gs("Other", "x")), 0, 1, false, -2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Check{Kind: "range", Path: "gamestate.FPS", Params: map[string]any{"min": tc.min, "max": tc.max}}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
		})
	}
}

func TestNonzeroCount(t *testing.T) {
	tests := []struct {
		name      string
		timeline  []Sample
		wantPass  bool
		wantFrame int
	}{
		{"spawns at frame 2", tl(counts("EnemyCharacter", 0.0), counts("EnemyCharacter", 0.0), counts("EnemyCharacter", 3.0)), true, 2},
		{"always zero", tl(counts("EnemyCharacter", 0.0), counts("EnemyCharacter", 0.0)), false, -2},
		{"never present", tl(gs("x", 1.0)), false, -2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Check{Kind: "nonzero_count", Path: "counts.EnemyCharacter"}
			got := one(t, tc.timeline, LogSummary{}, c)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			checkFrame(t, got, tc.wantFrame)
		})
	}
}

func TestLogChecks(t *testing.T) {
	logs := LogSummary{Errors: 0, Warnings: 3, Ensures: 1}
	tests := []struct {
		name     string
		check    Check
		wantPass bool
	}{
		{"log_zero errors ok", Check{Kind: "log_zero", Path: "errors"}, true},
		{"log_zero warnings fail", Check{Kind: "log_zero", Path: "warnings"}, false},
		{"log_zero ensures fail", Check{Kind: "log_zero", Path: "ensures"}, false},
		{"log_max warnings under", Check{Kind: "log_max", Path: "warnings", Params: map[string]any{"value": 5.0}}, true},
		{"log_max warnings equal", Check{Kind: "log_max", Path: "warnings", Params: map[string]any{"value": 3.0}}, true},
		{"log_max warnings over", Check{Kind: "log_max", Path: "warnings", Params: map[string]any{"value": 2.0}}, false},
		{"log_zero bad path", Check{Kind: "log_zero", Path: "nope"}, false},
		{"log_max missing value", Check{Kind: "log_max", Path: "errors"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Log checks must evaluate even with an empty timeline.
			got := one(t, nil, logs, tc.check)
			if got.Passed != tc.wantPass {
				t.Fatalf("Passed = %v, want %v (msg %q)", got.Passed, tc.wantPass, got.Message)
			}
			// Valid log paths always attach evidence at frame -1.
			if _, ok := logField(logs, tc.check.Path); ok {
				if got.Evidence == nil || got.Evidence.FrameIndex != -1 {
					t.Fatalf("expected evidence at frame -1, got %+v", got.Evidence)
				}
			}
		})
	}
}

func TestUnknownKind(t *testing.T) {
	got := one(t, tl(gs("x", 1.0)), LogSummary{}, Check{Kind: "teleported", Path: "gamestate.x"})
	if got.Passed {
		t.Fatalf("unknown kind should not pass")
	}
	if got.Message != "unknown kind" {
		t.Fatalf("Message = %q, want %q", got.Message, "unknown kind")
	}
}

func TestEmptyTimeline(t *testing.T) {
	// Presence-based checks fail with a clear message on an empty timeline.
	for _, kind := range []string{"reached", "entered", "increased", "decreased", "changed", "stayed", "range", "nonzero_count"} {
		got := one(t, nil, LogSummary{}, Check{Kind: kind, Path: "gamestate.x", Params: map[string]any{"value": 1.0, "from": 1.0, "to": 2.0, "min": 0.0, "max": 1.0}})
		if got.Passed {
			t.Fatalf("%s should fail on empty timeline", kind)
		}
		if got.Message == "" {
			t.Fatalf("%s should have a clear message on empty timeline", kind)
		}
	}
	// log_* still evaluates against the LogSummary.
	got := one(t, nil, LogSummary{Errors: 0}, Check{Kind: "log_zero", Path: "errors"})
	if !got.Passed {
		t.Fatalf("log_zero should pass on empty timeline with zero errors")
	}
}

func TestVerdictPrecedence(t *testing.T) {
	pass := Check{ID: "p", Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Combat"}, Severity: "fail"}
	failWarn := Check{ID: "w", Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Never"}, Severity: "warn"}
	failFail := Check{ID: "f", Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Never"}, Severity: "fail"}
	failInfo := Check{ID: "i", Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Never"}, Severity: "info"}
	timeline := tl(gs("W", "Combat"))

	tests := []struct {
		name   string
		checks []Check
		want   string
	}{
		{"all pass", []Check{pass}, "PASS"},
		{"empty spec passes", nil, "PASS"},
		{"failing warn only", []Check{pass, failWarn}, "WARN"},
		{"failing fail dominates warn", []Check{failWarn, failFail}, "FAIL"},
		{"failing info is advisory", []Check{pass, failInfo}, "PASS"},
		{"default severity is fail", []Check{{Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Never"}}}, "FAIL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := Evaluate(timeline, LogSummary{}, RubricSpec{Checks: tc.checks})
			if rep.Verdict != tc.want {
				t.Fatalf("Verdict = %q, want %q", rep.Verdict, tc.want)
			}
		})
	}
}

func TestDefaultSeverityApplied(t *testing.T) {
	got := one(t, tl(gs("W", "Combat")), LogSummary{}, Check{Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Combat"}})
	if got.Severity != "fail" {
		t.Fatalf("default Severity = %q, want %q", got.Severity, "fail")
	}
}

func TestEvidenceCarriesTWorld(t *testing.T) {
	timeline := []Sample{
		{Index: 10, TWorld: 1.5, State: gs("W", "Idle")},
		{Index: 11, TWorld: 2.5, State: gs("W", "Combat")},
	}
	got := one(t, timeline, LogSummary{}, Check{Kind: "reached", Path: "gamestate.W", Params: map[string]any{"value": "Combat"}})
	if got.Evidence == nil {
		t.Fatalf("expected evidence")
	}
	if got.Evidence.FrameIndex != 11 || got.Evidence.TWorld != 2.5 {
		t.Fatalf("evidence = %+v, want FrameIndex 11 TWorld 2.5", *got.Evidence)
	}
	if got.Evidence.Value != "Combat" {
		t.Fatalf("evidence value = %v, want Combat", got.Evidence.Value)
	}
}

// --- test helpers ---

// checkFrame asserts the evidence frame index; wantFrame == -2 means expect nil.
func checkFrame(t *testing.T, got CheckResult, wantFrame int) {
	t.Helper()
	if wantFrame == -2 {
		if got.Evidence != nil {
			t.Fatalf("expected nil evidence, got frame %d", got.Evidence.FrameIndex)
		}
		return
	}
	if got.Evidence == nil {
		t.Fatalf("expected evidence at frame %d, got nil", wantFrame)
	}
	if got.Evidence.FrameIndex != wantFrame {
		t.Fatalf("evidence frame = %d, want %d", got.Evidence.FrameIndex, wantFrame)
	}
}

// firstKey returns the gamestate field name of the first sample (tests build
// single-field gamestate states).
func firstKey(timeline []Sample) string {
	gs := timeline[0].State["gamestate"].(map[string]any)
	for k := range gs {
		return k
	}
	return ""
}
