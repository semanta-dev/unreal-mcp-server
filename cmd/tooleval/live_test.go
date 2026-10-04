package main

import (
	"strings"
	"testing"
)

func TestJudgeGame(t *testing.T) {
	task := &gameTask{ID: "t", Goal: "G4", NoPython: true, Checks: []liveCheck{
		{Name: "row", Probe: "x", Expect: []expectation{{Path: "count", Op: "gte", Value: 6.0}, {Path: "rows.0", Op: "exists"}}},
		{Name: "proved", Called: &calledCheck{Tool: "playtest", Op: "run", After: "data_edit"}},
		{Name: "ans", Answer: &answerCheck{From: "row.count", Tolerance: 0}},
	}}
	probes := map[string]map[string]any{"row": {"count": 6.0, "rows": []any{"a"}}}
	calls := []toolCall{{Tool: "data_edit", Args: map[string]any{"op": "table_upsert"}}, {Tool: "playtest", Args: map[string]any{"op": "run"}}}
	if f := judgeGame(task, probes, nil, calls, "Wave 2 now spawns 6 enemies.", 0); len(f) != 0 {
		t.Fatalf("a passing run failed: %v", f)
	}
	// The playtest ran BEFORE the edit: it proves nothing.
	early := []toolCall{calls[1], calls[0]}
	if f := judgeGame(task, probes, nil, early, "6", 0); len(f) != 1 || !strings.Contains(f[0], "proved") {
		t.Fatalf("playtest before the edit = %v", f)
	}
	// A failed call does not count.
	errd := []toolCall{calls[0], {Tool: "playtest", Args: map[string]any{"op": "run"}, Error: "PRECONDITION"}}
	if f := judgeGame(task, probes, nil, errd, "6", 0); len(f) != 1 {
		t.Fatalf("a failed playtest counted: %v", f)
	}
	if f := judgeGame(task, probes, nil, calls, "it spawns 7", 0); len(f) != 1 || !strings.Contains(f[0], "ans") {
		t.Fatalf("a wrong answer passed: %v", f)
	}
	if f := judgeGame(task, probes, nil, calls, "6", 2); len(f) != 1 || !strings.Contains(f[0], "no_python") {
		t.Fatalf("python calls on a no_python task = %v", f)
	}
	if f := judgeGame(task, map[string]map[string]any{}, map[string]string{"row": "boom"}, calls, "6", 0); len(f) < 2 {
		t.Fatalf("a failed probe must fail its check and the answer that reads it: %v", f)
	}
}

func TestExpectations(t *testing.T) {
	m := map[string]any{"a": 1.25, "s": "Shotgun_C", "l": []any{"E", "Q"}, "n": map[string]any{"x": "3"}}
	for _, c := range []struct {
		e    expectation
		want bool
	}{
		{expectation{"a", "eq", 1.25}, true}, {expectation{"a", "ne", 1.25}, false}, {expectation{"n.x", "gte", 3.0}, true},
		{expectation{"s", "contains", "Shotgun"}, true}, {expectation{"l", "contains", "E"}, true}, {expectation{"l", "contains", "R"}, false},
		{expectation{"missing", "exists", nil}, false}, {expectation{"l.1", "eq", "Q"}, true}, {expectation{"s", "gt", 1.0}, false},
	} {
		if got := expectHolds(m, c.e); got != c.want {
			t.Errorf("%+v = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestParseProbeAndVerdicts(t *testing.T) {
	m, err := parseProbe("log line\r\nRESULT {\"x\": 1}\r\n")
	if err != nil || m["x"] != 1.0 {
		t.Fatalf("parseProbe = %v %v", m, err)
	}
	if _, err := parseProbe("no result"); err == nil {
		t.Fatal("a probe without RESULT must fail")
	}
	vs := verdicts([]liveResult{{Task: "a", Pass: true}, {Task: "a", Pass: true}, {Task: "a"}, {Task: "b", Pass: true}, {Task: "b"}, {Task: "b"}})
	if !vs[0].Pass || vs[1].Pass {
		t.Fatalf("verdicts = %+v (2/3 passes, 1/3 fails)", vs)
	}
}

func TestLintGameTasks(t *testing.T) {
	ts := []*gameTask{{ID: "x", Goal: "G9", Project: "nope", Prompt: "p", Checks: []liveCheck{
		{Name: "a", Answer: &answerCheck{From: "later.v"}}, {Name: "later", Probe: "p"}, {Name: "both", Probe: "p", Called: &calledCheck{Tool: "t"}}}}}
	errs := strings.Join(lintGameTasks(ts, []string{"aesir"}, 1, 1), "\n")
	for _, want := range []string{"goal must be", "unknown project", "earlier probe", "exactly one of", "goal G2 has 0"} {
		if !strings.Contains(errs, want) {
			t.Errorf("lint misses %q:\n%s", want, errs)
		}
	}
	if gts, err := loadGameTasks("../../docs/validation/gameeval/tasks.json"); err != nil {
		t.Fatal(err)
	} else if errs := lintGameTasks(gts, []string{"aesir", "polyworld"}, 3, 8); len(errs) > 0 {
		t.Fatalf("the committed task file: %v", errs)
	} else if !strings.HasPrefix(gts[0].Checks[0].Probe, "import json") {
		t.Fatal("the prelude was not prepended to the probes")
	}
}

func TestRunCostUsesTheMostExpensiveServedModel(t *testing.T) {
	o := liveOpts{model: "a", prices: map[string]price{"a": {1, 1}, "b": {10, 10}}}
	u := usage{InputTokens: 1_000_000}
	if c := runCost(u, []string{"a"}, o); c != 1 {
		t.Fatalf("cost = %v", c)
	}
	if c := runCost(u, []string{"a", "b"}, o); c != 10 {
		t.Fatalf("cost with a pricier served model = %v", c)
	}
}
