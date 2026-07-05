package lumaudit

import (
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/imgdiff"
)

func TestAnalyzeFullyBlackImageFails(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))

	report := Analyze(img)
	if report.Pass {
		t.Fatalf("Analyze(black).Pass = true, want false")
	}
	if !near(report.FracBlack, 1.0, 0.0001) {
		t.Fatalf("Analyze(black).FracBlack = %.6f, want ~1.0", report.FracBlack)
	}
	if len(report.Reasons) == 0 {
		t.Fatalf("Analyze(black).Reasons is empty")
	}
}

func TestAnalyzeVariedMidGrayImagePasses(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			v := uint8(80 + (x+y)%96)
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}

	report := Analyze(img)
	if !report.Pass {
		t.Fatalf("Analyze(varied mid-gray).Pass = false, want true; report: %#v", report)
	}
	if report.Contrast <= 0 {
		t.Fatalf("Analyze(varied mid-gray).Contrast = %.6f, want > 0", report.Contrast)
	}
}

func TestAnalyzePolyWorldSliceIfPresent(t *testing.T) {
	const path = `C:/Users/jorda/code/games/poly-world/PolyWorld/Saved/PolySlice/slice_film_00_overview.png`
	if _, err := os.Stat(path); err != nil {
		t.Skipf("optional PolyWorld slice absent: %v", err)
	}

	img, err := imgdiff.Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	report := Analyze(img)
	t.Logf("PolyWorld slice MeanLuma=%.6f FracBlack=%.6f", report.MeanLuma, report.FracBlack)
	// The plan's Phase-0 gate is falsifiable against ground truth: the REAL shipped
	// PolyWorld frame is ~98% black (AGENTIC_GAMEDEV_PLAN.md §7.1). Assert the audit
	// actually FAILS it — not just that we can read the number.
	if report.Pass {
		t.Fatalf("real PolyWorld slice PASSED luminance audit; the audit is wrong: %+v", report)
	}
	if report.FracBlack < 0.90 {
		t.Fatalf("real PolyWorld slice FracBlack=%.4f, expected >0.90 (the documented 98%%-black defect)", report.FracBlack)
	}
}

func near(got, want, tolerance float64) bool {
	if got < want {
		return want-got <= tolerance
	}
	return got-want <= tolerance
}
