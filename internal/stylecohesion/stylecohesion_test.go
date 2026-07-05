package stylecohesion

import (
	"image"
	"image/color"
	"testing"
)

func TestAnalyzeFailsFlatHalfNextToHighFrequencyTexture(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{R: 96, G: 96, B: 96, A: 255}
			if x >= 32 {
				if (x+y)%2 == 0 {
					c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
				} else {
					c = color.RGBA{R: 0, G: 0, B: 0, A: 255}
				}
			}
			img.Set(x, y, c)
		}
	}

	report := Analyze(img)
	if report.Pass {
		t.Fatalf("Analyze mixed flat/noise frame Pass = true, want false; report: %#v", report)
	}
	if report.SplitFraction <= splitFractionThreshold {
		t.Fatalf("SplitFraction = %v, want > %v; report: %#v", report.SplitFraction, splitFractionThreshold, report)
	}
}

func TestAnalyzePassesCoherentTextureVariance(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
			}
		}
	}

	report := Analyze(img)
	if !report.Pass {
		t.Fatalf("Analyze coherent frame Pass = false, want true; report: %#v", report)
	}
}
