package framing

import (
	"math"
	"testing"
)

const eps = 1e-6

func approx(a, b, e float64) bool { return math.Abs(a-b) <= e }

// angEq compares two angles in degrees, tolerant of 360-degree wrap.
func angEq(a, b, e float64) bool {
	d := math.Mod(a-b, 360)
	if d > 180 {
		d -= 360
	}
	if d < -180 {
		d += 360
	}
	return math.Abs(d) <= e
}

func TestLookAtPyr(t *testing.T) {
	origin := [3]float64{0, 0, 0}
	cases := []struct {
		name      string
		from      [3]float64
		wantYaw   float64
		wantPitch float64
	}{
		// A camera above and behind, looking down at 45 degrees.
		{"above-behind -X", [3]float64{-1000, 0, 1000}, 0, -45},
		// Four cardinal placements at the same height (pitch 0).
		{"+X looks -X", [3]float64{1000, 0, 0}, 180, 0},
		{"-X looks +X", [3]float64{-1000, 0, 0}, 0, 0},
		{"+Y looks -Y", [3]float64{0, 1000, 0}, -90, 0},
		{"-Y looks +Y", [3]float64{0, -1000, 0}, 90, 0},
		// Straight overhead => yaw 0 (atan2(0,0)), pitch -90 (looking down).
		{"overhead", [3]float64{0, 0, 1000}, 0, -90},
		// Directly below => pitch +90 (looking up).
		{"below", [3]float64{0, 0, -1000}, 0, 90},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := lookAtPyr(c.from, origin)
			if !angEq(r[1], c.wantYaw, eps) {
				t.Errorf("yaw = %v, want %v", r[1], c.wantYaw)
			}
			if !approx(r[0], c.wantPitch, eps) {
				t.Errorf("pitch = %v, want %v", r[0], c.wantPitch)
			}
			if r[2] != 0 {
				t.Errorf("roll = %v, want 0", r[2])
			}
		})
	}
}

func TestBoundsCenterAndRadius(t *testing.T) {
	b := Bounds{Origin: [3]float64{1, 2, 3}, Extent: [3]float64{2, 3, 6}}
	if got := b.Center(); got != b.Origin {
		t.Errorf("Center() = %v, want %v", got, b.Origin)
	}
	// |(2,3,6)| = 7.
	if got := b.Radius(); !approx(got, 7, eps) {
		t.Errorf("Radius() = %v, want 7", got)
	}

	// Degenerate extent falls back to the sentinel (>= 1000).
	deg := Bounds{Origin: [3]float64{9, 9, 9}}
	if got := deg.Radius(); got < 1000 {
		t.Errorf("degenerate Radius() = %v, want >= 1000 sentinel", got)
	}
	if deg.Radius() != defaultRadius {
		t.Errorf("degenerate Radius() = %v, want %v", deg.Radius(), defaultRadius)
	}
}

func TestCombineBounds(t *testing.T) {
	// Two disjoint unit boxes centered at x=0 and x=10.
	a := Bounds{Origin: [3]float64{0, 0, 0}, Extent: [3]float64{1, 1, 1}}
	b := Bounds{Origin: [3]float64{10, 0, 0}, Extent: [3]float64{1, 1, 1}}
	u := CombineBounds([]Bounds{a, b})

	// Union spans x in [-1, 11] => origin x=5, extent x=6; y,z unchanged.
	wantOrigin := [3]float64{5, 0, 0}
	wantExtent := [3]float64{6, 1, 1}
	if u.Origin != wantOrigin {
		t.Errorf("Origin = %v, want %v", u.Origin, wantOrigin)
	}
	if u.Extent != wantExtent {
		t.Errorf("Extent = %v, want %v", u.Extent, wantExtent)
	}

	// Order independence.
	u2 := CombineBounds([]Bounds{b, a})
	if u2 != u {
		t.Errorf("CombineBounds not order independent: %v vs %v", u2, u)
	}

	// A single box round-trips unchanged.
	if got := CombineBounds([]Bounds{a}); got != a {
		t.Errorf("single-box union = %v, want %v", got, a)
	}

	// Empty slice => zero Bounds, whose Radius() falls back to the sentinel.
	empty := CombineBounds(nil)
	if empty != (Bounds{}) {
		t.Errorf("empty union = %v, want zero Bounds", empty)
	}
	if empty.Radius() < 1000 {
		t.Errorf("empty union Radius() = %v, want sentinel >= 1000", empty.Radius())
	}
}

func TestOrbitPosesEmpty(t *testing.T) {
	if got := OrbitPoses([3]float64{0, 0, 0}, 100, 0, 0, 0.5); got != nil {
		t.Errorf("num=0 => %v, want nil", got)
	}
	if got := OrbitPoses([3]float64{0, 0, 0}, 100, -3, 0, 0.5); got != nil {
		t.Errorf("num<0 => %v, want nil", got)
	}
}

