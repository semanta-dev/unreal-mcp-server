// audioaudit (merged into package audit) implements the audio_audit Layer-A audit from
// AGENTIC_GAMEDEV_PLAN.md sections 6.3 and 7.1, checking captured audio tracks
// so RC9 silent builds cannot pass as playable games.
package audit

import (
	"fmt"
	"math"

)

const (
	defaultSilenceRMS = 0.01
	defaultWindowMs   = 120.0
)

// AudioReport is the audio audit result.
type AudioReport struct {
	Pass          bool
	NotSilent     bool
	EventsCovered int
	EventsTotal   int
	Reasons       []string
}

// AudioAuditDefault runs AudioAudit with the default silence threshold and
// event-covering window.
func AudioAuditDefault(track []AudioSample, events []Event) AudioReport {
	return AudioAudit(track, events, defaultSilenceRMS, defaultWindowMs)
}

// AudioAudit checks whether the captured audio track is non-silent and whether
// every gameplay event has an audible RMS sample within +/- windowMs.
func AudioAudit(track []AudioSample, events []Event, silenceRMS float64, windowMs float64) AudioReport {
	report := AudioReport{
		EventsTotal: len(events),
	}

	for _, sample := range track {
		if sample.RMS > silenceRMS {
			report.NotSilent = true
			break
		}
	}

	windowSeconds := windowMs / 1000.0
	for _, event := range events {
		if eventCovered(track, event, silenceRMS, windowSeconds) {
			report.EventsCovered++
		}
	}

	report.Pass = report.NotSilent && (report.EventsTotal == 0 || report.EventsCovered == report.EventsTotal)
	if !report.NotSilent {
		report.Reasons = append(report.Reasons, "audio track is silent")
	}
	if report.EventsCovered != report.EventsTotal {
		report.Reasons = append(report.Reasons, fmt.Sprintf("audible samples cover %d/%d events", report.EventsCovered, report.EventsTotal))
	}

	return report
}

func eventCovered(track []AudioSample, event Event, silenceRMS float64, windowSeconds float64) bool {
	for _, sample := range track {
		if sample.RMS > silenceRMS && math.Abs(sample.T-event.T) <= windowSeconds {
			return true
		}
	}
	return false
}
