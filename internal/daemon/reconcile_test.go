package daemon

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/editorpool"
)

func actionFor(t *testing.T, actions []Action, token string) Action {
	t.Helper()
	for _, a := range actions {
		if a.Token == token {
			return a
		}
	}
	return Action{}
}

func TestReconcileAdoptReadopdtable(t *testing.T) {
	records := []Record{{ID: "e1", Project: "/A", Token: "mine-idle", State: editorpool.Idle, PID: 10}}
	live := []LiveEditor{{Token: "mine-idle", PID: 10, Project: "/A"}}
	acts := Reconcile(records, live)
	if len(acts) != 1 || acts[0].Kind != Adopt || acts[0].PID != 10 {
		t.Fatalf("re-adoptable Idle editor should Adopt: %+v", acts)
	}
}

func TestReconcileKillMyExpectedDeadOrphan(t *testing.T) {
	// A Restarting record whose editor came up alive-but-wedged (fresh relaunch PID
	// in the re-pin window) -> daemon-owned orphan -> Kill.
	records := []Record{{ID: "e2", Project: "/A", Token: "mine-restart", State: editorpool.Restarting, PID: 20}}
	live := []LiveEditor{{Token: "mine-restart", PID: 20}}
	a := actionFor(t, Reconcile(records, live), "mine-restart")
	if a.Kind != Kill || a.PID != 20 {
		t.Fatalf("MY-token Restarting orphan should Kill: %+v", a)
	}
}

func TestReconcileNeverTouchesForeign(t *testing.T) {
	records := []Record{{ID: "e1", Project: "/A", Token: "mine", State: editorpool.Idle, PID: 10}}
	live := []LiveEditor{
		{Token: "mine", PID: 10, Project: "/A"},                 // mine -> adopt
		{Token: "another-daemon-token", PID: 99, Project: "/A"}, // foreign token -> never touch
		{Token: "", PID: 88, Project: "/A"},                     // human editor, no token -> never touch
	}
	acts := Reconcile(records, live)
	for _, a := range acts {
		if a.Token == "another-daemon-token" || a.Token == "" {
			t.Fatalf("foreign/human editor must never be actioned: %+v", a)
		}
	}
	// Only the one mine editor is adopted.
	if a := actionFor(t, acts, "mine"); a.Kind != Adopt {
		t.Fatalf("mine should adopt: %+v", a)
	}
}

func TestReconcileRemoveStaleRecordAndIntent(t *testing.T) {
	records := []Record{
		{ID: "e1", Project: "/A", Token: "gone-record", State: editorpool.Leased, PID: 10},  // editor absent
		{ID: "e2", Project: "/B", Token: "gone-intent", State: editorpool.Starting, PID: 0}, // pre-Launch intent, spawn died
	}
	live := []LiveEditor{} // nothing alive
	acts := Reconcile(records, live)
	if len(acts) != 2 {
		t.Fatalf("both absent entries should be RemoveStale, got %+v", acts)
	}
	for _, a := range acts {
		if a.Kind != RemoveStale {
			t.Fatalf("absent editor -> RemoveStale, got %+v", a)
		}
	}
}

func TestReconcileDedupTokenInBothSources(t *testing.T) {
	// A re-adoptable warm editor is in BOTH the process table AND discovery, so its
	// token appears twice in the union input — must yield exactly ONE Adopt.
	records := []Record{{ID: "e1", Project: "/A", Token: "dual", State: editorpool.Idle, PID: 10}}
	live := []LiveEditor{
		{Token: "dual", PID: 10, Project: "/A"}, // seen via process enumeration
		{Token: "dual", PID: 10, Project: "/A"}, // and via discovery
	}
	acts := Reconcile(records, live)
	adopts := 0
	for _, a := range acts {
		if a.Token == "dual" && a.Kind == Adopt {
			adopts++
		}
	}
	if adopts != 1 {
		t.Fatalf("a token in both sources must Adopt exactly once, got %d (all: %+v)", adopts, acts)
	}
}

func TestReconcileIdleAbsentRemoveStale(t *testing.T) {
	// An Idle record whose editor is absent everywhere -> RemoveStale (not just Leased).
	records := []Record{{ID: "e1", Project: "/A", Token: "gone-idle", State: editorpool.Idle, PID: 10}}
	acts := Reconcile(records, []LiveEditor{})
	if len(acts) != 1 || acts[0].Kind != RemoveStale {
		t.Fatalf("absent Idle record -> RemoveStale, got %+v", acts)
	}
}

func TestReconcileLaunchWindowOrphanKilled(t *testing.T) {
	// The race the write-ahead exists for: a Starting intent whose editor is still
	// cold-starting (NOT on discovery yet) but IS in the process table by its token.
	// It's MY token, not re-adoptable (Starting) -> Kill (no leak).
	records := []Record{{ID: "e3", Project: "/A", Token: "cold", State: editorpool.Starting, PID: 30}}
	live := []LiveEditor{{Token: "cold", PID: 30}} // seen via process enumeration only
	a := actionFor(t, Reconcile(records, live), "cold")
	if a.Kind != Kill || a.PID != 30 {
		t.Fatalf("cold-start Launch-window orphan must be killed, not leaked: %+v", a)
	}
}
