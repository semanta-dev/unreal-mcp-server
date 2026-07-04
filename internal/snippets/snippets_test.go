package snippets

import "testing"

func TestOpNamesExtractsAll(t *testing.T) {
	names := OpNames()
	if len(names) < 60 {
		t.Fatalf("OpNames extracted only %d ops; expected ~70 (regex likely broke)", len(names))
	}
	// spot-check a few known ops across the alphabet + that they're sorted/unique.
	want := map[string]bool{"spawn_actor": false, "delete_actor": false, "cockpit_info": false, "world_query": false, "console": false}
	seen := map[string]bool{}
	prev := ""
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate op name %q", n)
		}
		seen[n] = true
		if n < prev {
			t.Fatalf("OpNames not sorted: %q before %q", prev, n)
		}
		prev = n
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("expected op %q not found in _OPS extraction", n)
		}
	}
}
