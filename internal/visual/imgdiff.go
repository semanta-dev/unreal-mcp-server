// imgdiff (merged into package visual) provides dependency-free perceptual image comparison for visual
// verification: a black/blank-frame detector (mean diffLuma) and aHash/dHash
// perceptual hashes with Hamming distance, so a rubric can assert "the frame
// isn't visually broken" and a golden-image gate can assert "this shot matches
// the baseline within epsilon" over the PNG frames already on disk. Pure stdlib
// image; no new dependencies.
package visual

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"math/bits"
	"os"
)

// Load decodes a PNG (by content, so an extensionless file works too).
func Load(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// MeanLuma returns the average perceptual luminance in [0,1]. A near-zero value
// is a black/unrendered frame; near-one is white/blown-out.
func MeanLuma(img image.Image) float64 {
	b := img.Bounds()
	if b.Empty() {
		return 0
	}
	var sum float64
	var n float64
	// Sample on a grid capped at ~64x64 for speed on large frames.
	stepX := max1(b.Dx() / 64)
	stepY := max1(b.Dy() / 64)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			sum += diffLuma(img.At(x, y))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// AHash is a 64-bit average hash (8x8 grayscale, bit set where >= mean).
func AHash(img image.Image) uint64 {
	g := grayscale8x8(img)
	var mean float64
	for _, v := range g {
		mean += v
	}
	mean /= float64(len(g))
	var h uint64
	for i, v := range g {
		if v >= mean {
			h |= 1 << uint(i)
		}
	}
	return h
}

// DHash is a 64-bit difference hash (8x9 grayscale rows, bit set where a pixel is
// brighter than its right neighbor) — robust to brightness/gamma shifts.
func DHash(img image.Image) uint64 {
	g := grayscaleWH(img, 9, 8) // 9 wide, 8 tall
	var h uint64
	bit := 0
	for row := 0; row < 8; row++ {
		for col := 0; col < 8; col++ {
			left := g[row*9+col]
			right := g[row*9+col+1]
			if left > right {
				h |= 1 << uint(bit)
			}
			bit++
		}
	}
	return h
}

// HammingDistance counts differing bits between two hashes (0 = identical).
func HammingDistance(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// DiffResult summarizes a comparison between two images.
type DiffResult struct {
	AHashDist int     `json:"ahash_dist"`
	DHashDist int     `json:"dhash_dist"`
	LumaDelta float64 `json:"luma_delta"`
	LumaA     float64 `json:"luma_a"`
	LumaB     float64 `json:"luma_b"`
}

// Compare returns perceptual distances between two images. A dHash distance of 0
// is a near-pixel-identical match; small (<=~5) is visually the same shot.
func Compare(a, b image.Image) DiffResult {
	la, lb := MeanLuma(a), MeanLuma(b)
	return DiffResult{
		AHashDist: HammingDistance(AHash(a), AHash(b)),
		DHashDist: HammingDistance(DHash(a), DHash(b)),
		LumaDelta: math.Abs(la - lb),
		LumaA:     la, LumaB: lb,
	}
}

// --- helpers ---

func diffLuma(c color.Color) float64 {
	r, g, b, _ := c.RGBA() // 0..65535
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
}

func grayscale8x8(img image.Image) []float64 { return grayscaleWH(img, 8, 8) }

// grayscaleWH resamples the image to w x h grayscale via nearest-neighbor.
func grayscaleWH(img image.Image, w, h int) []float64 {
	b := img.Bounds()
	out := make([]float64, w*h)
	if b.Empty() {
		return out
	}
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			sx := b.Min.X + i*b.Dx()/w
			sy := b.Min.Y + j*b.Dy()/h
			out[j*w+i] = diffLuma(img.At(sx, sy))
		}
	}
	return out
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}
