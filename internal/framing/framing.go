// Package framing is pure camera-pose math shared by capture (orbit contact
// sheets), viewport focus, and camera bookmarks. It solves where to put a
// camera and how to orient it so a target (or a bounds) is framed the way the
// caller asks; the editor side consumes the resulting poses and just renders
// them.
//
// Conventions match Unreal Engine exactly (left-handed, Z up, units = cm):
//   - Location/scale/extent are [3]float64 = [x, y, z].
//   - Rotation is RotationPyr = [pitch, yaw, roll] in degrees.
//   - Yaw: 0 == +X, increasing toward +Y. yaw = atan2(dirY, dirX).
//   - Pitch: positive pitches the view UP; a camera above its target looks
//     down and therefore has NEGATIVE pitch.
//
// Everything here is deterministic and side-effect free, so it is fully unit
// tested without an editor.
package framing

import "math"

const (
	degPerRad = 180.0 / math.Pi
	radPerDeg = math.Pi / 180.0

	// defaultRadius is the sentinel fallback radius (Unreal units) used when a
	// bounds is degenerate. It mirrors take_screenshot's framing fallback so
	// distance solves never divide by zero and callers always get a usable,
	// finite camera pose. It satisfies the ">= 1000" contract.
	defaultRadius = 1000.0

	// epsilon is the "effectively zero" threshold for degenerate-bounds and
	// look-at recomputation checks.
	epsilon = 1e-6

	// FrameShot clamps for numerically safe, sane framing.
	minFill = 1e-3  // avoid absurd (infinite) camera distances for fill<=0
	minFOV  = 1.0   // degrees
	maxFOV  = 170.0 // degrees

	// pitchDerivLimit bounds the pitchDeg used to derive an orbit height so the
	// tangent never blows up near the poles.
	pitchDerivLimit = 89.0 // degrees
)

// Pose is a fully-specified camera transform: where the camera is and how it is
// oriented (as an Unreal [pitch, yaw, roll] rotator, in degrees).
type Pose struct {
	Location    [3]float64
	RotationPyr [3]float64 // [pitch, yaw, roll]
}

// Bounds is an axis-aligned box in the UE get_actor_bounds convention: Origin is
// the box center and Extent is the half-size along each axis.
type Bounds struct {
	Origin [3]float64
	Extent [3]float64 // half-size (not full size)
}

// Center returns the box center, which is exactly the Origin.
func (b Bounds) Center() [3]float64 { return b.Origin }

// Radius returns the length of the Extent (half-diagonal) vector — the radius of
// the sphere that circumscribes the box. A degenerate (~zero) extent falls back
// to defaultRadius so callers framing this bounds never divide by zero.
func (b Bounds) Radius() float64 {
	r := math.Sqrt(b.Extent[0]*b.Extent[0] + b.Extent[1]*b.Extent[1] + b.Extent[2]*b.Extent[2])
	if r < epsilon {
		return defaultRadius
	}
	return r
}

// CombineBounds returns the union axis-aligned box that encloses every input
// bounds. An empty slice yields the zero Bounds; its Radius() then falls back to
// the sentinel default, so downstream distance solves stay safe.
func CombineBounds(bs []Bounds) Bounds {
	if len(bs) == 0 {
		return Bounds{}
	}
	// Seed the running min/max corners with the first box, then fold the rest.
	var min, max [3]float64
	for a := 0; a < 3; a++ {
		min[a] = bs[0].Origin[a] - bs[0].Extent[a]
		max[a] = bs[0].Origin[a] + bs[0].Extent[a]
	}
	for _, b := range bs[1:] {
		for a := 0; a < 3; a++ {
			lo := b.Origin[a] - b.Extent[a]
			hi := b.Origin[a] + b.Extent[a]
			if lo < min[a] {
				min[a] = lo
			}
			if hi > max[a] {
				max[a] = hi
			}
		}
	}
	var out Bounds
	for a := 0; a < 3; a++ {
		out.Origin[a] = (min[a] + max[a]) / 2
		out.Extent[a] = (max[a] - min[a]) / 2
	}
	return out
}