func TestOrbitPosesGeometry(t *testing.T) {
	center := [3]float64{100, 200, 50}
	radius := 500.0
	num := 8
	heightFrac := 0.5
	poses := OrbitPoses(center, radius, num, 0, heightFrac)

	if len(poses) != num {
		t.Fatalf("len = %d, want %d", len(poses), num)
	}

	wantZ := center[2] + radius*heightFrac
	wantPitch := math.Atan2(-radius*heightFrac, radius) * degPerRad // camera above => negative
	var prevYaw float64
	for i, p := range poses {
		// Height is constant and matches radius*heightFrac.
		if !approx(p.Location[2], wantZ, 1e-9) {
			t.Errorf("pose %d z = %v, want %v", i, p.Location[2], wantZ)
		}
		// Horizontal distance from center equals radius.
		dx := p.Location[0] - center[0]
		dy := p.Location[1] - center[1]
		if h := math.Hypot(dx, dy); !approx(h, radius, 1e-6) {
			t.Errorf("pose %d horizontal radius = %v, want %v", i, h, radius)
		}
		// Rotation is the exact look-at from the location back to center.
		want := lookAtPyr(p.Location, center)
		if !angEq(p.RotationPyr[1], want[1], eps) || !approx(p.RotationPyr[0], want[0], eps) || p.RotationPyr[2] != 0 {
			t.Errorf("pose %d rotation = %v, want look-at %v", i, p.RotationPyr, want)
		}
		// Pitch is identical for every pose (constant height/radius).
		if !approx(p.RotationPyr[0], wantPitch, eps) {
			t.Errorf("pose %d pitch = %v, want %v", i, p.RotationPyr[0], wantPitch)
		}
		// Azimuth is evenly spaced: consecutive yaws differ by 360/num.
		if i > 0 {
			if !angEq(p.RotationPyr[1]-prevYaw, 360/float64(num), eps) {
				t.Errorf("pose %d yaw step = %v, want %v", i, p.RotationPyr[1]-prevYaw, 360/float64(num))
			}
		}
		prevYaw = p.RotationPyr[1]
	}
}

func TestOrbitPosesRadiusSentinel(t *testing.T) {
	// radius <= 0 uses the sentinel radius for both placement and height.
	poses := OrbitPoses([3]float64{0, 0, 0}, 0, 4, 0, 0.5)
	if len(poses) != 4 {
		t.Fatalf("len = %d, want 4", len(poses))
	}
	for i, p := range poses {
		dx, dy := p.Location[0], p.Location[1]
		if h := math.Hypot(dx, dy); !approx(h, defaultRadius, 1e-6) {
			t.Errorf("pose %d horizontal radius = %v, want sentinel %v", i, h, defaultRadius)
		}
		if !approx(p.Location[2], defaultRadius*0.5, 1e-6) {
			t.Errorf("pose %d z = %v, want %v", i, p.Location[2], defaultRadius*0.5)
		}
	}
}

func TestOrbitPosesPitchFallback(t *testing.T) {
	// heightFrac == 0 with a non-zero pitchDeg derives the height so the look-at
	// pitch equals pitchDeg. -30 degrees => camera raised above, looking down.
	center := [3]float64{0, 0, 0}
	radius := 400.0
	poses := OrbitPoses(center, radius, 6, -30, 0)
	if len(poses) != 6 {
		t.Fatalf("len = %d, want 6", len(poses))
	}
	for i, p := range poses {
		if p.Location[2] <= 0 {
			t.Errorf("pose %d z = %v, want raised above center", i, p.Location[2])
		}
		if !approx(p.RotationPyr[0], -30, eps) {
			t.Errorf("pose %d pitch = %v, want -30", i, p.RotationPyr[0])
		}
	}

	// pitchDeg is ignored when heightFrac is non-zero.
	pp := OrbitPoses(center, radius, 4, -30, 0.25)
	for i, p := range pp {
		if !approx(p.Location[2], radius*0.25, 1e-6) {
			t.Errorf("pose %d z = %v, want %v (heightFrac wins)", i, p.Location[2], radius*0.25)
		}
	}
}

