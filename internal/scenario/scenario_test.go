package scenario

import "testing"

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
	s, diags, err := Parse(data)
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
		_, diags, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: json err %v", name, err)
		}
		if !HasErrors(diags) {
			t.Errorf("%s: expected an error diagnostic, got %+v", name, diags)
		}
	}
}

func TestParseStructuralError(t *testing.T) {
	if _, _, err := Parse([]byte(`{not json`)); err == nil {
		t.Fatal("expected a JSON error")
	}
}

func TestNoOpBeatWarns(t *testing.T) {
	_, diags, _ := Parse([]byte(`{"schema":"scenario/v1","name":"x","beats":[{"at_s":1}]}`))
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