// lookAtPyr returns the UE rotator [pitch, yaw, roll] that orients a camera at
// `from` to look at `to`. Roll is always 0. Yaw = atan2(dirY, dirX); pitch =
// atan2(dirZ, hypot(dirX, dirY)) so a camera above its target (dirZ < 0) yields
// a negative pitch, exactly as Unreal expects.
func lookAtPyr(from, to [3]float64) [3]float64 {
	dx := to[0] - from[0]
	dy := to[1] - from[1]
	dz := to[2] - from[2]
	yaw := math.Atan2(dy, dx) * degPerRad
	pitch := math.Atan2(dz, math.Hypot(dx, dy)) * degPerRad
	return [3]float64{pitch, yaw, 0}
}

// OrbitPoses returns num camera poses evenly spaced in azimuth on a ring of the
// given radius around center. Every camera looks AT center, so its RotationPyr
// is the exact look-at from the pose's location — pitch follows from the
// camera's height (raised above center => looks down => negative pitch), yaw
// points back toward center, roll is 0.
//
// Vertical placement: the camera height above center is radius*heightFrac. As a
// convenience, when heightFrac == 0 a non-zero pitchDeg is honored instead by
// raising the camera to the height whose look-at pitch equals pitchDeg (so a
// negative pitchDeg tilts the view down from above); pitchDeg is clamped away
// from the poles to keep the height finite.
//
// num <= 0 returns nil. A non-positive radius falls back to the degenerate
// sentinel radius so the ring is always non-degenerate.
func OrbitPoses(center [3]float64, radius float64, num int, pitchDeg float64, heightFrac float64) []Pose {
	if num <= 0 {
		return nil
	}
	if radius <= 0 {
		radius = defaultRadius
	}

	// Height of every camera above center.
	h := radius * heightFrac
	if heightFrac == 0 && pitchDeg != 0 {
		p := pitchDeg
		if p > pitchDerivLimit {
			p = pitchDerivLimit
		} else if p < -pitchDerivLimit {
			p = -pitchDerivLimit
		}
		// A camera at horizontal distance `radius` and height h looks at center
		// with pitch = atan2(-h, radius); solving for the requested pitch gives
		// h = -radius*tan(pitchDeg).
		h = -radius * math.Tan(p*radPerDeg)
	}
	z := center[2] + h

	poses := make([]Pose, num)
	for i := range poses {
		az := 2 * math.Pi * float64(i) / float64(num)
		loc := [3]float64{
			center[0] + radius*math.Cos(az),
			center[1] + radius*math.Sin(az),
			z,
		}
		poses[i] = Pose{Location: loc, RotationPyr: lookAtPyr(loc, center)}
	}
	return poses
}

// FrameShot places a single camera at the given azimuth/elevation around the
// center of b, at a distance solved so the bounds' radius fills `fill` fraction
// of the vertical field of view, and orients it to look at the center.
//
// distance = (radius / fill) / tan(fovDeg/2), so a larger radius or a smaller
// fill pushes the camera farther back. fill is clamped to (0, 1] and fovDeg to a
// sane (1, 170) so the solve is always finite. A degenerate bounds contributes
// the sentinel radius, so the result never contains NaN or Inf.
//
// elevationDeg is the camera's elevation above the horizon: positive elevation
// places the camera above the target, which (via the look-at) yields a negative
// pitch.
func FrameShot(b Bounds, azimuthDeg float64, elevationDeg float64, fovDeg float64, fill float64) Pose {
	fill = clamp(fill, minFill, 1)
	fovDeg = clamp(fovDeg, minFOV, maxFOV)

	radius := b.Radius() // already sentinel-guarded against degenerate bounds
	distance := (radius / fill) / math.Tan(fovDeg/2*radPerDeg)

	center := b.Center()
	az := azimuthDeg * radPerDeg
	el := elevationDeg * radPerDeg
	horiz := distance * math.Cos(el)
	loc := [3]float64{
		center[0] + horiz*math.Cos(az),
		center[1] + horiz*math.Sin(az),
		center[2] + distance*math.Sin(el),
	}
	return Pose{Location: loc, RotationPyr: lookAtPyr(loc, center)}
}

// clamp bounds v to the inclusive range [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
