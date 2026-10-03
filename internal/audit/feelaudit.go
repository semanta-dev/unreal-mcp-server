// feelaudit (merged into package audit) implements the feel_audit, verb_response, and in_motion_audit
// Layer-A audits from AGENTIC_GAMEDEV_PLAN.md §6.2 and §7.1 / RC8.
//
// These audits certify feedback presence and non-degeneracy only. They do not
// claim that a verb, impact, or locomotion pass feels good.
package audit

import (
	"fmt"
	"math"

)

const (
	defaultWithinMs      = 120.0
	defaultMaxFXPerEvent = 4
)

// FeelReport is the aggregate event-feedback audit result.
type FeelReport struct {
	Pass     bool
	PerEvent []EventResult
	Reasons  []string
}

// EventResult is the feedback-channel result for one gameplay event.
type EventResult struct {
	Index      int
	HasVisual  bool
	HasAudio   bool
	HasCamera  bool
	LatencyMs  float64
	OverJuiced bool
}

// FeelAuditDefault runs FeelAudit with the plan's default 120ms response window
// and per-event FX cap of 4.
func FeelAuditDefault(events []Event) FeelReport {
	return FeelAudit(events, defaultWithinMs, defaultMaxFXPerEvent)
}

// FeelAudit checks that every gameplay event has visual, audio, and camera
// feedback within withinMs, and stays under maxFXPerEvent.
func FeelAudit(events []Event, withinMs float64, maxFXPerEvent int) FeelReport {
	report := FeelReport{
		Pass:     true,
		PerEvent: make([]EventResult, 0, len(events)),
	}

	windowSeconds := withinMs / 1000.0
	for i, event := range events {
		result := EventResult{
			Index:      i,
			HasVisual:  responseWithin(event.T, event.VisualT, windowSeconds),
			HasAudio:   responseWithin(event.T, event.AudioT, windowSeconds),
			HasCamera:  responseWithin(event.T, event.CameraT, windowSeconds),
			OverJuiced: event.FXCount > maxFXPerEvent,
		}

		latency, ok := firstResponseLatencyMs(event)
		if ok {
			result.LatencyMs = latency
		}

		if !result.HasVisual || !result.HasAudio || !result.HasCamera || result.OverJuiced {
			report.Pass = false
			report.Reasons = append(report.Reasons, feelReason(i, result))
		}
		report.PerEvent = append(report.PerEvent, result)
	}

	return report
}

func responseWithin(eventT, responseT, windowSeconds float64) bool {
	if responseT == 0 {
		return false
	}
	delta := responseT - eventT
	return delta >= 0 && delta <= windowSeconds
}

func firstResponseLatencyMs(event Event) (float64, bool) {
	minT := math.Inf(1)
	for _, t := range []float64{event.VisualT, event.AudioT, event.CameraT} {
		if t == 0 || t < event.T {
			continue
		}
		if t < minT {
			minT = t
		}
	}
	if math.IsInf(minT, 1) {
		return 0, false
	}
	return (minT - event.T) * 1000, true
}

func feelReason(index int, result EventResult) string {
	reason := fmt.Sprintf("event %d failed", index)
	if !result.HasVisual {
		reason += ": missing visual"
	}
	if !result.HasAudio {
		reason += ": missing audio"
	}
	if !result.HasCamera {
		reason += ": missing camera"
	}
	if result.OverJuiced {
		reason += ": over FX cap"
	}
	return reason
}
