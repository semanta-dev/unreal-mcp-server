// persona (merged into package calibration) implements the Phase-4 player-persona calibration harness
// described in AGENTIC_GAMEDEV_PLAN.md sections 7.4b and 7.4c. It scores
// persona predictions against held-out known human outcomes, and reports which
// per-genre retention/delight and feel-quality legs may gate quality.
package calibration

import (
	"fmt"
	"sort"

)

const (
	defaultSpearmanThreshold = 0.7
	defaultAUCThreshold      = 0.7

	retentionBet = "retention-delight"
	feelBet      = "feel-quality"
)

// Session is one calibrated human-vs-persona play report.
type Session struct {
	ID    string
	Genre string

	HeldOut bool

	HumanBoredomMin    float64
	HumanSecondSession bool
	HumanDelight       bool
	HumanFeelScore     float64

	PredBoredomMin        float64
	PredSecondSessionProb float64
	PredDelightProb       float64
	PredFeelScore         float64
}

// BetResult summarizes one separately-gated persona bet for a genre.
type BetResult struct {
	Bet      string             `json:"bet"`
	Genre    string             `json:"genre"`
	N        int                `json:"n"`
	HeldOutN int                `json:"held_out_n"`
	Metrics  map[string]float64 `json:"metrics"`
	Cleared  bool               `json:"cleared"`
	Reason   string             `json:"reason,omitempty"`
}

// PersonaReport is the persona calibration artifact. Cleared legs may gate the
// in-loop gradient; diagnostic legs remain carried by the permanent human pulse.
type PersonaReport struct {
	// Version stamps the persona-calibration schema (§7.4c) so a persisted trusted-set
	// artifact is self-describing, mirroring CriticCalibrationVersion.
	Version        int         `json:"version"`
	Bets           []BetResult `json:"bets"`
	ClearedLegs    []string    `json:"cleared_legs"`
	DiagnosticLegs []string    `json:"diagnostic_legs"`
}

// PersonaCalibrationVersion is the schema version stamped into PersonaReport.Version.
const PersonaCalibrationVersion = 1

// AUC returns ROC AUC using the Mann-Whitney U pair form. Ties count as 0.5.
// Degenerate all-positive or all-negative inputs return 0.5.
func AUC(scores []float64, labels []bool) float64 {
	if len(scores) != len(labels) || len(scores) == 0 {
		return 0.5
	}

	var correct, pairs float64
	for i, labelI := range labels {
		if !labelI {
			continue
		}
		for j, labelJ := range labels {
			if labelJ {
				continue
			}
			pairs++
			switch {
			case scores[i] > scores[j]:
				correct++
			case scores[i] == scores[j]:
				correct += 0.5
			default:
			}
		}
	}
	if pairs == 0 {
		return 0.5
	}
	return correct / pairs
}

// CalibrateRetention evaluates Bet 1 per genre on held-out sessions:
// boredom-point Spearman, second-session AUC, and delight-event AUC.
func CalibrateRetention(sessions []Session, spearmanThresh, aucThresh float64) []BetResult {
	if spearmanThresh <= 0 {
		spearmanThresh = defaultSpearmanThreshold
	}
	if aucThresh <= 0 {
		aucThresh = defaultAUCThreshold
	}

	var results []BetResult
	for _, genre := range sortedGenres(sessions) {
		group := sessionsForGenre(sessions, genre)
		result := BetResult{
			Bet:     retentionBet,
			Genre:   genre,
			N:       len(group),
			Metrics: make(map[string]float64),
		}

		var humanBoredom, predBoredom, secondProb, delightProb []float64
		var secondLabels, delightLabels []bool
		for _, session := range group {
			if !session.HeldOut {
				continue
			}
			humanBoredom = append(humanBoredom, session.HumanBoredomMin)
			predBoredom = append(predBoredom, session.PredBoredomMin)
			secondProb = append(secondProb, session.PredSecondSessionProb)
			secondLabels = append(secondLabels, session.HumanSecondSession)
			delightProb = append(delightProb, session.PredDelightProb)
			delightLabels = append(delightLabels, session.HumanDelight)
		}

		result.HeldOutN = len(humanBoredom)
		result.Metrics["boredom_spearman"] = Spearman(predBoredom, humanBoredom)
		result.Metrics["second_session_auc"] = AUC(secondProb, secondLabels)
		result.Metrics["delight_auc"] = AUC(delightProb, delightLabels)
		result.Cleared, result.Reason = retentionReason(result, spearmanThresh, aucThresh)
		results = append(results, result)
	}
	return results
}

