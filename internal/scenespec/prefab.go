package scenespec

import "math"

// This file implements Unreal's FRotator (pitch/yaw/roll, in degrees) vector and
// rotation math, and prefab expansion built on it. Vectors are treated as row
// vectors so that applying rotation A then B is v * A * B, i.e. matrices compose
// left-to-right in application order. matFromPyr reproduces UE's FRotationMatrix
// and pyrFromMat reproduces UE's FMatrix::Rotator, so the rotators we emit match
// what the editor applies.

const deg2rad = math.Pi / 180
const rad2deg = 180 / math.Pi

// mat3 is a 3x3 rotation matrix whose rows are the rotated basis vectors.
type mat3 [3][3]float64

// matFromPyr builds UE's FRotationMatrix for rotation_pyr [pitch, yaw, roll] in
// degrees. With pitch=roll=0, positive yaw sends +X toward +Y.
func matFromPyr(pyr [3]float64) mat3 {
	cp, sp := math.Cos(pyr[0]*deg2rad), math.Sin(pyr[0]*deg2rad)
	cy, sy := math.Cos(pyr[1]*deg2rad), math.Sin(pyr[1]*deg2rad)
	cr, sr := math.Cos(pyr[2]*deg2rad), math.Sin(pyr[2]*deg2rad)
	return mat3{
		{cp * cy, cp * sy, sp},
		{sr*sp*cy - cr*sy, sr*sp*sy + cr*cy, -sr * cp},
		{-(cr*sp*cy + sr*sy), cy*sr - cr*sp*sy, cr * cp},
	}
}

// pyrFromMat extracts rotation_pyr (degrees) from a rotation matrix, mirroring
// UE's FMatrix::Rotator (pitch then yaw from the X row, then roll).
func pyrFromMat(m mat3) [3]float64 {
	x, y, z := m[0], m[1], m[2]
	pitch := math.Atan2(x[2], math.Hypot(x[0], x[1])) * rad2deg
	yaw := math.Atan2(x[1], x[0]) * rad2deg
	// Roll is recovered by comparing against a yaw/pitch-only frame's Y axis.
	sy := matFromPyr([3]float64{pitch, yaw, 0})[1]
	roll := math.Atan2(dot(z, sy), dot(y, sy)) * rad2deg
	return [3]float64{pitch, yaw, roll}
}

// matMul returns a*b (row-vector convention: v*a then *b == v*(a*b)).
func matMul(a, b mat3) mat3 {
	var out mat3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			out[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j]
		}
	}
	return out
}

// rotateByPyr rotates v by rotation_pyr (degrees), returning v * matFromPyr(pyr).
func rotateByPyr(pyr, v [3]float64) [3]float64 {
	m := matFromPyr(pyr)
	return [3]float64{
		v[0]*m[0][0] + v[1]*m[1][0] + v[2]*m[2][0],
		v[0]*m[0][1] + v[1]*m[1][1] + v[2]*m[2][1],
		v[0]*m[0][2] + v[1]*m[1][2] + v[2]*m[2][2],
	}
}

// composePyr returns the rotator for applying inner (a local rotation) first and
// then outer (its parent), matching how a child inherits its parent's rotation.
func composePyr(outer, inner [3]float64) [3]float64 {
	return pyrFromMat(matMul(matFromPyr(inner), matFromPyr(outer)))
}

func dot(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func addVec(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] + b[0], a[1] + b[1], a[2] + b[2]}
}

func mulVec(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] * b[0], a[1] * b[1], a[2] * b[2]}
}

// ExpandPrefab realizes each member of a prefab under a parent (instance)
// transform, composing the parent rotation/scale with each member's local
// offset. A member's world location is parent.Location + R(parent) * (member
// offset scaled by parent scale); its world rotation is the parent rotation
// composed with the member's; its world scale is the component-wise product.
// Member labels are "<labelPrefix>.<role>".
func ExpandPrefab(members []PrefabMember, parent Transform, labelPrefix string) []Placement {
	out := make([]Placement, 0, len(members))
	for _, m := range members {
		loc := orVec(m.Location, [3]float64{})
		rot := orVec(m.RotationPyr, [3]float64{})
		scl := orVec(m.Scale, [3]float64{1, 1, 1})

		worldLoc := addVec(parent.Location, rotateByPyr(parent.RotationPyr, mulVec(loc, parent.Scale)))
		worldRot := composePyr(parent.RotationPyr, rot)
		worldScale := mulVec(parent.Scale, scl)

		out = append(out, Placement{
			Label:          labelPrefix + "." + m.Role,
			Kind:           m.Kind,
			ClassPath:      m.ClassPath,
			StaticMeshPath: m.StaticMeshPath,
			Location:       worldLoc,
			RotationPyr:    worldRot,
			Scale:          worldScale,
		})
	}
	return out
}
