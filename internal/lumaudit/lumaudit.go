// Package lumaudit implements the luminance_report Layer-A audit from
// AGENTIC_GAMEDEV_PLAN.md §7.1 / RC1, detecting black, blown-out, and low-signal
// captured frames such as PolyWorld's 98%-black renders.
package lumaudit

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/jdziat/unreal-mcp-server/internal/gametrace"
	"github.com/jdziat/unreal-mcp-server/internal/imgdiff"
)

const (
	maxGridSamples = 128

	blackThreshold = 0.04
	blownThreshold = 0.96

	maxFracBlack = 0.90
	minMeanLuma  = 0.03
	maxMeanLuma  = 0.97
)

// Report is the luminance audit result.
type Report struct {
	Pass      bool
	MeanLuma  float64
	FracBlack float64
	FracBlown float64
	Contrast  float64
	Reasons   []string
}

// Analyze computes luminance statistics over an image using a grid capped at
// about 128x128 samples.
func Analyze(img image.Image) Report {
	b := img.Bounds()
	if b.Empty() {
		return Report{
			Pass:      false,
			MeanLuma:  imgdiff.MeanLuma(img),
			FracBlack: 0,
			FracBlown: 0,
			Contrast:  0,
			Reasons:   []string{"empty image"},
		}
	}

	stepX := ceilDiv(b.Dx(), maxGridSamples)
	stepY := ceilDiv(b.Dy(), maxGridSamples)

	var sum, sumSquares float64
	var black, blown, samples int
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			v := luma(img.At(x, y))
			sum += v
			sumSquares += v * v
			if v < blackThreshold {
				black++
			}
			if v > blownThreshold {
				blown++
			}
			samples++
		}
	}

	mean := sum / float64(samples)
	fracBlack := float64(black) / float64(samples)
	fracBlown := float64(blown) / float64(samples)
	variance := sumSquares/float64(samples) - mean*mean
	if variance < 0 {
		variance = 0
	}

	reasons := reasonsFor(mean, fracBlack)
	return Report{
		Pass:      len(reasons) == 0,
		MeanLuma:  mean,
		FracBlack: fracBlack,
		FracBlown: fracBlown,
		Contrast:  math.Sqrt(variance),
		Reasons:   reasons,
	}
}

// AnalyzeFrames loads captured frame images from disk, analyzes each, and returns
// per-frame reports plus an aggregate. Unreadable frames are reported as failed
// reports instead of aborting the whole audit.
func AnalyzeFrames(frames []gametrace.Frame) ([]Report, Report, error) {
	reports := make([]Report, 0, len(frames))
	if len(frames) == 0 {
		return reports, Report{Pass: false, Reasons: []string{"no frames"}}, nil
	}

	aggregate := Report{Pass: true}
	for _, frame := range frames {
		img, err := imgdiff.Load(frame.Path)
		if err != nil {
			report := Report{
				Pass:    false,
				Reasons: []string{fmt.Sprintf("unreadable frame %q: %v", frame.Path, err)},
			}
			reports = append(reports, report)
			mergeAggregate(&aggregate, report)
			continue
		}

		report := Analyze(img)
		reports = append(reports, report)
		mergeAggregate(&aggregate, report)
	}

	n := float64(len(reports))
	aggregate.MeanLuma /= n
	aggregate.FracBlack /= n
	aggregate.FracBlown /= n
	aggregate.Contrast /= n
	return reports, aggregate, nil
}

func mergeAggregate(aggregate *Report, report Report) {
	if !report.Pass {
		aggregate.Pass = false
	}
	aggregate.MeanLuma += report.MeanLuma
	aggregate.FracBlack += report.FracBlack
	aggregate.FracBlown += report.FracBlown
	aggregate.Contrast += report.Contrast
	aggregate.Reasons = append(aggregate.Reasons, report.Reasons...)
}

func reasonsFor(mean, fracBlack float64) []string {
	var reasons []string
	if fracBlack > maxFracBlack {
		reasons = append(reasons, "more than 90% of sampled pixels are black")
	}
	if mean < minMeanLuma {
		reasons = append(reasons, "mean luminance below 0.03")
	}
	if mean > maxMeanLuma {
		reasons = append(reasons, "mean luminance above 0.97")
	}
	return reasons
}

func luma(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
}

func ceilDiv(n, d int) int {
	if n <= 0 {
		return 1
	}
	return (n + d - 1) / d
}