// CalibrateFeel evaluates Bet 2 per genre on held-out sessions: the persona's
// felt-quality ordering against human FELT labels.
func CalibrateFeel(sessions []Session, spearmanThresh float64) []BetResult {
	if spearmanThresh <= 0 {
		spearmanThresh = defaultSpearmanThreshold
	}

	var results []BetResult
	for _, genre := range sortedGenres(sessions) {
		group := sessionsForGenre(sessions, genre)
		result := BetResult{
			Bet:     feelBet,
			Genre:   genre,
			N:       len(group),
			Metrics: make(map[string]float64),
		}

		var humanFeel, predFeel []float64
		for _, session := range group {
			if !session.HeldOut {
				continue
			}
			humanFeel = append(humanFeel, session.HumanFeelScore)
			predFeel = append(predFeel, session.PredFeelScore)
		}

		result.HeldOutN = len(humanFeel)
		result.Metrics["feel_spearman"] = Spearman(predFeel, humanFeel)
		result.Cleared, result.Reason = feelReason(result, spearmanThresh)
		results = append(results, result)
	}
	return results
}

// CalibratePersona runs both persona bets and assembles cleared and diagnostic legs.
func CalibratePersona(sessions []Session, spearmanThresh, aucThresh float64) PersonaReport {
	var report PersonaReport
	report.Version = PersonaCalibrationVersion
	report.Bets = append(report.Bets, CalibrateRetention(sessions, spearmanThresh, aucThresh)...)
	report.Bets = append(report.Bets, CalibrateFeel(sessions, spearmanThresh)...)

	for _, bet := range report.Bets {
		for _, metric := range sortedMetricNames(bet.Metrics) {
			leg := bet.Bet + "/" + bet.Genre + "/" + metric
			if bet.Cleared {
				report.ClearedLegs = append(report.ClearedLegs, leg)
			} else {
				report.DiagnosticLegs = append(report.DiagnosticLegs, leg)
			}
		}
	}
	sort.Strings(report.ClearedLegs)
	sort.Strings(report.DiagnosticLegs)
	return report
}

func retentionReason(result BetResult, spearmanThresh, aucThresh float64) (bool, string) {
	if result.HeldOutN < 3 {
		return false, fmt.Sprintf("insufficient held-out N: %d < 3; retention/delight remains diagnostic", result.HeldOutN)
	}
	boredom := result.Metrics["boredom_spearman"]
	second := result.Metrics["second_session_auc"]
	delight := result.Metrics["delight_auc"]
	if boredom < spearmanThresh {
		return false, fmt.Sprintf("boredom_spearman %.2f < %.2f; retention/delight remains diagnostic", boredom, spearmanThresh)
	}
	if second < aucThresh {
		return false, fmt.Sprintf("second_session_auc %.2f < %.2f; retention/delight remains diagnostic", second, aucThresh)
	}
	if delight < aucThresh {
		return false, fmt.Sprintf("delight_auc %.2f < %.2f; retention/delight remains diagnostic", delight, aucThresh)
	}
	return true, fmt.Sprintf("all retention/delight metrics cleared on held-out N=%d", result.HeldOutN)
}

func feelReason(result BetResult, spearmanThresh float64) (bool, string) {
	if result.HeldOutN < 3 {
		return false, fmt.Sprintf("insufficient held-out N: %d < 3; feel-quality is the hardest bet and remains diagnostic", result.HeldOutN)
	}
	feel := result.Metrics["feel_spearman"]
	if feel < spearmanThresh {
		return false, fmt.Sprintf("feel_spearman %.2f < %.2f; feel-quality is the hardest bet and remains diagnostic", feel, spearmanThresh)
	}
	return true, fmt.Sprintf("feel-quality cleared despite being the hardest bet, on held-out N=%d", result.HeldOutN)
}

func sortedGenres(sessions []Session) []string {
	seen := make(map[string]bool)
	for _, session := range sessions {
		seen[session.Genre] = true
	}
	genres := make([]string, 0, len(seen))
	for genre := range seen {
		genres = append(genres, genre)
	}
	sort.Strings(genres)
	return genres
}

func sessionsForGenre(sessions []Session, genre string) []Session {
	var group []Session
	for _, session := range sessions {
		if session.Genre == genre {
			group = append(group, session)
		}
	}
	return group
}

func sortedMetricNames(metrics map[string]float64) []string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
