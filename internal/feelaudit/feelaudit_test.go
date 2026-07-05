package feelaudit

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/gametrace"
)

func TestFeelAuditDefaultFixtures(t *testing.T) {
	t.Run("fails silent debug-line fire", func(t *testing.T) {
		report := FeelAuditDefault(gametrace.SilentDebugLineFire())
		if report.Pass {
			t.Fatalf("FeelAuditDefault(SilentDebugLineFire()).Pass = true, want false")
		}
	})

	t.Run("passes juicy fire", func(t *testing.T) {
		report := FeelAuditDefault(gametrace.JuicyFire())
		if !report.Pass {
			t.Fatalf("FeelAuditDefault(JuicyFire()).Pass = false, want true; reasons: %#v", report.Reasons)
		}
	})
}

func TestVerbResponseFixtures(t *testing.T) {
	t.Run("fails teleport snap", func(t *testing.T) {
		report := VerbResponse(gametrace.TeleportSnapBurst(), gametrace.DefaultMovementEnvelope())
		if report.Pass {
			t.Fatalf("VerbResponse(TeleportSnapBurst()).Pass = true, want false")
		}
		if !report.Degenerate {
			t.Fatalf("VerbResponse(TeleportSnapBurst()).Degenerate = false, want true")
		}
	})

	t.Run("passes healthy movement", func(t *testing.T) {
		report := VerbResponse(gametrace.HealthyMovementBurst(), gametrace.DefaultMovementEnvelope())
		if !report.Pass {
			t.Fatalf("VerbResponse(HealthyMovementBurst()).Pass = false, want true; report: %#v", report)
		}
		if report.Degenerate {
			t.Fatalf("VerbResponse(HealthyMovementBurst()).Degenerate = true, want false")
		}
	})
}

func TestInMotionAuditDefaultFixtures(t *testing.T) {
	t.Run("fails foot slide motion", func(t *testing.T) {
		report := InMotionAuditDefault(gametrace.FootSlideMotion(30))
		if report.Pass {
			t.Fatalf("InMotionAuditDefault(FootSlideMotion(30)).Pass = true, want false")
		}
	})

	t.Run("passes blended motion", func(t *testing.T) {
		report := InMotionAuditDefault(gametrace.BlendedMotion(30))
		if !report.Pass {
			t.Fatalf("InMotionAuditDefault(BlendedMotion(30)).Pass = false, want true; reasons: %#v", report.Reasons)
		}
	})
}

func TestFeelAuditFlagsLateMissingAndOverJuicedEvents(t *testing.T) {
	report := FeelAudit([]gametrace.Event{
		{T: 1, VisualT: 1.05, AudioT: 1.07, CameraT: 1.08, FXCount: 1},
		{T: 2, VisualT: 2.20, AudioT: 2.01, FXCount: 5},
	}, 120, 4)

	if report.Pass {
		t.Fatalf("FeelAudit mixed events passed, want fail")
	}
	if len(report.PerEvent) != 2 {
		t.Fatalf("PerEvent length = %d, want 2", len(report.PerEvent))
	}
	if !report.PerEvent[0].HasVisual || !report.PerEvent[0].HasAudio || !report.PerEvent[0].HasCamera {
		t.Fatalf("first event channels = %#v, want all present", report.PerEvent[0])
	}
	if report.PerEvent[1].HasVisual || report.PerEvent[1].HasCamera || !report.PerEvent[1].OverJuiced {
		t.Fatalf("second event = %#v, want late visual, missing camera, over-juiced", report.PerEvent[1])
	}
}

func TestInMotionAuditEmptySamplesPasses(t *testing.T) {
	report := InMotionAuditDefault(nil)
	if !report.Pass {
		t.Fatalf("InMotionAuditDefault(nil).Pass = false, want true")
	}
}

// A gradual turn that crosses the ±180° wrap boundary must NOT read as a snap.
func TestInMotionAuditWrapIsNotSnap(t *testing.T) {
	// 30 samples at 60fps turning a smooth 120°/s straight through +180 -> -180.
	samples := make([]gametrace.MotionSample, 30)
	yaw := 170.0
	for i := range samples {
		yaw += 120.0 / 60.0
		w := yaw
		for w >= 180 {
			w -= 360
		}
		samples[i] = gametrace.MotionSample{T: float64(i) / 60.0, RootTravel: 5, AnimStride: 5, Yaw: w}
	}
	report := InMotionAuditDefault(samples)
	if !report.Pass {
		t.Fatalf("wrap-crossing gradual turn flagged as snap: rate=%.1f deg/s reasons=%#v", report.MaxYawRateDegPerSec, report.Reasons)
	}
	if report.MaxYawRateDegPerSec > 200 {
		t.Fatalf("wrap inflated yaw rate to %.1f deg/s (should be ~120)", report.MaxYawRateDegPerSec)
	}
}

// The real 540°/s snap must FAIL on the yaw leg alone (no foot-slide present).
func TestInMotionAuditRealSnapRateFails(t *testing.T) {
	samples := make([]gametrace.MotionSample, 20)
	yaw := 0.0
	for i := range samples {
		yaw += 540.0 / 60.0 // 540°/s at 60fps
		w := yaw
		for w >= 180 {
			w -= 360
		}
		samples[i] = gametrace.MotionSample{T: float64(i) / 60.0, RootTravel: 5, AnimStride: 5, Yaw: w} // no slide
	}
	report := InMotionAuditDefault(samples)
	if report.Pass {
		t.Fatalf("540 deg/s snap passed; yaw rate=%.1f deg/s", report.MaxYawRateDegPerSec)
	}
	if report.MaxYawRateDegPerSec < 500 {
		t.Fatalf("expected ~540 deg/s, got %.1f", report.MaxYawRateDegPerSec)
	}
}
