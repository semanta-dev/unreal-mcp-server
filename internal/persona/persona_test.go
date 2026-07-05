package persona

import (
	"math"
	"strings"
	"testing"
)

func TestAUC(t *testing.T) {
	tests := []struct {
		name   string
		scores []float64
		labels []bool
		want   float64
	}{
		{
			name:   "perfect",
			scores: []float64{0.9, 0.8, 0.2, 0.1},
			labels: []bool{true, true, false, false},
			want:   1.0,
		},
		{
			name:   "inverted",
			scores: []float64{0.1, 0.2, 0.8, 0.9},
			labels: []bool{true, true, false, false},
			want:   0.0,
		},
		{
			name:   "ties",
			scores: []float64{0.5, 0.5, 0.5, 0.5},
			labels: []bool{true, false, true, false},
			want:   0.5,
		},
		{
			name:   "degenerate",
			scores: []float64{0.1, 0.2, 0.3},
			labels: []bool{true, true, true},
			want:   0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AUC(tt.scores, tt.labels)
			if math.IsNaN(got) {
				t.Fatalf("AUC returned NaN")
			}
			if got != tt.want {
				t.Fatalf("AUC() = %.3f, want %.3f", got, tt.want)
			}
		})
	}
}

func TestCalibrateRetentionClearsOnHeldOutHumanMatch(t *testing.T) {
	sessions := []Session{
		{ID: "a", Genre: "tower", HeldOut: true, HumanBoredomMin: 10, HumanSecondSession: false, HumanDelight: false, PredBoredomMin: 10, PredSecondSessionProb: 0.1, PredDelightProb: 0.2},
		{ID: "b", Genre: "tower", HeldOut: true, HumanBoredomMin: 20, HumanSecondSession: true, HumanDelight: false, PredBoredomMin: 20, PredSecondSessionProb: 0.8, PredDelightProb: 0.3},
		{ID: "c", Genre: "tower", HeldOut: true, HumanBoredomMin: 30, HumanSecondSession: true, HumanDelight: true, PredBoredomMin: 30, PredSecondSessionProb: 0.9, PredDelightProb: 0.9},
		{ID: "d", Genre: "tower", HeldOut: false, HumanBoredomMin: 5, HumanSecondSession: false, HumanDelight: false, PredBoredomMin: 99, PredSecondSessionProb: 0.99, PredDelightProb: 0.99},
	}

	results := CalibrateRetention(sessions, 0, 0)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	result := results[0]
	if !result.Cleared {
		t.Fatalf("retention bet did not clear: %+v", result)
	}
	if result.HeldOutN != 3 {
		t.Fatalf("HeldOutN = %d, want 3", result.HeldOutN)
	}
	if result.Metrics["boredom_spearman"] != 1 {
		t.Fatalf("boredom_spearman = %.3f, want 1", result.Metrics["boredom_spearman"])
	}
	if result.Metrics["second_session_auc"] != 1 {
		t.Fatalf("second_session_auc = %.3f, want 1", result.Metrics["second_session_auc"])
	}
	if result.Metrics["delight_auc"] != 1 {
		t.Fatalf("delight_auc = %.3f, want 1", result.Metrics["delight_auc"])
	}
}

func TestCalibrateFeelMissRemainsDiagnostic(t *testing.T) {
	sessions := []Session{
		{ID: "a", Genre: "platformer", HeldOut: true, HumanFeelScore: 1, PredFeelScore: 4},
		{ID: "b", Genre: "platformer", HeldOut: true, HumanFeelScore: 2, PredFeelScore: 3},
		{ID: "c", Genre: "platformer", HeldOut: true, HumanFeelScore: 3, PredFeelScore: 2},
		{ID: "d", Genre: "platformer", HeldOut: true, HumanFeelScore: 4, PredFeelScore: 1},
	}

	report := Calibrate(sessions, 0, 0)
	if contains(report.ClearedLegs, "feel-quality/platformer/feel_spearman") {
		t.Fatalf("feel-quality leg unexpectedly cleared: %+v", report)
	}
	if !contains(report.DiagnosticLegs, "feel-quality/platformer/feel_spearman") {
		t.Fatalf("feel-quality leg missing from diagnostics: %+v", report)
	}

	var feel BetResult
	for _, result := range report.Bets {
		if result.Bet == feelBet && result.Genre == "platformer" {
			feel = result
		}
	}
	if feel.Cleared {
		t.Fatalf("feel-quality unexpectedly cleared: %+v", feel)
	}
	if feel.Metrics["feel_spearman"] >= 0.7 {
		t.Fatalf("feel_spearman = %.3f, want below threshold", feel.Metrics["feel_spearman"])
	}
	if !strings.Contains(feel.Reason, "hardest bet") {
		t.Fatalf("Reason %q does not mention hardest bet", feel.Reason)
	}
}

func TestThinHeldOutNeverClears(t *testing.T) {
	sessions := []Session{
		{ID: "a", Genre: "roguelite", HeldOut: true, HumanBoredomMin: 10, HumanSecondSession: false, HumanDelight: false, HumanFeelScore: 1, PredBoredomMin: 10, PredSecondSessionProb: 0.1, PredDelightProb: 0.1, PredFeelScore: 1},
		{ID: "b", Genre: "roguelite", HeldOut: true, HumanBoredomMin: 20, HumanSecondSession: true, HumanDelight: true, HumanFeelScore: 2, PredBoredomMin: 20, PredSecondSessionProb: 0.9, PredDelightProb: 0.9, PredFeelScore: 2},
	}

	report := Calibrate(sessions, 0, 0)
	if len(report.ClearedLegs) != 0 {
		t.Fatalf("thin data produced cleared legs: %+v", report.ClearedLegs)
	}
	for _, result := range report.Bets {
		if result.Cleared {
			t.Fatalf("thin data result cleared: %+v", result)
		}
		if !strings.Contains(result.Reason, "insufficient held-out N: 2 < 3") {
			t.Fatalf("Reason %q does not explain thin data", result.Reason)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
