package audit

import (
	"testing"
)

func TestAudioAuditDefaultFixtures(t *testing.T) {
	t.Run("fails silent debug-line fire defect fixture", func(t *testing.T) {
		report := AudioAuditDefault(SilentTrack(600), SilentDebugLineFire())
		if report.Pass {
			t.Fatalf("AudioAuditDefault(SilentTrack, SilentDebugLineFire).Pass = true, want false")
		}
		if report.NotSilent {
			t.Fatalf("AudioAuditDefault(SilentTrack, SilentDebugLineFire).NotSilent = true, want false")
		}
	})

	t.Run("passes loud juicy fire fixed fixture", func(t *testing.T) {
		report := AudioAuditDefault(LoudTrack(600), JuicyFire())
		if !report.Pass {
			t.Fatalf("AudioAuditDefault(LoudTrack, JuicyFire).Pass = false, want true; report: %#v", report)
		}
		if !report.NotSilent {
			t.Fatalf("AudioAuditDefault(LoudTrack, JuicyFire).NotSilent = false, want true")
		}
	})
}

func TestAudioAuditEventCoverage(t *testing.T) {
	track := []AudioSample{
		{T: 0.000, RMS: 0.02},
		{T: 1.119, RMS: 0.02},
		{T: 2.121, RMS: 0.02},
	}
	events := []Event{
		{T: 0.000, Kind: EventFire},
		{T: 1.000, Kind: EventHit},
		{T: 2.000, Kind: EventDeath},
	}

	report := AudioAudit(track, events, 0.01, 120)
	if report.EventsTotal != 3 {
		t.Fatalf("EventsTotal = %d, want 3", report.EventsTotal)
	}
	if report.EventsCovered != 2 {
		t.Fatalf("EventsCovered = %d, want 2; report: %#v", report.EventsCovered, report)
	}
	if report.Pass {
		t.Fatalf("Pass = true, want false when an event lacks an audible sample in-window")
	}
}

func TestAudioAuditNoEventsRequiresOnlyNonSilentTrack(t *testing.T) {
	report := AudioAudit([]AudioSample{{T: 0, RMS: 0.02}}, nil, 0.01, 120)
	if !report.Pass {
		t.Fatalf("Pass = false, want true for non-silent track with no events; report: %#v", report)
	}
	if report.EventsTotal != 0 || report.EventsCovered != 0 {
		t.Fatalf("event counts = %d/%d, want 0/0", report.EventsCovered, report.EventsTotal)
	}
}
