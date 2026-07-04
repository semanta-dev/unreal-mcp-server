package affordances

import "testing"

func TestByToolAndConstraints(t *testing.T) {
	wq, ok := ByTool("world_query")
	if !ok || !wq.NeedsPIE || !wq.NeedsNavmesh {
		t.Fatalf("world_query affordance wrong: %+v ok=%v", wq, ok)
	}
	pm, ok := ByTool("project_map")
	if !ok || !pm.Offline || pm.NeedsEditor {
		t.Fatalf("project_map should be offline: %+v", pm)
	}
	if _, ok := ByTool("nope"); ok {
		t.Error("unknown tool should not be found")
	}
}

func TestOfflineSubset(t *testing.T) {
	off := Offline()
	if len(off) == 0 {
		t.Fatal("expected some offline tools")
	}
	for _, a := range off {
		if !a.Offline {
			t.Errorf("%s in Offline() but not offline", a.Tool)
		}
		if a.NeedsPIE {
			t.Errorf("%s offline yet needs PIE — contradiction", a.Tool)
		}
	}
}

func TestRegistrySorted(t *testing.T) {
	r := Registry()
	for i := 1; i < len(r); i++ {
		if r[i-1].Tool > r[i].Tool {
			t.Fatalf("registry not sorted at %d: %s > %s", i, r[i-1].Tool, r[i].Tool)
		}
	}
}
