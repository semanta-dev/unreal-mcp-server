// stylecohesion (merged into package visual) implements the style_cohesion Layer-A audit from
// AGENTIC_GAMEDEV_PLAN.md §4, P0 pixel variant only: it detects incompatible
// style clusters from captured-frame pixel statistics without asset tags.
package visual

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
)

const (
	gridSize               = 8
	splitFractionThreshold = 0.25
	minVarianceGap         = 0.035
	kMeansIterations       = 8
)

// StyleReport is the style-cohesion audit result for one frame or a frame set.
type StyleReport struct {
	Pass          bool
	ClusterCount  int
	SplitFraction float64
	Reasons       []string
}

type regionFeature struct {
	mean     float64
	variance float64
	pixels   int
}

// AnalyzeStyle partitions img into an 8x8 region grid, computes per-region mean styleLuma
// and styleLuma variance, then clusters regions with 2-means on the variance axis.
// The variance split is intentionally narrow for P0: flat low-poly regions and
// high-frequency textured/PBR regions read as distinct texture-variance clusters.
func AnalyzeStyle(img image.Image) StyleReport {
	b := img.Bounds()
	if b.Empty() {
		return StyleReport{Pass: false, Reasons: []string{"empty image"}}
	}

	features := regionFeatures(img)
	lowWeight, highWeight, lowMean, highMean := clusterByVariance(features)
	totalPixels := b.Dx() * b.Dy()
	if totalPixels == 0 {
		return StyleReport{Pass: false, Reasons: []string{"empty image"}}
	}

	gap := math.Abs(highMean - lowMean)
	smallerWeight := minInt(lowWeight, highWeight)
	splitFraction := float64(smallerWeight) / float64(totalPixels)
	clusterCount := 1
	if lowWeight > 0 && highWeight > 0 && gap >= minVarianceGap {
		clusterCount = 2
	}

	report := StyleReport{
		Pass:          true,
		ClusterCount:  clusterCount,
		SplitFraction: splitFraction,
	}
	if clusterCount >= 2 && splitFraction > splitFractionThreshold {
		report.Pass = false
		report.Reasons = append(report.Reasons, fmt.Sprintf(
			"incompatible texture-variance clusters cover %.2f of frame with variance gap %.3f",
			splitFraction, gap,
		))
	}
	return report
}

// AnalyzeStyleFrames loads each captured frame path and returns per-frame reports plus
// an aggregate report that fails if any frame fails.
func AnalyzeStyleFrames(frames []audit.Frame) ([]StyleReport, StyleReport, error) {
	reports := make([]StyleReport, 0, len(frames))
	aggregate := StyleReport{Pass: true}
	for _, frame := range frames {
		img, err := Load(frame.Path)
		if err != nil {
			return nil, StyleReport{}, err
		}
		report := AnalyzeStyle(img)
		reports = append(reports, report)

		if !report.Pass {
			aggregate.Pass = false
		}
		if report.ClusterCount > aggregate.ClusterCount {
			aggregate.ClusterCount = report.ClusterCount
		}
		if report.SplitFraction > aggregate.SplitFraction {
			aggregate.SplitFraction = report.SplitFraction
		}
		aggregate.Reasons = append(aggregate.Reasons, report.Reasons...)
	}
	if len(frames) == 0 {
		aggregate.ClusterCount = 0
	}
	return reports, aggregate, nil
}

func regionFeatures(img image.Image) []regionFeature {
	b := img.Bounds()
	features := make([]regionFeature, 0, gridSize*gridSize)
	for gy := 0; gy < gridSize; gy++ {
		y0 := b.Min.Y + gy*b.Dy()/gridSize
		y1 := b.Min.Y + (gy+1)*b.Dy()/gridSize
		for gx := 0; gx < gridSize; gx++ {
			x0 := b.Min.X + gx*b.Dx()/gridSize
			x1 := b.Min.X + (gx+1)*b.Dx()/gridSize
			features = append(features, computeFeature(img, x0, y0, x1, y1))
		}
	}
	return features
}

func computeFeature(img image.Image, x0, y0, x1, y1 int) regionFeature {
	var sum, sumSquares float64
	var pixels int
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			l := styleLuma(img.At(x, y))
			sum += l
			sumSquares += l * l
			pixels++
		}
	}
	if pixels == 0 {
		return regionFeature{}
	}
	mean := sum / float64(pixels)
	variance := sumSquares/float64(pixels) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return regionFeature{mean: mean, variance: variance, pixels: pixels}
}

func clusterByVariance(features []regionFeature) (lowWeight, highWeight int, lowMean, highMean float64) {
	if len(features) == 0 {
		return 0, 0, 0, 0
	}

	lowMean, highMean = features[0].variance, features[0].variance
	for _, feature := range features[1:] {
		if feature.variance < lowMean {
			lowMean = feature.variance
		}
		if feature.variance > highMean {
			highMean = feature.variance
		}
	}

	var lowSum, highSum float64
	for i := 0; i < kMeansIterations; i++ {
		lowWeight, highWeight = 0, 0
		lowSum, highSum = 0, 0
		for _, feature := range features {
			if math.Abs(feature.variance-lowMean) <= math.Abs(feature.variance-highMean) {
				lowWeight += feature.pixels
				lowSum += feature.variance * float64(feature.pixels)
			} else {
				highWeight += feature.pixels
				highSum += feature.variance * float64(feature.pixels)
			}
		}
		if lowWeight > 0 {
			lowMean = lowSum / float64(lowWeight)
		}
		if highWeight > 0 {
			highMean = highSum / float64(highWeight)
		}
	}

	if lowMean > highMean {
		return highWeight, lowWeight, highMean, lowMean
	}
	return lowWeight, highWeight, lowMean, highMean
}

func styleLuma(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
