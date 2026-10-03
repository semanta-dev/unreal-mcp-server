package audit

import (
	"fmt"
	"math"

)

const (
	defaultMaxSlideRatio = 0.35
	// defaultMaxYawRateDegPerSec is a smooth-turn ceiling in DEGREES PER SECOND. It is
	// deliberately below Aesir's raw RotationRate=540°/s (EnemyCharacter.cpp:47) so the
	// real snap-rotation defect FAILS, while a human-plausible ~120-360°/s turn passes.
	// A per-second rate (not per-frame) makes the audit capture-rate-independent.
	defaultMaxYawRateDegPerSec = 400.0
	motionRatioEpsilon         = 0.000001
)

// MotionReport is the locomotion filmstrip audit result. MaxYawRateDegPerSec is the
// peak facing-yaw angular rate over the strip (shortest-arc, so a ±180° wrap is not
// mistaken for a snap).
type MotionReport struct {
	Pass                bool
	FootSlideRatio      float64
	MaxYawRateDegPerSec float64
	Reasons             []string
}

// InMotionAuditDefault runs InMotionAudit with the plan's default foot-slide ratio
// and snap-rotation (deg/second) ceilings.
func InMotionAuditDefault(samples []MotionSample) MotionReport {
	return InMotionAudit(samples, defaultMaxSlideRatio, defaultMaxYawRateDegPerSec)
}

// InMotionAudit checks foot-slide and snap-rotation over a locomotion filmstrip.
// maxYawRateDegPerSec bounds the facing-yaw angular rate in degrees per second.
func InMotionAudit(samples []MotionSample, maxSlideRatio float64, maxYawRateDegPerSec float64) MotionReport {
	report := MotionReport{Pass: true}
	if len(samples) == 0 {
		return report
	}

	report.FootSlideRatio = meanFootSlideRatio(samples)
	report.MaxYawRateDegPerSec = maxYawRate(samples)

	if report.FootSlideRatio > maxSlideRatio {
		report.Pass = false
		report.Reasons = append(report.Reasons, fmt.Sprintf("foot_slide_ratio %.3f above max %.3f", report.FootSlideRatio, maxSlideRatio))
	}
	if report.MaxYawRateDegPerSec > maxYawRateDegPerSec {
		report.Pass = false
		report.Reasons = append(report.Reasons, fmt.Sprintf("yaw_rate %.1f deg/s above max %.1f deg/s", report.MaxYawRateDegPerSec, maxYawRateDegPerSec))
	}
	return report
}

func meanFootSlideRatio(samples []MotionSample) float64 {
	total := 0.0
	for _, sample := range samples {
		denom := math.Max(math.Max(math.Abs(sample.RootTravel), math.Abs(sample.AnimStride)), motionRatioEpsilon)
		total += math.Abs(sample.RootTravel-sample.AnimStride) / denom
	}
	return total / float64(len(samples))
}

// maxYawRate returns the peak facing-yaw angular rate in deg/second, using the
// shortest signed arc between consecutive samples so a legitimate turn crossing the
// ±180° boundary (e.g. 179°→-179°, a 2° turn) is not scored as a ~358° snap.
func maxYawRate(samples []MotionSample) float64 {
	maxRate := 0.0
	for i := 1; i < len(samples); i++ {
		dt := samples[i].T - samples[i-1].T
		if dt <= 0 {
			continue
		}
		rate := math.Abs(shortestArcDeg(samples[i].Yaw, samples[i-1].Yaw)) / dt
		if rate > maxRate {
			maxRate = rate
		}
	}
	return maxRate
}

// shortestArcDeg is the signed shortest angular difference a-b in (-180,180].
func shortestArcDeg(a, b float64) float64 {
	d := math.Mod(a-b, 360)
	if d > 180 {
		d -= 360
	}
	if d < -180 {
		d += 360
	}
	return d
}
