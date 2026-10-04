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
	edit := toolCall{Tool: "data_edit", Args: map[string]any{"op": "table_upsert"}}
	played := toolCall{Tool: "playtest", Args: map[string]any{"op": "run"}, Job: "job-3", JobState: "succeeded"}
	calls := []toolCall{edit, played}
	if f := judgeGame(task, probes, nil, calls, "Wave 2 now spawns 6 enemies.\nANSWER: 6", 0); len(f) != 0 {
		t.Fatalf("a passing run failed: %v", f)
	}
	// The playtest ran BEFORE the edit: it proves nothing.
	if f := judgeGame(task, probes, nil, []toolCall{played, edit}, "ANSWER: 6", 0); len(f) != 1 || !strings.Contains(f[0], "proved") {
		t.Fatalf("playtest before the edit = %v", f)
	}
	// A failed call, or an async call whose job did not succeed (or was never followed),
	// does not count.
	for _, bad := range []toolCall{
		{Tool: "playtest", Args: map[string]any{"op": "run"}, Error: "PRECONDITION"},
		{Tool: "playtest", Args: map[string]any{"op": "run"}, Job: "job-3", JobState: "running"},
		{Tool: "playtest", Args: map[string]any{"op": "run"}, Job: "job-3", JobState: "failed"},
	} {
		if f := judgeGame(task, probes, nil, []toolCall{edit, bad}, "ANSWER: 6", 0); len(f) != 1 {
			t.Fatalf("%+v counted as proof: %v", bad, f)
		}
	}
	if f := judgeGame(task, probes, nil, calls, "ANSWER: 7", 0); len(f) != 1 || !strings.Contains(f[0], "ans") {
		t.Fatalf("a wrong answer passed: %v", f)
	}
	if f := judgeGame(task, probes, nil, calls, "ANSWER: 6", 2); len(f) != 1 || !strings.Contains(f[0], "no_python") {
		t.Fatalf("python calls on a no_python task = %v", f)
	}
	if f := judgeGame(task, map[string]map[string]any{}, map[string]string{"row": "boom"}, calls, "ANSWER: 6", 0); len(f) < 2 {
		t.Fatalf("a failed probe must fail its check and the answer that reads it: %v", f)
	}
}

// TestAnswerCannotBeGamed: only the ANSWER line counts, it must hold exactly one number
// (or one per key), and separators/hyphens parse as written.
func TestAnswerCannotBeGamed(t *testing.T) {
	probes := map[string]map[string]any{"p": {"v": 4.0, "big": 12345.0, "min": 3.5, "max": 7.25}}
	one := answerCheck{From: "p.v"}
	for reply, want := range map[string]bool{
		"ANSWER: 4":                        true,
		"It is wave 4.\n**ANSWER: 4**":     true,
		"ANSWER: wave 1, 2, 3 or 4":        false, // listing candidates
		"The wave is 4.":                   false, // no ANSWER line
		"ANSWER: 4\nANSWER: 5":             false, // the last ANSWER line wins
		"ANSWER: wave-4":                   true,  // "wave-4" is 4, not -4
		"ANSWER: -4":                       false,
		"I think 4 but ANSWER: none found": false,
	} {
		if got := answerHolds(one, probes, reply); got != want {
			t.Errorf("%q = %v, want %v", reply, got, want)
		}
	}
	if !answerHolds(answerCheck{From: "p.big"}, probes, "ANSWER: 12,345") {
		t.Error("thousands separators")
	}
	keyed := "ANSWER: min=3.5 s, max=7.25 s"
	if !answerHolds(answerCheck{From: "p.min", Key: "min", Tolerance: 0.01}, probes, keyed) ||
		!answerHolds(answerCheck{From: "p.max", Key: "max", Tolerance: 0.01}, probes, keyed) {
		t.Error("keyed answers")
	}
	if answerHolds(answerCheck{From: "p.max", Key: "max"}, probes, "ANSWER: min=3.5") {
		t.Error("a missing key passed")
	}
	verdict := answerCheck{Regex: "(?i)^FAIL$"}
	if !answerHolds(verdict, nil, "It failed.\nANSWER: FAIL") || answerHolds(verdict, nil, "It didn't fail.\nANSWER: PASS") ||
		answerHolds(verdict, nil, "ANSWER: not FAIL") {
		t.Error("verdict regex")
	}
}

