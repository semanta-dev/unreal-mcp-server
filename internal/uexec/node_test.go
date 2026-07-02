package uexec

import (
	"encoding/json"
	"testing"
	"time"
)

func pong(meta map[string]any) json.RawMessage {
	b, _ := json.Marshal(meta)
	return b
}

func TestNodeTableUpsertAndSweep(t *testing.T) {
	tab := newNodeTable()
	t0 := time.Now()
	tab.upsert("n1", pong(map[string]any{"project_root": "C:/x", "engine_version": "5.7"}), t0)
	tab.upsert("n2", pong(map[string]any{"project_root": "C:/y"}), t0)
	if got := tab.list(); len(got) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(got))
	}
	// n1 parsed metadata
	for _, n := range tab.list() {
		if n.ID == "n1" && (n.ProjectRoot != "C:/x" || n.EngineVersion != "5.7") {
			t.Fatalf("n1 metadata not parsed: %+v", n)
		}
	}
	// Age out n1 only.
	tab.upsert("n2", pong(map[string]any{"project_root": "C:/y"}), t0.Add(10*time.Second))
	tab.sweep(t0.Add(11*time.Second), 5*time.Second)
	got := tab.list()
	if len(got) != 1 || got[0].ID != "n2" {
		t.Fatalf("expected only n2 to survive sweep, got %+v", got)
	}
}

func TestPickNodeByProject(t *testing.T) {
	aesir := &Node{ID: "aesir", ProjectRoot: "C:/Users/jorda/code/games/aesir-wave-defense/", ProjectName: "AesirWaveDefense",
		Data: pong(map[string]any{"project_root": "C:/Users/jorda/code/games/aesir-wave-defense/"})}
	other := &Node{ID: "other", ProjectRoot: "C:/Users/jorda/code/games/some-other/", ProjectName: "SomeOther",
		Data: pong(map[string]any{"project_root": "C:/Users/jorda/code/games/some-other/"})}
	nodes := []*Node{other, aesir}

	// Path match despite dir base ("aesir-wave-defense") != project_name ("AesirWaveDefense").
	got, reason := pickNode(nodes, `C:\Users\jorda\code\games\aesir-wave-defense`)
	if got == nil || got.ID != "aesir" {
		t.Fatalf("expected aesir by project_root path match, got %+v (%s)", got, reason)
	}

	// No filter -> first (sorted by id -> "aesir").
	got, _ = pickNode(nodes, "")
	if got == nil {
		t.Fatal("expected a node without filter")
	}

	// Filter set but no match -> nil (caller falls back).
	got, reason = pickNode(nodes, "C:/nowhere/nothing")
	if got != nil {
		t.Fatalf("expected no match, got %+v (%s)", got, reason)
	}
}

func TestNodeMatchesProjectRawFallback(t *testing.T) {
	// project_root absent; only raw pong carries the dir name.
	n := &Node{ID: "n", Data: pong(map[string]any{"cwd": "C:/Users/jorda/code/games/aesir-wave-defense/x"})}
	ok, reason := nodeMatchesProject(n, "C:/Users/jorda/code/games/aesir-wave-defense")
	if !ok {
		t.Fatalf("expected raw-contains fallback match, reason=%q", reason)
	}
}
