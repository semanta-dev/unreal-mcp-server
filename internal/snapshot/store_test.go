package snapshot

import (
	"errors"
	"testing"
	"time"
)

func actor(path, label string, x float64, tags ...string) Actor {
	return Actor{Path: path, Label: label, Class: "Actor", Tags: tags, Loc: [3]float64{x, 0, 0}, Scale: [3]float64{1, 1, 1}}
}

func TestDiffMatchesByPath(t *testing.T) {
	a := &File{Actors: []Actor{actor("/L.L:P.A_1", "A", 0), actor("/L.L:P.B_1", "B", 0, "t1"), actor("/L.L:P.C_1", "C", 0)}}
	// B renamed to "Bee" (same path, so not added/removed), moved and retagged; C removed;
	// a new actor reuses the label "C" under another path (added, not "unchanged").
	b := &File{Actors: []Actor{actor("/L.L:P.A_1", "A", 0.001), actor("/L.L:P.B_1", "Bee", 50, "t2"), actor("/L.L:P.C_2", "C", 0)}}
	d := Diff(a, b)
	if len(d.Added) != 1 || d.Added[0].Path != "/L.L:P.C_2" {
		t.Fatalf("added = %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Path != "/L.L:P.C_1" {
		t.Fatalf("removed = %+v", d.Removed)
	}
	if len(d.Moved) != 1 || d.Moved[0].Label != "Bee" || d.Moved[0].To[0][0] != 50 {
		t.Fatalf("moved = %+v (sub-tolerance jitter must not count)", d.Moved)
	}
	if len(d.Retagged) != 1 || d.Retagged[0].To[0] != "t2" {
		t.Fatalf("retagged = %+v", d.Retagged)
	}
}

func TestDiffRotationWraps(t *testing.T) {
	a := &File{Actors: []Actor{{Path: "p", Rot: [3]float64{0, 359.999, 0}, Scale: [3]float64{1, 1, 1}}}}
	b := &File{Actors: []Actor{{Path: "p", Rot: [3]float64{0, -0.001, 0}, Scale: [3]float64{1, 1, 1}}}}
	if d := Diff(a, b); len(d.Moved) != 0 {
		t.Fatalf("359.999° vs -0.001° is not a move: %+v", d.Moved)
	}
}

func TestDiffWorldPartitionUnloadedIsUnknown(t *testing.T) {
	a := &File{WorldPartition: true, Actors: []Actor{actor("p/near", "Near", 0), actor("p/far", "Far", 0)}}
	// In b the far cell is unloaded: Far is unknown, not removed. An actor loaded in b
	// but unloaded in a is unknown, not added.
	b := &File{WorldPartition: true, Unloaded: []string{"p/far"}, Actors: []Actor{actor("p/near", "Near", 0), actor("p/edge", "Edge", 0)}}
	a.Unloaded = []string{"p/edge"}
	d := Diff(a, b)
	if len(d.Removed) != 0 || len(d.Added) != 0 || len(d.Unknown) != 2 {
		t.Fatalf("diff = %+v", d)
	}
}

func TestStoreRoundTripAndNames(t *testing.T) {
	dir := t.TempDir()
	f := &File{Name: "before_fix", TakenAt: time.Now().UTC().Truncate(time.Second), World: "editor", Actors: []Actor{actor("p", "A", 1)}}
	if _, err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, "before_fix")
	if err != nil || len(got.Actors) != 1 || !got.TakenAt.Equal(f.TakenAt) {
		t.Fatalf("load = %+v, %v", got, err)
	}
	if _, err := Load(dir, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	for _, bad := range []string{"../x", "a/b", "", "a.b", "x y"} {
		if _, err := Save(dir, &File{Name: bad}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if l, _ := List(dir); len(l) != 1 || l[0].Name != "before_fix" || l[0].Actors != 1 {
		t.Fatalf("list = %+v", l)
	}
}
