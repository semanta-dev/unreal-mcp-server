package perf

import (
	"math"
	"testing"
)

func TestParseCSVPercentiles(t *testing.T) {
	// 10 frames, one big hitch at 100ms.
	csv := "FrameTime,GameThreadTime\n" +
		"16,8\n16,8\n16,8\n17,9\n16,8\n18,9\n16,8\n100,50\n16,8\n17,9\n"
	s, err := ParseCSV(csv, 33.3)
	if err != nil {
		t.Fatal(err)
	}
	if s.Count != 10 || s.Column != "FrameTime" {
		t.Fatalf("stats = %+v", s)
	}
	if s.MaxMs != 100 {
		t.Errorf("max = %g, want 100", s.MaxMs)
	}
	if s.HitchCount != 1 {
		t.Errorf("hitch count = %d, want 1", s.HitchCount)
	}
	// p50 should be a normal frame (~16-17), not skewed by the single hitch.
	if s.P50Ms > 20 {
		t.Errorf("p50 = %g, want ~16", s.P50Ms)
	}
	if s.P99Ms < 100 {
		t.Errorf("p99 = %g, want ~100 (the hitch)", s.P99Ms)
	}
}

func TestParseCSVMissingColumn(t *testing.T) {
	s, _ := ParseCSV("SomethingElse,Other\n1,2\n", 0)
	if s.Count != 0 {
		t.Errorf("no frame-time column should yield 0 frames, got %+v", s)
	}
}

func TestParseCSVFallbackColumn(t *testing.T) {
	// No FrameTime, but GameThreadTime present -> use it.
	s, _ := ParseCSV("GameThreadTime,X\n10,1\n20,1\n30,1\n", 0)
	if s.Count != 3 || s.Column != "GameThreadTime" {
		t.Fatalf("fallback failed: %+v", s)
	}
}

func TestParseMemReport(t *testing.T) {
	report := `Platform Memory Stats
Process Physical Memory: 2048.50 MB used
Process Virtual Memory: 4096.00 MB used
Textures 512 MB
Meshes 256 MB
`
	m := ParseMemReport(report)
	if math.Abs(m.PhysicalMB-2048.5) > 0.01 {
		t.Errorf("physical = %g", m.PhysicalMB)
	}
	if math.Abs(m.VirtualMB-4096) > 0.01 {
		t.Errorf("virtual = %g", m.VirtualMB)
	}
	if m.Buckets["Textures"] != 512 {
		t.Errorf("buckets = %v", m.Buckets)
	}
}

func TestMemReportGBConversion(t *testing.T) {
	m := ParseMemReport("Used Physical: 3 GB")
	if m.PhysicalMB != 3072 {
		t.Errorf("GB conversion wrong: %g", m.PhysicalMB)
	}
}
