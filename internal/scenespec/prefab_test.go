package scenespec

import "testing"

func TestRotateByPyrYaw90(t *testing.T) {
	// Positive yaw sends +X toward +Y.
	approxVec(t, rotateByPyr([3]float64{0, 90, 0}, [3]float64{100, 0, 0}), [3]float64{0, 100, 0})
	// +Y goes to -X under yaw 90.
	approxVec(t, rotateByPyr([3]float64{0, 90, 0}, [3]float64{0, 100, 0}), [3]float64{-100, 0, 0})
	// Identity leaves the vector unchanged.
	approxVec(t, rotateByPyr([3]float64{0, 0, 0}, [3]float64{3, 4, 5}), [3]float64{3, 4, 5})
}

func TestRotateByPyrPitchAndRoll(t *testing.T) {
	// Pitch 90 sends +X toward +Z (M[0][2] = sin(pitch)).
	approxVec(t, rotateByPyr([3]float64{90, 0, 0}, [3]float64{100, 0, 0}), [3]float64{0, 0, 100})
	// Roll 90 rotates about +X; UE's convention (right-wing-down) sends +Y to -Z.
	approxVec(t, rotateByPyr([3]float64{0, 0, 90}, [3]float64{0, 100, 0}), [3]float64{0, 0, -100})
}

func TestExpandPrefabParentYawComposition(t *testing.T) {
	members := []PrefabMember{
		{Role: "base", Kind: "static_mesh"},
		{Role: "arm", Kind: "static_mesh", Location: &[3]float64{100, 0, 0}},
	}
	// Parent rotated 90 deg yaw at origin: the arm's local +X offset lands on +Y.
	parent := Transform{RotationPyr: [3]float64{0, 90, 0}, Scale: [3]float64{1, 1, 1}}
	got := ExpandPrefab(members, parent, "s.g.0")

	if got[0].Label != "s.g.0.base" || got[1].Label != "s.g.0.arm" {
		t.Fatalf("labels: %q %q", got[0].Label, got[1].Label)
	}
	approxVec(t, got[0].Location, [3]float64{0, 0, 0})
	approxVec(t, got[1].Location, [3]float64{0, 100, 0})
	// The member inherits the parent yaw.
	if !approx(got[1].RotationPyr[1], 90) {
		t.Fatalf("arm yaw = %v, want 90", got[1].RotationPyr[1])
	}
}

func TestExpandPrefabParentLocationAndScale(t *testing.T) {
	members := []PrefabMember{
		{Role: "arm", Kind: "static_mesh", Location: &[3]float64{100, 0, 0}, Scale: &[3]float64{2, 2, 2}},
	}
	parent := Transform{
		Location:    [3]float64{10, 20, 0},
		RotationPyr: [3]float64{0, 90, 0},
		Scale:       [3]float64{3, 1, 1},
	}
	got := ExpandPrefab(members, parent, "p")
	// offset scaled by parent scale.x=3 => local (300,0,0), rotated 90 => (0,300,0),
	// then translated by parent location (10,20,0) => (10,320,0).
	approxVec(t, got[0].Location, [3]float64{10, 320, 0})
	// world scale = parent (3,1,1) * member (2,2,2) = (6,2,2).
	approxVec(t, got[0].Scale, [3]float64{6, 2, 2})
}

func TestComposePyrYawAddition(t *testing.T) {
	// Two yaw rotations add.
	got := composePyr([3]float64{0, 30, 0}, [3]float64{0, 60, 0})
	if !approx(got[0], 0) || !approx(got[1], 90) || !approx(got[2], 0) {
		t.Fatalf("compose yaw = %v, want [0 90 0]", got)
	}
	// Composing with identity returns the other rotation.
	got = composePyr([3]float64{0, 0, 0}, [3]float64{0, 45, 0})
	if !approx(got[1], 45) {
		t.Fatalf("compose identity yaw = %v, want 45", got[1])
	}
}

func TestMatRoundTripPyr(t *testing.T) {
	// pyrFromMat(matFromPyr(x)) == x for a non-trivial, non-gimbal rotation.
	in := [3]float64{20, 35, 15}
	out := pyrFromMat(matFromPyr(in))
	approxVec(t, out, in)
}
