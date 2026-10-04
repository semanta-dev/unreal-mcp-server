// feelaudit (merged into package audit) implements the feel_audit, verb_response, and in_motion_audit
// Layer-A audits from AGENTIC_GAMEDEV_PLAN.md §6.2 and §7.1 / RC8.
//
// These audits certify feedback presence and non-degeneracy only. They do not
// claim that a verb, impact, or locomotion pass feels good.
package audit

import (
	"fmt"
	"math"
	"sort"
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
	// Median delay per channel over the events that had that response (ms; absent
	// when none did): "how long until the hit is seen" is MedianVisualMs, not the
	// first-response LatencyMs (a shot's sound usually starts with it).
	MedianVisualMs *float64 `json:",omitempty"`
	MedianAudioMs  *float64 `json:",omitempty"`
	MedianCameraMs *float64 `json:",omitempty"`
}

// EventResult is the feedback-channel result for one gameplay event.
type EventResult struct {
	Index     int
	HasVisual bool
	HasAudio  bool
	HasCamera bool
	LatencyMs float64 // to the first response on any channel
	// Each channel's delay (ms) when it responded at or after the event.
	VisualMs   *float64 `json:",omitempty"`
	AudioMs    *float64 `json:",omitempty"`
	CameraMs   *float64 `json:",omitempty"`
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
		result.VisualMs, result.AudioMs, result.CameraMs = delayMs(event.T, event.VisualT), delayMs(event.T, event.AudioT), delayMs(event.T, event.CameraT)

		if !result.HasVisual || !result.HasAudio || !result.HasCamera || result.OverJuiced {
			report.Pass = false
			report.Reasons = append(report.Reasons, feelReason(i, result))
		}
		report.PerEvent = append(report.PerEvent, result)
	}
	var vis, aud, cam []float64
	for _, r := range report.PerEvent {
		for _, c := range []struct {
			v   *float64
			dst *[]float64
		}{{r.VisualMs, &vis}, {r.AudioMs, &aud}, {r.CameraMs, &cam}} {
			if c.v != nil {
				*c.dst = append(*c.dst, *c.v)
			}
		}
	}
	report.MedianVisualMs, report.MedianAudioMs, report.MedianCameraMs = medianOf(vis), medianOf(aud), medianOf(cam)
	return report
}

// delayMs is a response's delay after the event, or nil (no response, or before it).
func delayMs(eventT, responseT float64) *float64 {
	if responseT == 0 || responseT < eventT {
		return nil
	}
	d := (responseT - eventT) * 1000
	return &d
}

func medianOf(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := s[len(s)/2]
	if len(s)%2 == 0 {
		m = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return &m
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
