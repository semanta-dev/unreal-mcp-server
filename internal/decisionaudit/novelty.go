package decisionaudit

import (
	"fmt"
	"sort"

	"github.com/jdziat/unreal-mcp-server/internal/gametrace"
)

const defaultMaxDeadStretch = 120.0

// NoveltyReport summarizes the session novelty cadence and flags RC10b dead
// stretches where no new elements appear for too long. It also checks adherence to
// the AUTHORED new-element schedule (§6.1b, §7.1): a build that promised N reveals
// but shipped fewer is flagged even if it has no dead stretch.
type NoveltyReport struct {
	Pass               bool
	NewElementCount    int
	LongestDeadStretch float64
	DeadStretchStart   float64
	ScheduledCount     int      // distinct elements the authored schedule promised
	DeliveredCount     int      // scheduled elements actually observed in the session
	MissingElements    []string // scheduled element keys never observed
	Reasons            []string
}

// NoveltyAuditDefault applies the Phase-0 default dead-stretch threshold of 120s.
func NoveltyAuditDefault(tr gametrace.SessionTrace) NoveltyReport {
	return NoveltyAudit(tr, defaultMaxDeadStretch)
}

// NoveltyAudit checks the largest gap between ordered first appearances,
// including the opening 0->first gap and final last->Duration gap.
func NoveltyAudit(tr gametrace.SessionTrace, maxDeadStretch float64) NoveltyReport {
	duration := tr.Duration
	if duration < 0 {
		duration = 0
	}

	times := firstAppearanceTimes(tr.Observed)
	report := NoveltyReport{NewElementCount: len(times)}

	prev := 0.0
	for _, t := range times {
		t = clampTime(t, duration)
		if gap := t - prev; gap > report.LongestDeadStretch {
			report.LongestDeadStretch = gap
			report.DeadStretchStart = prev
		}
		prev = t
	}
	if gap := duration - prev; gap > report.LongestDeadStretch {
		report.LongestDeadStretch = gap
		report.DeadStretchStart = prev
	}

	if report.LongestDeadStretch > maxDeadStretch {
		report.Reasons = append(report.Reasons, fmt.Sprintf("longest novelty dead stretch %.1fs exceeds %.1fs", report.LongestDeadStretch, maxDeadStretch))
	}

	// Schedule adherence: every element the authored schedule promised must actually
	// appear in the observed session (matched by Kind+ID). A promised-but-undelivered
	// reveal is a novelty defect distinct from a dead stretch.
	report.ScheduledCount, report.DeliveredCount, report.MissingElements = scheduleAdherence(tr)
	if report.ScheduledCount > 0 && report.DeliveredCount < report.ScheduledCount {
		report.Reasons = append(report.Reasons, fmt.Sprintf("authored schedule delivered %d/%d elements; missing: %v",
			report.DeliveredCount, report.ScheduledCount, report.MissingElements))
	}

	report.Pass = len(report.Reasons) == 0
	return report
}

// scheduleAdherence matches the authored schedule against the observed elements by
// Kind+ID and returns (scheduled, delivered, missingKeys).
func scheduleAdherence(tr gametrace.SessionTrace) (scheduled, delivered int, missing []string) {
	observed := make(map[string]bool, len(tr.Observed))
	for i, el := range tr.Observed {
		observed[noveltyKey(el, i)] = true
	}
	seenScheduled := make(map[string]bool, len(tr.Schedule))
	for i, el := range tr.Schedule {
		key := noveltyKey(el, i)
		if seenScheduled[key] {
			continue
		}
		seenScheduled[key] = true
		scheduled++
		if observed[key] {
			delivered++
		} else {
			missing = append(missing, key)
		}
	}
	return scheduled, delivered, missing
}

func firstAppearanceTimes(observed []gametrace.NewElement) []float64 {
	firstByElement := make(map[string]float64)
	for i, el := range observed {
		key := noveltyKey(el, i)
		t, ok := firstByElement[key]
		if !ok || el.T < t {
			firstByElement[key] = el.T
		}
	}

	times := make([]float64, 0, len(firstByElement))
	for _, t := range firstByElement {
		times = append(times, t)
	}
	sort.Float64s(times)
	return times
}

func noveltyKey(el gametrace.NewElement, fallback int) string {
	if el.ID != "" {
		return el.Kind + "\x00" + el.ID
	}
	return fmt.Sprintf("%s\x00%d", el.Kind, fallback)
}

func clampTime(t, duration float64) float64 {
	switch {
	case t < 0:
		return 0
	case t > duration:
		return duration
	default:
		return t
	}
}
