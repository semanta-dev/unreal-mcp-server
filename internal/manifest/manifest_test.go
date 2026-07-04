package manifest

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/snippets"
)

// TestManifestBijection is the §5.3 conformance gate: every op in the editor's _OPS table
// must have a classification, and every classification must map to a real op. This FAILS
// CI if a new op is added without classifying it (so it can't silently bypass the gate) or
// if a classification is left behind for a removed op.
func TestManifestBijection(t *testing.T) {
	missing, extra := Verify(snippets.OpNames())
	if len(missing) > 0 {
		t.Fatalf("%d op(s) have NO classification (fail-closed boundary — classify them in table): %v", len(missing), missing)
	}
	if len(extra) > 0 {
		t.Fatalf("%d classified op(s) do not exist in _OPS (remove them): %v", len(extra), extra)
	}
}

// TestFailClosedDefault pins the safety default: an unknown op is Exec-tier + gateable, so
// nothing dangerous is ever permissively allowed.
func TestFailClosedDefault(t *testing.T) {
	c, known := Classify("a_brand_new_unclassified_op")
	if known {
		t.Fatal("an unclassified op must report known=false")
	}
	if c.Class != Exec || !c.Gateable || !c.TouchesAsset {
		t.Fatalf("fail-closed default must be exec+gateable+touches_asset, got %+v", c)
	}
}

// TestReadonlyOpsNotGateable sanity-checks the table's internal consistency once filled:
// pure-readonly ops carry no destructive flags. (Skips gracefully until the table exists.)
func TestClassInvariants(t *testing.T) {
	if len(table) == 0 {
		t.Skip("classification table not yet populated")
	}
	for op, c := range table {
		switch c.Class {
		case Readonly:
			if c.TouchesAsset {
				t.Errorf("%s is readonly but touches_asset=true", op)
			}
		case Destructive, Exec, SCC:
			if !c.Gateable {
				t.Errorf("%s is %s but not gateable (must be gateable, §5.3)", op, c.Class)
			}
		}
	}
}
