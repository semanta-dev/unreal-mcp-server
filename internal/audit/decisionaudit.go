// decisionaudit (merged into package audit) implements the deterministic decision_audit and
// novelty_audit floor checks described in AGENTIC_GAMEDEV_PLAN.md section 6.1b
// and surfaced as Layer-A design-quality audits in section 7.1.
package audit

import (
	"math"
	"sort"
)

const (
	minCadenceEntropy  = 0.35
	minDecisionDensity = 1.5
)

// DecisionReport summarizes whether an observed decision trace has real choices
// and avoids the RC10a metronome failure mode.
type DecisionReport struct {
	Pass            bool
	DecisionDensity float64
	CadenceEntropy  float64
	ActionSkew      float64
	Reasons         []string
}

// DecisionAudit scores a trace of observed decision points.
func DecisionAudit(pts []DecisionPoint) DecisionReport {
	if len(pts) == 0 {
		return DecisionReport{
			Pass:    false,
			Reasons: []string{"no decision points observed"},
		}
	}

	ordered := append([]DecisionPoint(nil), pts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].T < ordered[j].T
	})

	density := decisionDensity(ordered)
	actionCounts := make(map[string]int)
	chosen := make([]string, 0, len(ordered))
	for _, pt := range ordered {
		if pt.Chosen == "" {
			continue
		}
		chosen = append(chosen, pt.Chosen)
		actionCounts[pt.Chosen]++
	}

	report := DecisionReport{
		DecisionDensity: density,
		CadenceEntropy:  cadenceEntropy(ordered, chosen),
		ActionSkew:      actionSkew(actionCounts, len(ordered)),
	}

	if report.CadenceEntropy < minCadenceEntropy {
		report.Reasons = append(report.Reasons, "cadence entropy below 0.35: observed choices read as a metronome")
	}
	if report.DecisionDensity < minDecisionDensity {
		report.Reasons = append(report.Reasons, "decision density below 1.5: too few distinct viable actions per decision point")
	}
	report.Pass = len(report.Reasons) == 0
	return report
}

func decisionDensity(pts []DecisionPoint) float64 {
	total := 0
	for _, pt := range pts {
		total += len(distinctNonEmpty(pt.Available))
	}
	return float64(total) / float64(len(pts))
}

// cadenceEntropy combines action variety and timing variety:
// score = 0.7*H_norm(chosen_actions) + 0.3*(CV(gaps)/(1+CV(gaps))).
// H_norm is Shannon entropy divided by log(number of distinct chosen actions),
// yielding [0,1]. CV is the coefficient of variation of inter-decision time
// gaps, mapped to [0,1). A fixed-period single-action loop therefore scores 0,
// while varied choices at irregular intervals score high.
func cadenceEntropy(pts []DecisionPoint, chosen []string) float64 {
	actionEntropy := normalizedShannon(chosen)
	gapIrregularity := interDecisionIrregularity(pts)
	return clamp01(0.7*actionEntropy + 0.3*gapIrregularity)
}

func normalizedShannon(values []string) float64 {
	counts := make(map[string]int)
	for _, value := range values {
		if value != "" {
			counts[value]++
		}
	}
	if len(counts) <= 1 {
		return 0
	}

	var h float64
	for _, count := range counts {
		p := float64(count) / float64(len(values))
		h -= p * math.Log(p)
	}
	return clamp01(h / math.Log(float64(len(counts))))
}

func interDecisionIrregularity(pts []DecisionPoint) float64 {
	if len(pts) < 3 {
		return 0
	}

	gaps := make([]float64, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		gap := pts[i].T - pts[i-1].T
		if gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) < 2 {
		return 0
	}

	var sum float64
	for _, gap := range gaps {
		sum += gap
	}
	mean := sum / float64(len(gaps))
	if mean == 0 {
		return 0
	}

	var variance float64
	for _, gap := range gaps {
		d := gap - mean
		variance += d * d
	}
	cv := math.Sqrt(variance/float64(len(gaps))) / mean
	return clamp01(cv / (1 + cv))
}

func actionSkew(counts map[string]int, total int) float64 {
	if total == 0 {
		return 0
	}
	maxCount := 0
	for _, count := range counts {
		if count > maxCount {
			maxCount = count
		}
	}
	return float64(maxCount) / float64(total)
}

func distinctNonEmpty(values []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, value := range values {
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
