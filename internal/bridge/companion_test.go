package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestOpNamesExtractsAll(t *testing.T) {
	names := CompanionOps()
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

// splitSourceSHA256 is the SHA-256 of the monolithic mcp_bridge.py (v37) at the
// moment it was split into section files (P1). The pure-move split must reproduce
// it byte-for-byte; delete this test when the module is first edited (P5a).
const splitSourceSHA256 = "40fc6ba8df3f1186d36a5aceca55f52fa32a209a4af651dd2589d2044b659190"

func TestSplitIsByteIdentical(t *testing.T) {
	sum := sha256.Sum256([]byte(CompanionSource()))
	if got := hex.EncodeToString(sum[:]); got != splitSourceSHA256 {
		t.Fatalf("concatenated section files differ from the pre-split module: sha256 %s", got)
	}
}

func TestSectionFilesOrderedAndNewlineTerminated(t *testing.T) {
	files := CompanionFiles()
	if len(files) < 2 || files[0] != "py/00_prelude.py" || files[len(files)-1] != "py/99_dispatch.py" {
		t.Fatalf("unexpected section order: %v", files)
	}
	for _, f := range files {
		b, _ := companionFS.ReadFile(f)
		if len(b) == 0 || b[len(b)-1] != '\n' {
			t.Errorf("%s must end with a newline (concatenation would merge lines)", f)
		}
	}
}
