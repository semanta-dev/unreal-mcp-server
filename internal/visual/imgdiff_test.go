package visual

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// halfSplit: left half black, right half white — a stable perceptual pattern.
func halfSplit(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, color.Black)
			} else {
				img.Set(x, y, color.White)
			}
		}
	}
	return img
}

func TestMeanLuma(t *testing.T) {
	if l := MeanLuma(solid(32, 32, color.Black)); l > 0.01 {
		t.Errorf("black diffLuma = %g, want ~0", l)
	}
	if l := MeanLuma(solid(32, 32, color.White)); l < 0.99 {
		t.Errorf("white diffLuma = %g, want ~1", l)
	}
	mid := MeanLuma(solid(32, 32, color.RGBA{128, 128, 128, 255}))
	if mid < 0.4 || mid > 0.6 {
		t.Errorf("gray diffLuma = %g, want ~0.5", mid)
	}
}

func TestCompareIdenticalVsDifferent(t *testing.T) {
	a := halfSplit(64, 64)
	b := halfSplit(64, 64)
	r := Compare(a, b)
	if r.DHashDist != 0 || r.AHashDist != 0 {
		t.Errorf("identical images should have 0 hash distance, got %+v", r)
	}
	if r.LumaDelta > 0.001 {
		t.Errorf("identical diffLuma delta = %g", r.LumaDelta)
	}
	// A very different image (inverted split) must have a large dHash distance.
	inv := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				inv.Set(x, y, color.White)
			} else {
				inv.Set(x, y, color.Black)
			}
		}
	}
	rd := Compare(a, inv)
	if rd.DHashDist < 4 {
		t.Errorf("inverted image should differ a lot, dHash dist = %d", rd.DHashDist)
	}
}

func TestBlackFrameDetector(t *testing.T) {
	// The classic "did it render?" gate: a near-black frame.
	if MeanLuma(solid(100, 100, color.RGBA{2, 2, 2, 255})) > 0.05 {
		t.Error("near-black frame should read as low diffLuma")
	}
}

func TestHamming(t *testing.T) {
	if HammingDistance(0x0, 0xF) != 4 || HammingDistance(0xFF, 0xFF) != 0 {
		t.Error("hamming wrong")
	}
}
