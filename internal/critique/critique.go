// Package critique implements the Phase-4 calibration gate for trusted critic
// dimensions described in AGENTIC_GAMEDEV_PLAN.md sections 7.4, 7.4c, and 7.5.
// It scores already-produced human labels and critic scores, then reports which
// per-genre dimensions may gate quality and which must remain diagnostic.
package critique

import (
	"fmt"
	"math"
	"sort"
)

const defaultThreshold = 0.7

// Sample is one human-label/critic-score pair for a scene dimension.
type Sample struct {
	ID          string
	Genre       string
	Dimension   string
	HumanScore  float64
	CriticScore float64
	HeldOut     bool
}

// DimensionResult summarizes the calibration outcome for one genre/dimension.
type DimensionResult struct {
	Genre             string  `json:"genre"`
	Dimension         string  `json:"dimension"`
	N                 int     `json:"n"`
	HeldOutN          int     `json:"held_out_n"`
	Spearman          float64 `json:"spearman"`
	PairwiseAgreement float64 `json:"pairwise_agreement"`
	Cleared           bool    `json:"cleared"`
	Reason            string  `json:"reason,omitempty"`
}

// Report is the marshalable calibration artifact for the trusted set.
type Report struct {
	// Version stamps the calibration schema so a persisted Saved/Critiques/calibration.json
	// (the plan's versioned, logged trusted-set artifact, §7.4d) is self-describing.
	Version    int               `json:"version"`
	Threshold  float64           `json:"threshold"`
	Dimensions []DimensionResult `json:"dimensions"`
	Trusted    []string          `json:"trusted"`
	Diagnostic []string          `json:"diagnostic"`
}

// CalibrationVersion is the schema version stamped into Report.Version.
const CalibrationVersion = 1

// Spearman returns the Spearman rank correlation of x and y. Values are ranked
// independently with average ranks for ties, then Pearson correlation is applied
// to those ranks. It returns 0 for unequal lengths, len < 2, or zero variance.
func Spearman(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return 0
	}
	return pearson(rank(x), rank(y))
}

// PairwiseAgreement returns the fraction of non-tied human pairs whose critic
// ordering has the same sign as the human ordering.
func PairwiseAgreement(human, critic []float64) float64 {
	if len(human) != len(critic) || len(human) < 2 {
		return 0
	}
	var agree, total int
	for i := 0; i < len(human); i++ {
		for j := i + 1; j < len(human); j++ {
			h := sign(human[i] - human[j])
			if h == 0 {
				continue
			}
			total++
			if sign(critic[i]-critic[j]) == h {
				agree++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(agree) / float64(total)
}

// CalibrateDimension evaluates one genre/dimension group on its held-out subset.
func CalibrateDimension(samples []Sample, threshold float64) DimensionResult {
	r := DimensionResult{N: len(samples)}
	if len(samples) > 0 {
		r.Genre = samples[0].Genre
		r.Dimension = samples[0].Dimension
	}

	var human, critic []float64
	for _, s := range samples {
		if !s.HeldOut {
			continue
		}
		human = append(human, s.HumanScore)
		critic = append(critic, s.CriticScore)
	}
	r.HeldOutN = len(human)
	r.Spearman = Spearman(human, critic)
	r.PairwiseAgreement = PairwiseAgreement(human, critic)

	switch {
	case r.HeldOutN < 2:
		r.Reason = fmt.Sprintf("insufficient held-out N: %d < 2", r.HeldOutN)
	case r.Spearman < threshold:
		r.Reason = fmt.Sprintf("spearman %.2f < %.2f", r.Spearman, threshold)
	default:
		r.Cleared = true
		r.Reason = fmt.Sprintf("spearman %.2f >= %.2f", r.Spearman, threshold)
	}
	return r
}

// Calibrate groups samples by genre/dimension and returns the trusted set.
func Calibrate(samples []Sample, threshold float64) Report {
	if threshold <= 0 {
		threshold = defaultThreshold
	}

	groups := make(map[string][]Sample)
	for _, s := range samples {
		groups[groupKey(s.Genre, s.Dimension)] = append(groups[groupKey(s.Genre, s.Dimension)], s)
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	report := Report{Version: CalibrationVersion, Threshold: threshold}
	for _, key := range keys {
		result := CalibrateDimension(groups[key], threshold)
		report.Dimensions = append(report.Dimensions, result)
		if result.Cleared {
			report.Trusted = append(report.Trusted, key)
		} else {
			report.Diagnostic = append(report.Diagnostic, key)
		}
	}
	sort.Strings(report.Trusted)
	sort.Strings(report.Diagnostic)
	return report
}

// ShouldAbstain reports whether repeated critic scores disagree too much.
func ShouldAbstain(scores []float64, maxStdDev float64) bool {
	if len(scores) < 2 {
		return false
	}
	mean := 0.0
	for _, score := range scores {
		mean += score
	}
	mean /= float64(len(scores))

	variance := 0.0
	for _, score := range scores {
		d := score - mean
		variance += d * d
	}
	variance /= float64(len(scores))
	return math.Sqrt(variance) > maxStdDev
}

func groupKey(genre, dimension string) string {
	return genre + "/" + dimension
}

func rank(values []float64) []float64 {
	type item struct {
		value float64
		index int
	}
	items := make([]item, len(values))
	for i, value := range values {
		items[i] = item{value: value, index: i}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].value < items[j].value
	})

	ranks := make([]float64, len(values))
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && items[j].value == items[i].value {
			j++
		}
		avg := (float64(i+1) + float64(j)) / 2
		for k := i; k < j; k++ {
			ranks[items[k].index] = avg
		}
		i = j
	}
	return ranks
}

func pearson(x, y []float64) float64 {
	meanX, meanY := mean(x), mean(y)
	var ssX, ssY, cov float64
	for i := range x {
		dx := x[i] - meanX
		dy := y[i] - meanY
		ssX += dx * dx
		ssY += dy * dy
		cov += dx * dy
	}
	if ssX == 0 || ssY == 0 {
		return 0
	}
	return cov / math.Sqrt(ssX*ssY)
}

func mean(values []float64) float64 {
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}
