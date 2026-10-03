package calibration

import (
	"math"
	"strings"
	"testing"
)

func TestCalibrateTrustsMonotonicHeldOutDimension(t *testing.T) {
	samples := []Sample{
		{ID: "a", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 1, CriticScore: 0.2, HeldOut: true},
		{ID: "b", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 2, CriticScore: 0.4, HeldOut: true},
		{ID: "c", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 3, CriticScore: 0.6, HeldOut: true},
		{ID: "d", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 4, CriticScore: 0.8, HeldOut: true},
		{ID: "e", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 5, CriticScore: 1.0, HeldOut: true},
		{ID: "f", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 6, CriticScore: 1.2, HeldOut: true},
		{ID: "train", Genre: "tower-defense", Dimension: "design_quality", HumanScore: 1, CriticScore: -10, HeldOut: false},
	}

	report := CalibrateCritic(samples, 0)
	if got, want := report.Threshold, 0.7; got != want {
		t.Fatalf("threshold = %g, want %g", got, want)
	}
	if len(report.Trusted) != 1 || report.Trusted[0] != "tower-defense/design_quality" {
		t.Fatalf("trusted = %#v, want tower-defense/design_quality", report.Trusted)
	}
	if len(report.Dimensions) != 1 || !report.Dimensions[0].Cleared {
		t.Fatalf("dimension did not clear: %+v", report.Dimensions)
	}
	if report.Dimensions[0].HeldOutN != 6 {
		t.Fatalf("held-out N = %d, want 6", report.Dimensions[0].HeldOutN)
	}
	if report.Dimensions[0].Spearman < 0.7 {
		t.Fatalf("spearman = %g, want >= 0.7", report.Dimensions[0].Spearman)
	}
}

func TestCalibrateRejectsInvertedHeldOutDimension(t *testing.T) {
	samples := []Sample{
		{ID: "a", Genre: "tower-defense", Dimension: "feel_quality", HumanScore: 1, CriticScore: 6, HeldOut: true},
		{ID: "b", Genre: "tower-defense", Dimension: "feel_quality", HumanScore: 2, CriticScore: 5, HeldOut: true},
		{ID: "c", Genre: "tower-defense", Dimension: "feel_quality", HumanScore: 3, CriticScore: 4, HeldOut: true},
		{ID: "d", Genre: "tower-defense", Dimension: "feel_quality", HumanScore: 4, CriticScore: 3, HeldOut: true},
		{ID: "e", Genre: "tower-defense", Dimension: "feel_quality", HumanScore: 5, CriticScore: 2, HeldOut: true},
		{ID: "f", Genre: "tower-defense", Dimension: "feel_quality", HumanScore: 6, CriticScore: 1, HeldOut: true},
	}

	report := CalibrateCritic(samples, 0.7)
	if len(report.Trusted) != 0 {
		t.Fatalf("trusted = %#v, want none", report.Trusted)
	}
	if len(report.Diagnostic) != 1 || report.Diagnostic[0] != "tower-defense/feel_quality" {
		t.Fatalf("diagnostic = %#v, want tower-defense/feel_quality", report.Diagnostic)
	}
	result := report.Dimensions[0]
	if result.Cleared {
		t.Fatalf("inverted critic cleared: %+v", result)
	}
	if result.Spearman >= 0 {
		t.Fatalf("spearman = %g, want negative", result.Spearman)
	}
	if !strings.Contains(result.Reason, "spearman") {
		t.Fatalf("reason = %q, want spearman failure", result.Reason)
	}
}

func TestSpearmanPerfectInvertedAndTies(t *testing.T) {
	if got := Spearman([]float64{1, 2, 3, 4}, []float64{10, 20, 30, 40}); !near(got, 1) {
		t.Fatalf("perfect spearman = %g, want 1", got)
	}
	if got := Spearman([]float64{1, 2, 3, 4}, []float64{40, 30, 20, 10}); !near(got, -1) {
		t.Fatalf("inverted spearman = %g, want -1", got)
	}
	if got := Spearman([]float64{1, 1, 2, 3}, []float64{2, 2, 4, 8}); math.IsNaN(got) {
		t.Fatal("tie spearman returned NaN")
	}
	if got := Spearman([]float64{1, 1, 1}, []float64{3, 2, 1}); got != 0 {
		t.Fatalf("zero-variance spearman = %g, want 0", got)
	}
}

func TestShouldAbstain(t *testing.T) {
	if !ShouldAbstain([]float64{0.1, 0.9, 0.2, 0.95}, 0.2) {
		t.Fatal("high-dispersion scores should abstain")
	}
	if ShouldAbstain([]float64{0.70, 0.72, 0.69, 0.71}, 0.05) {
		t.Fatal("tight scores should not abstain")
	}
	if ShouldAbstain([]float64{0.7}, 0.01) {
		t.Fatal("single score should not abstain")
	}
}

func TestInsufficientHeldOutIsDiagnostic(t *testing.T) {
	report := CalibrateCritic([]Sample{
		{ID: "a", Genre: "action", Dimension: "through_line", HumanScore: 1, CriticScore: 1, HeldOut: true},
		{ID: "train", Genre: "action", Dimension: "through_line", HumanScore: 2, CriticScore: 2, HeldOut: false},
	}, 0.7)

	if len(report.Trusted) != 0 {
		t.Fatalf("trusted = %#v, want none", report.Trusted)
	}
	if len(report.Diagnostic) != 1 || report.Diagnostic[0] != "action/through_line" {
		t.Fatalf("diagnostic = %#v, want action/through_line", report.Diagnostic)
	}
	result := report.Dimensions[0]
	if result.Cleared {
		t.Fatalf("insufficient held-out dimension cleared: %+v", result)
	}
	if result.HeldOutN != 1 {
		t.Fatalf("held-out N = %d, want 1", result.HeldOutN)
	}
	if !strings.Contains(result.Reason, "insufficient held-out N") {
		t.Fatalf("reason = %q, want clear insufficient-N reason", result.Reason)
	}
}

func near(got, want float64) bool {
	return math.Abs(got-want) < 1e-12
}
