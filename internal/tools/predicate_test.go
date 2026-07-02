package tools

import "testing"

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
		p, err := parsePredicate(c.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", c.expr, err)
		}
		if got, _ := p.eval(obs()); got != c.want {
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
		p, err := parsePredicate(c.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", c.expr, err)
		}
		if got, _ := p.eval(obs()); got != c.want {
			t.Errorf("%q = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestPredicateInvalid(t *testing.T) {
	if _, err := parsePredicate("no operator here"); err == nil {
		t.Fatal("expected parse error for missing operator")
	}
}

func TestPredicateNilState(t *testing.T) {
	p, _ := parsePredicate("gamestate.WaveNumber >= 1")
	if got, _ := p.eval(nil); got {
		t.Fatal("nil state should never satisfy a predicate")
	}
}

// TestPredicateEnumString covers the final-gate fix: UENUM fields come through
// pie_observe as their enumerator NAME (UPPER_SNAKE_CASE in Unreal), so string
// predicates match against that name.
func TestPredicateEnumString(t *testing.T) {
	state := map[string]any{"gamestate": map[string]any{"WaveState": "IN_PROGRESS"}}
	p, err := parsePredicate("gamestate.WaveState == 'IN_PROGRESS'")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.eval(state); !ok {
		t.Fatal("expected enum-name string predicate to match")
	}
}
