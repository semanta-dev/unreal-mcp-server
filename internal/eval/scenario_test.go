package eval

import (
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	data := []byte(`{
      "schema": "scenario/v1",
      "name": "caster_spawns_and_attacks",
      "mode": "pie",
      "level": "/Game/Maps/L_Arena",
      "duration_s": 20,
      "setup": { "set_props": [{"target": "Hero", "properties": {"Health": 100}}] },
      "beats": [
        {"at_s": 2, "exec": {"target": "gamestate", "ufunction": "StartWave"}},
        {"wait_until": "counts.EnemyCharacter >= 1", "timeout_s": 10}
      ],
      "rubric": [
        {"id": "enemies", "kind": "nonzero_count", "path": "counts.EnemyCharacter", "severity": "fail"},
        {"id": "no_err", "kind": "log_zero", "path": "errors"}
      ]
    }`)
	s, diags, err := ParseScenario(data)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(diags) {
		t.Fatalf("unexpected errors: %+v", diags)
	}
	if s.Name != "caster_spawns_and_attacks" || len(s.Beats) != 2 || len(s.Rubric) != 2 {
		t.Fatalf("parsed wrong: %+v", s)
	}
	if s.Beats[0].Exec.UFunction != "StartWave" {
		t.Errorf("beat exec = %+v", s.Beats[0].Exec)
	}
}

func TestParseSemanticErrors(t *testing.T) {
	cases := map[string][]byte{
		"bad schema":      []byte(`{"schema":"nope","name":"x","rubric":[{"id":"a","kind":"reached"}]}`),
		"missing name":    []byte(`{"schema":"scenario/v1","rubric":[{"id":"a","kind":"reached"}]}`),
		"bad mode":        []byte(`{"schema":"scenario/v1","name":"x","mode":"turbo"}`),
		"dup check id":    []byte(`{"schema":"scenario/v1","name":"x","rubric":[{"id":"a","kind":"reached"},{"id":"a","kind":"changed"}]}`),
		"check no kind":   []byte(`{"schema":"scenario/v1","name":"x","rubric":[{"id":"a"}]}`),
		"exec incomplete": []byte(`{"schema":"scenario/v1","name":"x","beats":[{"exec":{"target":"gs"}}]}`),
	}
	for name, data := range cases {
		_, diags, err := ParseScenario(data)
		if err != nil {
			t.Fatalf("%s: json err %v", name, err)
		}
		if !HasErrors(diags) {
			t.Errorf("%s: expected an error diagnostic, got %+v", name, diags)
		}
	}
}

func TestParseStructuralError(t *testing.T) {
	if _, _, err := ParseScenario([]byte(`{not json`)); err == nil {
		t.Fatal("expected a JSON error")
	}
}

func TestNoOpBeatWarns(t *testing.T) {
	_, diags, _ := ParseScenario([]byte(`{"schema":"scenario/v1","name":"x","beats":[{"at_s":1}]}`))
	if HasErrors(diags) {
		t.Fatalf("a no-op beat should warn, not error: %+v", diags)
	}
	var warned bool
	for _, d := range diags {
		if d.Severity == "warning" {
			warned = true
		}
	}
	if !warned {
		t.Error("expected a no-op beat warning")
	}
}

// TestLintRubricRejectsRecorderPerf: perf.* is gone and recorder.* needs an explicit
// opt-in — the recorder's tick is slowed by its own captures, so it is not the game's
// frame rate (R0.7).
func TestLintRubricRejectsRecorderPerf(t *testing.T) {
	doc := `{"schema":"scenario/v1","name":"p","rubric":[
	 {"id":"a","kind":"min","path":"perf.fps","params":{"value":30}},
	 {"id":"b","kind":"max","path":"recorder.tick_ms","params":{"value":50}},
	 {"id":"c","kind":"max","path":"recorder.max_tick_ms","params":{"value":250},"allow_perturbed":true},
	 {"id":"d","kind":"max","path":"gamestate.performance","params":{"value":1}}]}`
	_, diags, err := ParseScenario([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range diags {
		if d.Severity == "error" {
			got[d.Field] = true
		}
	}
	if !got["rubric[0].path"] || !got["rubric[1].path"] || got["rubric[2].path"] || got["rubric[3].path"] || len(got) != 2 {
		t.Fatalf("errors on %v, want rubric[0] and rubric[1] only (diags %v)", got, diags)
	}
}

// R2 review: input steps are checked when the scenario is parsed, not mid-run.
func TestInputStepsAreChecked(t *testing.T) {
	for step, want := range map[string]string{
		`{"key":"MouseX","action":"axis"}`:               "needs value",
		`{"key":"W","value":1}`:                          "value goes with",
		`{"position":[1,2],"action":"click","to":[3,4]}`: "to goes with",
		`{"position":[1,2],"action":"drag"}`:             "needs to",
		`{"position":[1],"action":"click"}`:              "position is",
		`{"key":"W","action":"click"}`:                   "no action",
		`{"widget":"B","action":"tap"}`:                  "no action",
		`{"key":"W","widget":"B"}`:                       "exactly one",
	} {
		_, diags, err := ParseScenario([]byte(`{"schema":"scenario/v1","name":"x","beats":[{"input":` + step + `}]}`))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range diags {
			if d.Severity == "error" && strings.Contains(d.Message, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("input %s: want an error containing %q, got %+v", step, want, diags)
		}
	}
}
