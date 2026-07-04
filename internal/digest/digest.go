// Package digest computes a deterministic integer-quantized hash over a set of
// transforms — the authored-content oracle for builder/sim games whose worlds are
// HISM/ISM instances (which actor-label diffs are blind to). It replaces the
// hand-rolled dual JS+C++ digest poly-world needed: the same worldState hashes to
// the same value here, in the browser preview, and in the engine, so an autographed
// map is verified by hash equality rather than "looks right".
//
// The contract mirrors poly-world's polycompile.js / PolyVerify.cpp bit-for-bit:
// per-item canonical line "mesh|qpos,qpos,qpos|qangle,qangle,qangle|qscale,qscale,
// qscale", code-point sorted, joined by "\n", SHA1-hex. Quantization buckets are
// inputs (a P0.3 margin measurement picks them), and the result reports the worst
// half-bucket margin so a too-coarse bucket is visible.
package digest

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Transform is one instance/actor placement to hash.
type Transform struct {
	Mesh  string     `json:"mesh"`
	Loc   [3]float64 `json:"loc"`   // x,y,z
	Rot   [3]float64 `json:"rot"`   // pitch,yaw,roll (degrees)
	Scale [3]float64 `json:"scale"` // sx,sy,sz
}

// Quant holds the quantization buckets. PosBucket is in world units, RotBucket in
// degrees. Zero buckets default to 1 (pos) / 1 (rot).
type Quant struct {
	PosBucket float64 `json:"pos_bucket"`
	RotBucket float64 `json:"rot_bucket"`
}

// Result is the digest plus the coarseness margin report.
type Result struct {
	Hash           string  `json:"hash"`
	Count          int     `json:"count"`
	WorstPosMargin float64 `json:"worst_pos_margin_uu"`  // smallest distance-to-bucket-edge across all pos components (uu)
	WorstRotMargin float64 `json:"worst_rot_margin_deg"` // smallest distance-to-bucket-edge across all rot components (deg)
}

// Digest computes the canonical hash of the transforms.
func Digest(items []Transform, q Quant) Result {
	pb, rb := q.PosBucket, q.RotBucket
	if pb <= 0 {
		pb = 1
	}
	if rb <= 0 {
		rb = 1
	}
	worstPos := math.Inf(1)
	worstRot := math.Inf(1)
	lines := make([]string, 0, len(items))
	for _, it := range items {
		for _, v := range it.Loc {
			if m := edgeMargin(v, pb); m < worstPos {
				worstPos = m
			}
		}
		for _, v := range it.Rot {
			if m := edgeMargin(v, rb); m < worstRot {
				worstRot = m
			}
		}
		lines = append(lines, line(it, pb, rb))
	}
	sort.Strings(lines) // code-point order == polycompile.js Array.sort / C++ CaseSensitive
	sum := sha1.Sum([]byte(strings.Join(lines, "\n")))
	if len(items) == 0 {
		worstPos, worstRot = 0, 0
	}
	return Result{Hash: hex.EncodeToString(sum[:]), Count: len(items), WorstPosMargin: worstPos, WorstRotMargin: worstRot}
}

func line(it Transform, pb, rb float64) string {
	return it.Mesh + "|" +
		fmt.Sprintf("%d,%d,%d", qpos(it.Loc[0], pb), qpos(it.Loc[1], pb), qpos(it.Loc[2], pb)) + "|" +
		fmt.Sprintf("%d,%d,%d", qangle(it.Rot[0], rb), qangle(it.Rot[1], rb), qangle(it.Rot[2], rb)) + "|" +
		fmt.Sprintf("%d,%d,%d", qscale(it.Scale[0]), qscale(it.Scale[1]), qscale(it.Scale[2]))
}

// qpos quantizes a world-unit coordinate: floor(v/b + 0.5) (round-half-up).
func qpos(v, b float64) int { return int(math.Floor(v/b + 0.5)) }

// qangle normalizes an angle to [0,360), buckets it, and wraps the index so 360==0.
func qangle(v, b float64) int {
	a := math.Mod(v, 360)
	if a < 0 {
		a += 360
	}
	idx := int(math.Floor(a/b + 0.5))
	nb := int(math.Max(1, math.Round(360/b)))
	idx = ((idx % nb) + nb) % nb
	return idx
}

// qscale quantizes a scale factor to milli-units: floor(s*1000 + 0.5).
func qscale(s float64) int { return int(math.Floor(s*1000 + 0.5)) }

// edgeMargin is the distance from v to the nearest bucket boundary (how close a
// value sits to flipping to an adjacent bucket). A small margin across the set
// means the bucket is dangerously coarse for that content.
func edgeMargin(v, b float64) float64 {
	frac := math.Abs(v/b - math.Floor(v/b+0.5)) // in [0, 0.5]
	return frac * b
}
