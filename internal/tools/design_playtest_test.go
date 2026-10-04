package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
)

func writeDoc(t *testing.T, doc map[string]any) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "playtest.json")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ev(t float64, kind string, byPlayer bool, extra map[string]any) map[string]any {
	e := map[string]any{"t": t, "kind": kind, "by_player": byPlayer, "actor": "Player_0"}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func recorded() map[string]any {
	return map[string]any{"engine": "recorded", "journal": "recorded", "server": "recorded"}
}

func feelDoc() map[string]any {
	return map[string]any{"event_window": []float64{0, 60}, "event_sources": recorded(), "event_gaps": []any{},
		"events": []any{
			ev(1, "hit", true, map[string]any{"visual_t": 1.01}), ev(1.02, "sfx", true, nil), ev(1.03, "camera_shake", true, nil), ev(1.0, "vfx", true, nil),
			ev(5, "hit", true, map[string]any{"visual_t": 5.05}), ev(5.04, "sfx", true, nil), ev(5.06, "camera_shake", true, nil),
			ev(9, "hit", true, nil), // no visual response: the audit fails it
		}}
}

func asPrecondition(err error) (*envelope.Error, bool) {
	var e *envelope.Error
	return e, errors.As(err, &e) && e.Code == envelope.Precondition && e.Details["reason"] == "insufficient_evidence"
}

func TestFeelAuditFromAPlaytest(t *testing.T) {
	p := writeDoc(t, feelDoc())
	out, err := feelFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	rep := m["audit"].(audit.FeelReport)
	if m["events"] != 3 || rep.Pass || len(rep.Reasons) != 1 || !rep.PerEvent[0].HasVisual || !rep.PerEvent[0].HasAudio ||
		!rep.PerEvent[0].HasCamera || rep.PerEvent[0].OverJuiced || rep.PerEvent[2].HasVisual {
		t.Fatalf("feel = %+v", m)
	}
	// A window that holds only the two answered hits passes.
	out, err = feelFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `","to_t":6}`))
	if err != nil || !out.(map[string]any)["audit"].(audit.FeelReport).Pass {
		t.Fatalf("to_t=6: %v %v", out, err)
	}
}

func TestFeelAuditFromAPlaytestRefusesMissingEvidence(t *testing.T) {
	noSfx := feelDoc()
	var keep []any
	for _, e := range noSfx["events"].([]any) {
		if e.(map[string]any)["kind"] != "sfx" {
			keep = append(keep, e)
		}
	}
	noSfx["events"] = keep
	gap := feelDoc()
	gap["event_gaps"] = []any{map[string]any{"source": "journal", "from_t": 4.0, "to_t": 6.0, "dropped": 3, "reason": "overran"}}
	off := feelDoc()
	delete(off, "event_sources")
	journalOff := feelDoc()
	journalOff["event_sources"] = map[string]any{"engine": "recorded", "journal": "unavailable", "server": "recorded"}
	for name, tc := range map[string]struct {
		doc   map[string]any
		extra string
		want  string
	}{
		"no sfx in the run": {noSfx, "", "events.sfx"},
		"a journal gap":     {gap, "", "lost events"},
		"no recording":      {off, "", "record_events"},
		"journal off":       {journalOff, "", "unavailable"},
		"no hits in window": {feelDoc(), `,"from_t":20`, "no hit events"},
	} {
		p := writeDoc(t, tc.doc)
		_, err := feelFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `"` + tc.extra + `}`))
		if e, ok := asPrecondition(err); !ok || !strings.Contains(e.Message, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A gap outside the window does not touch it.
	p := writeDoc(t, gap)
	if _, err := feelFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `","to_t":3}`)); err != nil {
		t.Fatalf("a window before the gap: %v", err)
	}
}

func TestDecisionAuditFromAPlaytest(t *testing.T) {
	doc := map[string]any{"event_window": []float64{0, 60}, "event_sources": recorded(), "event_gaps": []any{},
		"events": []any{
			ev(1.0, "weapon_fire", true, map[string]any{"data": map[string]any{"weapon": "Rifle"}}),
			ev(1.1, "weapon_fire", true, map[string]any{"data": map[string]any{"weapon": "Rifle"}}), // held fire: one choice
			ev(1.2, "weapon_fire", true, map[string]any{"data": map[string]any{"weapon": "Rifle"}}),
			ev(2.0, "dash", true, nil),
			ev(3.0, "weapon_fire", true, map[string]any{"data": map[string]any{"weapon": "Shotgun"}}),
			ev(3.0, "hit", true, nil),   // a consequence
			ev(4.0, "dash", false, nil), // not the player
			ev(7.0, "dash", true, nil),
			ev(8.0, "input", true, map[string]any{"source": "server"}), // the playtest's own beat
		}}
	p := writeDoc(t, doc)
	out, err := decisionFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["points"] != 4 || strings.Join(m["available"].([]string), ",") != "weapon_fire:Rifle,dash,weapon_fire:Shotgun" {
		t.Fatalf("decision = %+v", m)
	}
	out, err = decisionFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `","verbs":["dash"],"available":["dash","fire","build"]}`))
	if err != nil || out.(map[string]any)["points"] != 2 || out.(map[string]any)["audit"].(audit.DecisionReport).DecisionDensity != 3 {
		t.Fatalf("verbs=[dash]: %+v %v", out, err)
	}
	if _, err := decisionFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `","from_t":8}`)); err == nil {
		t.Fatal("no actions in the window should be insufficient evidence")
	} else if _, ok := asPrecondition(err); !ok {
		t.Fatalf("no actions: %v", err)
	}
	if _, err := decisionFromPlaytest([]byte(`{"source":"` + filepath.ToSlash(p) + `","nope":1}`)); err == nil {
		t.Fatal("an unknown input key passed")
	}
}
