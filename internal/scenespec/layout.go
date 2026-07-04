package scenespec

import (
	"math"
	"math/rand"
)

// defaultSpacing is used when a grid layout omits a positive spacing, so a bare
// {type:"grid", count:N} still produces distinct, non-overlapping points.
const defaultSpacing = 100.0

// Expand distributes a group's instances in space per the layout, returning one
// Transform per instance in deterministic order. Layout points are computed in
// the group-local frame, then composed under base: world location =
// base.Location + R(base) * point, world rotation = base rotation (with the
// ring's align-to-center yaw added), world scale = base.Scale. An unknown or
// empty layout yields no transforms.
func Expand(l Layout, base Transform) []Transform {
	pts, yaws := layoutPoints(l)
	out := make([]Transform, 0, len(pts))
	for i, p := range pts {
		world := Transform{
			Location:    addVec(base.Location, rotateByPyr(base.RotationPyr, p)),
			RotationPyr: base.RotationPyr,
			Scale:       base.Scale,
		}
		if yaws[i] != 0 {
			world.RotationPyr[1] = normDeg(base.RotationPyr[1] + yaws[i])
		}
		out = append(out, world)
	}
	return out
}

// layoutPoints returns the local-frame points and, parallel to them, a per-point
// local yaw contribution (nonzero only for align-to-center rings).
func layoutPoints(l Layout) (pts [][3]float64, yaws []float64) {
	switch l.Type {
	case "grid":
		return gridPoints(l), nil2(gridCount(l))
	case "ring":
		return ringPoints(l)
	case "line":
		return linePoints(l), nil2(l.Count)
	case "scatter":
		return scatterPoints(l), nil2(l.Count)
	default:
		return nil, nil
	}
}

// nil2 returns a zero-filled yaw slice of length n so callers can index it in
// lockstep with the point slice.
func nil2(n int) []float64 {
	if n <= 0 {
		return nil
	}
	return make([]float64, n)
}

// gridDims resolves the (rows, cols, count) of a grid: explicit rows*cols when
// both are given, otherwise a near-square grid holding count items.
func gridDims(l Layout) (rows, cols, count int) {
	if l.Rows > 0 && l.Cols > 0 {
		return l.Rows, l.Cols, l.Rows * l.Cols
	}
	if l.Count <= 0 {
		return 0, 0, 0
	}
	cols = int(math.Ceil(math.Sqrt(float64(l.Count))))
	rows = int(math.Ceil(float64(l.Count) / float64(cols)))
	return rows, cols, l.Count
}

func gridCount(l Layout) int {
	_, _, c := gridDims(l)
	return c
}

func gridPoints(l Layout) [][3]float64 {
	rows, cols, count := gridDims(l)
	if count == 0 {
		return nil
	}
	sp := l.Spacing
	if sp <= 0 {
		sp = defaultSpacing
	}
	halfW := float64(cols-1) * sp / 2
	halfH := float64(rows-1) * sp / 2
	pts := make([][3]float64, 0, count)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if r*cols+c >= count {
				return pts
			}
			pts = append(pts, [3]float64{
				l.Center[0] - halfW + float64(c)*sp,
				l.Center[1] - halfH + float64(r)*sp,
				l.Center[2],
			})
		}
	}
	return pts
}

func ringPoints(l Layout) (pts [][3]float64, yaws []float64) {
	if l.Count <= 0 {
		return nil, nil
	}
	step := 360.0 / float64(l.Count)
	pts = make([][3]float64, 0, l.Count)
	yaws = make([]float64, 0, l.Count)
	for i := 0; i < l.Count; i++ {
		ang := l.StartAngleDeg + step*float64(i)
		rad := ang * deg2rad
		pts = append(pts, [3]float64{
			l.Center[0] + l.Radius*math.Cos(rad),
			l.Center[1] + l.Radius*math.Sin(rad),
			l.Center[2],
		})
		if l.AlignToCenter {
			// Face the center: opposite the outward radial direction.
			yaws = append(yaws, normDeg(ang+180))
		} else {
			yaws = append(yaws, 0)
		}
	}
	return pts, yaws
}

func linePoints(l Layout) [][3]float64 {
	if l.Count <= 0 {
		return nil
	}
	pts := make([][3]float64, 0, l.Count)
	if l.Count == 1 {
		return append(pts, l.Start)
	}
	for i := 0; i < l.Count; i++ {
		t := float64(i) / float64(l.Count-1)
		pts = append(pts, [3]float64{
			l.Start[0] + (l.End[0]-l.Start[0])*t,
			l.Start[1] + (l.End[1]-l.Start[1])*t,
			l.Start[2] + (l.End[2]-l.Start[2])*t,
		})
	}
	return pts
}

func scatterPoints(l Layout) [][3]float64 {
	if l.Count <= 0 {
		return nil
	}
	r := rand.New(rand.NewSource(l.Seed))
	pts := make([][3]float64, 0, l.Count)
	for i := 0; i < l.Count; i++ {
		pts = append(pts, [3]float64{
			l.Center[0] + (r.Float64()*2-1)*l.Extent[0],
			l.Center[1] + (r.Float64()*2-1)*l.Extent[1],
			l.Center[2] + (r.Float64()*2-1)*l.Extent[2],
		})
	}
	return pts
}

// normDeg normalizes an angle in degrees to [0, 360).
func normDeg(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}
