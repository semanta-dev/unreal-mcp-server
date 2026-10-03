package audit

import (
	"fmt"
	"math"
)

const inputOnsetThreshold = 0.000001

// VerbReport is the input-to-state response audit result.
type VerbReport struct {
	Pass         bool
	RiseFrames   int
	PeakDeltaMag float64
	Degenerate   bool
	Reason       string
}

// VerbResponse fingerprints a 60fps input burst and rejects teleport-snap,
// dead-zone, and mushy response shapes against env.
func VerbResponse(burst []BurstSample, env VerbEnvelope) VerbReport {
	onset := inputOnset(burst)
	if onset < 0 {
		return VerbReport{
			Pass:       false,
			Degenerate: true,
			Reason:     "no input onset",
		}
	}

	deltas := stateDeltas(burst, onset)
	peak, peakIndex := peakDelta(deltas)
	riseFrames := 0
	if peak > 0 {
		riseFrames = framesToReach(deltas, onset, peak*0.9)
	}

	report := VerbReport{
		Pass:         true,
		RiseFrames:   riseFrames,
		PeakDeltaMag: peak,
	}

	switch {
	case riseFrames < env.MinRiseFrames:
		report.Degenerate = true
		report.Reason = fmt.Sprintf("rise_frames %d below min %d", riseFrames, env.MinRiseFrames)
	case peak < env.MinDeltaMag:
		report.Degenerate = true
		report.Reason = fmt.Sprintf("peak_delta_mag %.3f below min %.3f", peak, env.MinDeltaMag)
	case riseFrames > env.MaxRiseFrames:
		report.Degenerate = true
		report.Reason = fmt.Sprintf("rise_frames %d above max %d", riseFrames, env.MaxRiseFrames)
	case peakIndex < onset:
		report.Degenerate = true
		report.Reason = "peak before input onset"
	}
	report.Pass = !report.Degenerate
	return report
}

func inputOnset(burst []BurstSample) int {
	for i, sample := range burst {
		if sample.Input > inputOnsetThreshold {
			return i
		}
	}
	return -1
}

func stateDeltas(burst []BurstSample, onset int) []float64 {
	deltas := make([]float64, len(burst))
	base := burst[onset]
	for i := onset; i < len(burst); i++ {
		deltas[i] = movementStateDelta(base, burst[i])
	}
	return deltas
}

func movementStateDelta(base, sample BurstSample) float64 {
	sum := 0.0
	for i := 0; i < 3; i++ {
		posDelta := sample.Pos[i] - base.Pos[i]
		velDelta := sample.Vel[i] - base.Vel[i]
		sum += posDelta*posDelta + velDelta*velDelta
	}
	return math.Sqrt(sum)
}

func peakDelta(deltas []float64) (float64, int) {
	peak := 0.0
	peakIndex := -1
	for i, delta := range deltas {
		if delta > peak {
			peak = delta
			peakIndex = i
		}
	}
	return peak, peakIndex
}

func framesToReach(deltas []float64, onset int, threshold float64) int {
	for i := onset; i < len(deltas); i++ {
		if deltas[i] >= threshold {
			framesAfterFirstDelta := i - onset - 1
			if framesAfterFirstDelta < 0 {
				return 0
			}
			return framesAfterFirstDelta
		}
	}
	return len(deltas) - onset
}
