package scenespec

import "testing"

func identity() Transform {
	return Transform{Scale: [3]float64{1, 1, 1}}
}

func TestExpandGridCentering(t *testing.T) {
	// count=4 => 2x2, spacing 200, centered at origin.
	got := Expand(Layout{Type: "grid", Count: 4, Spacing: 200}, identity())
	if len(got) != 4 {
		t.Fatalf("len = %d", len(got))
	}
	want := [][3]float64{
		{-100, -100, 0}, {100, -100, 0}, // row 0 (c0, c1)
		{-100, 100, 0}, {100, 100, 0}, // row 1
	}
	for i := range want {
		approxVec(t, got[i].Location, want[i])
	}
}

func TestExpandGridRowsCols(t *testing.T) {
	got := Expand(Layout{Type: "grid", Rows: 2, Cols: 3, Spacing: 100}, identity())
	if len(got) != 6 {
		t.Fatalf("len = %d", len(got))
	}
	// First point is top-left of a centered 3-wide, 2-tall grid.
	approxVec(t, got[0].Location, [3]float64{-100, -50, 0})
	// Last point is bottom-right.
	approxVec(t, got[5].Location, [3]float64{100, 50, 0})
}

func TestExpandRingAnglesAndAlign(t *testing.T) {
	got := Expand(Layout{Type: "ring", Count: 4, Radius: 100, StartAngleDeg: 0, AlignToCenter: true}, identity())
	if len(got) != 4 {
		t.Fatalf("len = %d", len(got))
	}
	// Positions step by 90 degrees starting at +X.
	approxVec(t, got[0].Location, [3]float64{100, 0, 0})
	approxVec(t, got[1].Location, [3]float64{0, 100, 0})
	approxVec(t, got[2].Location, [3]float64{-100, 0, 0})
	approxVec(t, got[3].Location, [3]float64{0, -100, 0})
	// align_to_center: yaw faces the center = angle + 180 (normalized).
	wantYaw := []float64{180, 270, 0, 90}
	for i, w := range wantYaw {
		if !approx(got[i].RotationPyr[1], w) {
			t.Fatalf("ring[%d] yaw = %v, want %v", i, got[i].RotationPyr[1], w)
		}
	}
}

func TestExpandRingNoAlignZeroYaw(t *testing.T) {
	got := Expand(Layout{Type: "ring", Count: 3, Radius: 50}, identity())
	for i, tr := range got {
		if tr.RotationPyr[1] != 0 {
			t.Fatalf("ring[%d] yaw = %v, want 0 without align", i, tr.RotationPyr[1])
		}
	}
}

func TestExpandLineEndpoints(t *testing.T) {
	start := [3]float64{0, 0, 0}
	end := [3]float64{300, 0, 0}
	got := Expand(Layout{Type: "line", Count: 4, Start: start, End: end}, identity())
	if len(got) != 4 {
		t.Fatalf("len = %d", len(got))
	}
	approxVec(t, got[0].Location, start)
	approxVec(t, got[1].Location, [3]float64{100, 0, 0})
	approxVec(t, got[2].Location, [3]float64{200, 0, 0})
	approxVec(t, got[3].Location, end)
}

func TestExpandLineSingle(t *testing.T) {
	start := [3]float64{5, 6, 7}
	got := Expand(Layout{Type: "line", Count: 1, Start: start, End: [3]float64{99, 99, 99}}, identity())
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	approxVec(t, got[0].Location, start)
}

func TestExpandScatterReproducibleUnderSeed(t *testing.T) {
	l := Layout{Type: "scatter", Count: 8, Center: [3]float64{0, 0, 0}, Extent: [3]float64{500, 500, 0}, Seed: 42}
	a := Expand(l, identity())
	b := Expand(l, identity())
	if len(a) != 8 || len(b) != 8 {
		t.Fatalf("len a=%d b=%d", len(a), len(b))
	}
	for i := range a {
		approxVec(t, a[i].Location, b[i].Location) // same seed => identical
	}
	// A different seed should move points.
	c := Expand(Layout{Type: "scatter", Count: 8, Extent: [3]float64{500, 500, 0}, Seed: 43}, identity())
	same := true
	for i := range a {
		if a[i].Location != c[i].Location {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seed produced identical scatter")
	}
	// Z extent is 0 => all points share the center Z.
	for _, tr := range a {
		if tr.Location[2] != 0 {
			t.Fatalf("z = %v, want 0 for zero extent", tr.Location[2])
		}
	}
}

func TestExpandUnknownEmpty(t *testing.T) {
	if got := Expand(Layout{Type: "spiral", Count: 4}, identity()); len(got) != 0 {
		t.Fatalf("unknown layout should be empty, got %v", got)
	}
}

func TestExpandBaseLocationOffsetsPoints(t *testing.T) {
	base := Transform{Location: [3]float64{1000, 0, 0}, Scale: [3]float64{1, 1, 1}}
	got := Expand(Layout{Type: "line", Count: 2, Start: [3]float64{0, 0, 0}, End: [3]float64{100, 0, 0}}, base)
	approxVec(t, got[0].Location, [3]float64{1000, 0, 0})
	approxVec(t, got[1].Location, [3]float64{1100, 0, 0})
}