func TestExpectations(t *testing.T) {
	m := map[string]any{"a": 1.25, "s": "Shotgun_C", "l": []any{"E", "Q"}, "n": map[string]any{"x": "3"}, "text": "WAVE 02 // 3"}
	for _, c := range []struct {
		e    expectation
		want bool
	}{
		{expectation{Path: "a", Op: "eq", Value: 1.25}, true}, {expectation{Path: "a", Op: "ne", Value: 1.25}, false},
		{expectation{Path: "n.x", Op: "gte", Value: 3.0}, true}, {expectation{Path: "s", Op: "contains", Value: "Shotgun"}, true},
		{expectation{Path: "l", Op: "contains", Value: "E"}, true}, {expectation{Path: "l", Op: "contains", Value: "R"}, false},
		{expectation{Path: "missing", Op: "exists"}, false}, {expectation{Path: "l.1", Op: "eq", Value: "Q"}, true},
		{expectation{Path: "s", Op: "gt", Value: 1.0}, false}, {expectation{Path: "text", Op: "contains", Value: 3.0}, true},
		{expectation{Path: "a", Op: "eq"}, false}, // an unresolved value_from never matches
	} {
		if got := expectHolds(m, c.e); got != c.want {
			t.Errorf("%+v = %v, want %v", c.e, got, c.want)
		}
	}
	// value_from reads another probe.
	task := &gameTask{ID: "t", Goal: "G5", Checks: []liveCheck{
		{Name: "state", Probe: "x"},
		{Name: "hud", Call: &liveCall{Tool: "widget_query"}, Expect: []expectation{{Path: "text", Op: "contains", ValueFrom: "state.wave"}}},
	}}
	probes := map[string]map[string]any{"state": {"wave": 3.0}, "hud": {"text": "WAVE 3"}}
	if f := judgeGame(task, probes, nil, nil, "", 0); len(f) != 0 {
		t.Fatalf("value_from = %v", f)
	}
	probes["state"]["wave"] = 4.0
	if f := judgeGame(task, probes, nil, nil, "", 0); len(f) != 1 {
		t.Fatalf("value_from mismatch passed: %v", f)
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

// TestPassBarNeedsEveryGoal: a goal absent from the results fails the bar.
func TestPassBarNeedsEveryGoal(t *testing.T) {
	var vs []taskVerdict
	for _, g := range []string{"G2", "G3", "G5", "G6"} {
		for i := 0; i < 6; i++ {
			vs = append(vs, taskVerdict{Goal: g, Pass: true})
		}
	}
	if passBarMet(vs, 0, 20, 2) {
		t.Fatal("no G4 tasks, yet the bar was met")
	}
	vs = append(vs, taskVerdict{Goal: "G4", Pass: true}, taskVerdict{Goal: "G4", Pass: true})
	if !passBarMet(vs, 0, 20, 2) || passBarMet(vs, 0.2, 20, 2) {
		t.Fatal("bar with every goal / python rate")
	}
}

func TestLintGameTasks(t *testing.T) {
	ts := []*gameTask{
		{ID: "x", Goal: "G9", Project: "nope", Prompt: "p", Checks: []liveCheck{
			{Name: "a", Answer: &answerCheck{From: "later.v"}}, {Name: "later", Probe: "p"}, {Name: "both", Probe: "p", Called: &calledCheck{Tool: "t"}}}},
		{ID: "ungrounded", Goal: "G6", Project: "aesir", Prompt: "p", Checks: []liveCheck{
			{Name: "c", Called: &calledCheck{Tool: "playtest"}}, {Name: "r", Answer: &answerCheck{Regex: `\d+ ms`}}}},
	}
	errs := strings.Join(lintGameTasks(ts, []string{"aesir"}, 1, 1), "\n")
	for _, want := range []string{"goal must be", "unknown project", "earlier probe", "exactly one of", "goal G2 has 0",
		"ungrounded: not grounded", "regex answer is allowed only for G1"} {
		if !strings.Contains(errs, want) {
			t.Errorf("lint misses %q:\n%s", want, errs)
		}
	}
	gts, err := loadGameTasks("../../docs/validation/gameeval/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	if errs := lintGameTasks(gts, []string{"aesir", "polyworld"}, 3, 8); len(errs) > 0 {
		t.Fatalf("the committed task file: %v", errs)
	}
	if !strings.HasPrefix(gts[0].Checks[0].Probe, "import json") {
		t.Fatal("the prelude was not prepended to the probes")
	}
	if _, err := selectGameTasks(gts, "aesir_ttk,nope"); err == nil {
		t.Fatal("-only with an unknown id must fail")
	}
}

func TestRunCostFailsClosed(t *testing.T) {
	o := liveOpts{model: "a", prices: map[string]price{"a": {1, 1}, "b": {10, 10}}}
	u := usage{InputTokens: 1_000_000}
	if c, ok := runCost(u, []string{"a"}, o); c != 1 || !ok {
		t.Fatalf("cost = %v %v", c, ok)
	}
	if c, ok := runCost(u, []string{"a", "b"}, o); c != 10 || !ok {
		t.Fatalf("cost with a pricier served model = %v %v", c, ok)
	}
	if _, ok := runCost(u, []string{"a", "unknown-model"}, o); ok {
		t.Fatal("a served model without a price must be reported unpriced")
	}
}