// framedDistance returns the 3D distance from the camera to the bounds center.
func framedDistance(b Bounds, p Pose) float64 {
	c := b.Center()
	dx := p.Location[0] - c[0]
	dy := p.Location[1] - c[1]
	dz := p.Location[2] - c[2]
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func TestFrameShotDistanceSolve(t *testing.T) {
	// A bounds whose Radius() is exactly r (extent along one axis).
	mk := func(r float64) Bounds { return Bounds{Extent: [3]float64{r, 0, 0}} }

	// The solved distance matches (radius/fill)/tan(fov/2).
	b := mk(200)
	p := FrameShot(b, 30, 20, 60, 0.8)
	wantDist := (200.0 / 0.8) / math.Tan(60.0/2*radPerDeg)
	if got := framedDistance(b, p); !approx(got, wantDist, 1e-6) {
		t.Errorf("distance = %v, want %v", got, wantDist)
	}

	// Larger radius => farther camera (same fill/fov).
	near := FrameShot(mk(100), 0, 0, 60, 0.8)
	far := FrameShot(mk(300), 0, 0, 60, 0.8)
	if framedDistance(mk(300), far) <= framedDistance(mk(100), near) {
		t.Errorf("larger radius did not push camera farther")
	}

	// Smaller fill => farther camera (same radius/fov).
	fillBig := FrameShot(mk(200), 0, 0, 60, 0.9)
	fillSmall := FrameShot(mk(200), 0, 0, 60, 0.3)
	if framedDistance(mk(200), fillSmall) <= framedDistance(mk(200), fillBig) {
		t.Errorf("smaller fill did not push camera farther")
	}
}

func TestFrameShotClamps(t *testing.T) {
	b := Bounds{Extent: [3]float64{150, 0, 0}}

	// fovDeg below 1 clamps to minFOV; a wildly large fov clamps to maxFOV.
	lowClamped := FrameShot(b, 0, 0, 0, 0.8)
	atMin := FrameShot(b, 0, 0, minFOV, 0.8)
	if !approx(framedDistance(b, lowClamped), framedDistance(b, atMin), 1e-9) {
		t.Errorf("fov=0 did not clamp to minFOV")
	}
	highClamped := FrameShot(b, 0, 0, 5000, 0.8)
	atMax := FrameShot(b, 0, 0, maxFOV, 0.8)
	if !approx(framedDistance(b, highClamped), framedDistance(b, atMax), 1e-9) {
		t.Errorf("huge fov did not clamp to maxFOV")
	}
	// Narrow fov => farther than wide fov.
	if framedDistance(b, atMin) <= framedDistance(b, atMax) {
		t.Errorf("narrow fov should be farther than wide fov")
	}

	// fill <= 0 clamps to minFill (finite, large distance) and fill > 1 clamps to 1.
	over := FrameShot(b, 0, 0, 60, 5)
	atOne := FrameShot(b, 0, 0, 60, 1)
	if !approx(framedDistance(b, over), framedDistance(b, atOne), 1e-9) {
		t.Errorf("fill>1 did not clamp to 1")
	}
	zero := FrameShot(b, 0, 0, 60, 0)
	d := framedDistance(b, zero)
	if math.IsNaN(d) || math.IsInf(d, 0) {
		t.Errorf("fill=0 produced non-finite distance %v", d)
	}
}

func TestFrameShotDegenerateNoNaN(t *testing.T) {
	// Zero bounds => sentinel radius => finite, well-formed pose.
	p := FrameShot(Bounds{}, 45, 30, 60, 0.75)
	for i, v := range p.Location {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("Location[%d] = %v, want finite", i, v)
		}
	}
	for i, v := range p.RotationPyr {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("RotationPyr[%d] = %v, want finite", i, v)
		}
	}
	// Distance equals the solve using the sentinel radius.
	want := (defaultRadius / 0.75) / math.Tan(60.0/2*radPerDeg)
	if got := framedDistance(Bounds{}, p); !approx(got, want, 1e-6) {
		t.Errorf("degenerate distance = %v, want %v", got, want)
	}
}

func TestFrameShotElevationSign(t *testing.T) {
	b := Bounds{Extent: [3]float64{100, 0, 0}}

	// Positive elevation => camera above => negative pitch (looking down).
	up := FrameShot(b, 0, 45, 60, 0.8)
	if up.Location[2] <= b.Center()[2] {
		t.Errorf("elevation +45 should raise the camera, z = %v", up.Location[2])
	}
	if up.RotationPyr[0] >= 0 {
		t.Errorf("elevation +45 pitch = %v, want negative", up.RotationPyr[0])
	}

	// Negative elevation => camera below => positive pitch (looking up).
	down := FrameShot(b, 0, -45, 60, 0.8)
	if down.RotationPyr[0] <= 0 {
		t.Errorf("elevation -45 pitch = %v, want positive", down.RotationPyr[0])
	}

	// Zero elevation => level => pitch ~ 0.
	level := FrameShot(b, 0, 0, 60, 0.8)
	if !approx(level.RotationPyr[0], 0, eps) {
		t.Errorf("elevation 0 pitch = %v, want 0", level.RotationPyr[0])
	}

	// The camera always looks at center: recomputed look-at matches stored rotation.
	for _, p := range []Pose{up, down, level} {
		want := lookAtPyr(p.Location, b.Center())
		if !approx(p.RotationPyr[0], want[0], eps) || !angEq(p.RotationPyr[1], want[1], eps) {
			t.Errorf("rotation %v not a look-at (want %v)", p.RotationPyr, want)
		}
	}
}
