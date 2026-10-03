package eval

import "testing"

// Guards the LookupPath []any branch: a numeric path segment indexes a JSON array
// (pie_verify's flagship predicate nodes.0.SupplyRatio). Committed coverage for the
// PW gate blocker fix.
func TestArrayIndexPath(t *testing.T) {
	state := map[string]any{"nodes": []any{
		map[string]any{"SupplyRatio": 0.4},
		map[string]any{"SupplyRatio": 0.9},
	}}
	mustEval := func(expr string, want bool) {
		t.Helper()
		p, err := ParsePredicate(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		got, _ := p.Eval(state)
		if got != want {
			t.Errorf("Eval(%q) = %v, want %v", expr, got, want)
		}
	}
	mustEval("nodes.0.SupplyRatio < 0.5", true)  // index 0 -> 0.4 < 0.5
	mustEval("nodes.1.SupplyRatio < 0.5", false) // index 1 -> 0.9
	mustEval("nodes.5.SupplyRatio < 0.5", false) // out of range -> unmet
	// A non-array indexed numerically, or a non-numeric index into an array, is unmet.
	if v, ok := LookupPath(state, []string{"nodes", "x"}); ok {
		t.Errorf("non-numeric array index should miss, got %v", v)
	}
	if _, ok := LookupPath(map[string]any{"a": 1.0}, []string{"a", "0"}); ok {
		t.Error("indexing a scalar should miss")
	}
}

func obs() map[string]any {
	return map[string]any{
		"gamestate": map[string]any{"WaveNumber": float64(3), "WaveState": "InProgress"},
		"counts":    map[string]any{"EnemyCharacter": float64(5), "Serath": float64(1)},
	}
}

func TestPredicateNumeric(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"gamestate.WaveNumber >= 2", true},
		{"gamestate.WaveNumber >= 4", false},
		{"gamestate.WaveNumber == 3", true},
		{"gamestate.WaveNumber != 3", false},
		{"counts.EnemyCharacter > 4", true},
		{"counts.Serath >= 1", true},
		{"counts.Missing >= 1", false}, // absent path -> false
	}
	for _, c := range cases {
		p, err := ParsePredicate(c.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", c.expr, err)
		}
		if got, _ := p.Eval(obs()); got != c.want {
			t.Errorf("%q = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestPredicateString(t *testing.T) {
	for _, c := range []struct {
		expr string
		want bool
	}{
		{"gamestate.WaveState == 'InProgress'", true},
		{`gamestate.WaveState == "Intermission"`, false},
		{"gamestate.WaveState != 'Intermission'", true},
	} {
		p, err := ParsePredicate(c.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", c.expr, err)
		}
		if got, _ := p.Eval(obs()); got != c.want {
			t.Errorf("%q = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestPredicateInvalid(t *testing.T) {
	if _, err := ParsePredicate("no operator here"); err == nil {
		t.Fatal("expected parse error for missing operator")
	}
}

func TestPredicateNilState(t *testing.T) {
	p, _ := ParsePredicate("gamestate.WaveNumber >= 1")
	if got, _ := p.Eval(nil); got {
		t.Fatal("nil state should never satisfy a predicate")
	}
}

// TestPredicateEnumString covers the final-gate fix: UENUM fields come through
// pie_observe as their enumerator NAME (UPPER_SNAKE_CASE in Unreal), so string
// predicates match against that name.
func TestPredicateEnumString(t *testing.T) {
	state := map[string]any{"gamestate": map[string]any{"WaveState": "IN_PROGRESS"}}
	p, err := ParsePredicate("gamestate.WaveState == 'IN_PROGRESS'")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.Eval(state); !ok {
		t.Fatal("expected enum-name string predicate to match")
	}
}
