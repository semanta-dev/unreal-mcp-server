package eval

import (
	"strings"
	"testing"
)

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

func TestParsePredicateIsStrict(t *testing.T) {
	for _, bad := range []string{"a >>> 1", "a >= ", "a == =2", ">= 1", "a b >= 1", "counts.Pawn"} {
		if _, err := ParsePredicate(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	for _, ok := range []string{"gamestate.wave_number >= 2", "counts.EnemyCharacter>=1", "pawn.speed > 100", "state == 'Running'"} {
		if _, err := ParsePredicate(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

// TestCompoundPredicates (R1.3): and / or / not / parentheses, with "and" binding
// tighter than "or"; an absent path is unmet, so "not" of it holds.
func TestCompoundPredicates(t *testing.T) {
	for _, c := range []struct {
		expr string
		want bool
	}{
		{"gamestate.WaveNumber >= 2 and counts.EnemyCharacter > 4", true},
		{"gamestate.WaveNumber >= 4 and counts.EnemyCharacter > 4", false},
		{"gamestate.WaveNumber >= 4 or counts.Serath == 1", true},
		{"not gamestate.WaveNumber >= 4", true},
		{"not counts.Missing >= 1", true},
		{"gamestate.WaveNumber == 9 or gamestate.WaveNumber == 3 and counts.Serath >= 1", true},
		{"(gamestate.WaveNumber == 9 or gamestate.WaveNumber == 3) and counts.Serath >= 5", false},
		{"gamestate.WaveState == 'InProgress' AND NOT counts.Serath >= 2", true},
		{"gamestate.WaveNumber>=3", true},
	} {
		p, err := ParsePredicate(c.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", c.expr, err)
		}
		if got, _ := p.Eval(obs()); got != c.want {
			t.Errorf("%q = %v, want %v", c.expr, got, c.want)
		}
	}
	for _, bad := range []string{"", "a >= 1 and", "(a >= 1", "a >= 1)", "a >= 1 b >= 2", "and a >= 1", "a >= and", "a >>> 1"} {
		if _, err := ParsePredicate(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// TestObjectPathPredicates: an object path is a flat key the caller fills; ObjectPaths
// lists them and HasStatePaths says whether pie_observe is needed at all.
func TestObjectPathPredicates(t *testing.T) {
	p, err := ParsePredicate("@subsystem:AesirAgentSubsystem.PeekSnapshotJson().wave_number >= 2 and not @gamestate.enemies_remaining > 0")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"@subsystem:AesirAgentSubsystem.PeekSnapshotJson().wave_number", "@gamestate.enemies_remaining"}
	if got := p.ObjectPaths(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ObjectPaths = %v", got)
	}
	if p.HasStatePaths() {
		t.Fatal("no state paths in this predicate")
	}
	st := map[string]any{want[0]: 3.0, want[1]: 0.0}
	if ok, _ := p.Eval(st); !ok {
		t.Fatal("object path predicate should hold")
	}
	st[want[1]] = nil // unreadable yet: the comparison is unmet, so "not" holds
	if ok, _ := p.Eval(st); !ok {
		t.Fatal("not of an absent object path holds")
	}
	if q, _ := ParsePredicate("counts.Enemy >= 1 or @gamestate.wave >= 1"); !q.HasStatePaths() {
		t.Fatal("HasStatePaths")
	}
	if _, err := ParsePredicate("@bad ref.x >= 1"); err == nil {
		t.Fatal("a malformed object path must not parse")
	}
}

// A live R1 wait spelled a bool the Python way ("== True") and never matched.
func TestPredicateBoolSpellings(t *testing.T) {
	state := map[string]any{"player": map[string]any{"alive": true}}
	for expr, want := range map[string]bool{
		"player.alive == True": true, "player.alive == true": true, "player.alive == TRUE": true,
		"player.alive != False": true, "player.alive == false": false, "player.alive == 'yes'": false,
	} {
		p, err := ParsePredicate(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		if got, _ := p.Eval(state); got != want {
			t.Errorf("%q = %v, want %v", expr, got, want)
		}
	}
}

// Review R1 #5: the game_api spelling of a subsystem (Module.Class or /Script/Module.Class).
func TestObjectPathClassForms(t *testing.T) {
	for _, expr := range []string{
		"@subsystem:/Script/Game.AgentSubsystem.wave >= 1",
		"@subsystem:Game.AgentSubsystem.PeekSnapshotJson().wave >= 1",
	} {
		p, err := ParsePredicate(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		if got := p.ObjectPaths(); len(got) != 1 || got[0] != strings.Fields(expr)[0] {
			t.Fatalf("%q: ObjectPaths = %v", expr, got)
		}
	}
}
