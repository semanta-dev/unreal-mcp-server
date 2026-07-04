package digest

import "testing"

func TestQuantizers(t *testing.T) {
	// qpos: round-half-up (floor(v/b+0.5))
	for _, c := range []struct {
		v, b float64
		want int
	}{{100, 1, 100}, {100.4, 1, 100}, {100.5, 1, 101}, {-100.5, 1, -100}, {250, 100, 3}, {249, 100, 2}} {
		if got := qpos(c.v, c.b); got != c.want {
			t.Errorf("qpos(%g,%g)=%d want %d", c.v, c.b, got, c.want)
		}
	}
	// qangle: normalize + wrap (360 == 0)
	for _, c := range []struct {
		v, b float64
		want int
	}{{0, 1, 0}, {360, 1, 0}, {-90, 1, 270}, {359.6, 1, 0}, {90, 90, 1}, {450, 90, 1}} {
		if got := qangle(c.v, c.b); got != c.want {
			t.Errorf("qangle(%g,%g)=%d want %d", c.v, c.b, got, c.want)
		}
	}
	// qscale: milli-units
	if qscale(1.0) != 1000 || qscale(1.2345) != 1235 || qscale(0.5) != 500 {
		t.Errorf("qscale wrong: %d %d %d", qscale(1.0), qscale(1.2345), qscale(0.5))
	}
}

func TestDigestDeterministicAndOrderIndependent(t *testing.T) {
	q := Quant{PosBucket: 1, RotBucket: 1}
	a := []Transform{
		{Mesh: "/Game/M.SM_A", Loc: [3]float64{100, 0, 0}, Rot: [3]float64{0, 90, 0}, Scale: [3]float64{1, 1, 1}},
		{Mesh: "/Game/M.SM_B", Loc: [3]float64{0, 200, 0}, Rot: [3]float64{0, 0, 0}, Scale: [3]float64{2, 2, 2}},
	}
	// reversed input order must hash identically (lines are sorted)
	b := []Transform{a[1], a[0]}
	ra, rb := Digest(a, q), Digest(b, q)
	if ra.Hash != rb.Hash {
		t.Fatalf("order changed the hash: %s vs %s", ra.Hash, rb.Hash)
	}
	if ra.Count != 2 || len(ra.Hash) != 40 {
		t.Fatalf("bad result: %+v", ra)
	}
	// a moved instance (beyond a bucket) changes the hash
	c := []Transform{a[0], {Mesh: a[1].Mesh, Loc: [3]float64{0, 205, 0}, Rot: a[1].Rot, Scale: a[1].Scale}}
	if Digest(c, q).Hash == ra.Hash {
		t.Fatal("moving an instance did not change the hash")
	}
	// a move WITHIN the bucket does not change the hash (quantization)
	d := []Transform{a[0], {Mesh: a[1].Mesh, Loc: [3]float64{0.3, 200.2, 0}, Rot: a[1].Rot, Scale: a[1].Scale}}
	if Digest(d, q).Hash != ra.Hash {
		t.Fatal("sub-bucket jitter changed the hash (quantization not applied)")
	}
}

func TestDigestEmpty(t *testing.T) {
	r := Digest(nil, Quant{})
	if r.Count != 0 || len(r.Hash) != 40 || r.WorstPosMargin != 0 {
		t.Fatalf("empty digest: %+v", r)
	}
}

func TestMarginReport(t *testing.T) {
	// value 100.5 with bucket 1 sits exactly on a bucket edge -> margin ~0.5? No:
	// 100.5/1 - floor(100.5+0.5)=101 -> |0.5-... | edgeMargin = |100.5 - 101|=0.5*1.
	// A value at 100.0 is bucket-centered -> margin 0.5 (max). A value at 100.49 -> ~0.01.
	r := Digest([]Transform{{Mesh: "m", Loc: [3]float64{100.49, 0, 0}, Scale: [3]float64{1, 1, 1}}}, Quant{PosBucket: 1, RotBucket: 1})
	if r.WorstPosMargin > 0.02 {
		t.Errorf("near-edge value should report a tiny margin, got %g", r.WorstPosMargin)
	}
}
