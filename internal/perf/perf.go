// Package perf turns Unreal's textual profiling output into structured numbers so
// a rubric can gate performance-as-DATA (p95 frame time, hitch count, memory)
// instead of an agent squinting at "stat fps" viewport text. It parses a
// CsvProfiler CSV (frame-time percentiles) and a `memreport` dump (memory
// buckets). Pure Go, unit-tested; the live capture (triggering CsvProfiler /
// memreport) is the gated part.
package perf

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FrameStats summarizes per-frame timing (all in milliseconds).
type FrameStats struct {
	Count      int     `json:"count"`
	MeanMs     float64 `json:"mean_ms"`
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
	P99Ms      float64 `json:"p99_ms"`
	MaxMs      float64 `json:"max_ms"`
	HitchCount int     `json:"hitch_count"` // frames slower than the hitch threshold
	HitchMs    float64 `json:"hitch_ms"`
	Column     string  `json:"column"` // the CSV column used
}

// ParseCSV parses a CsvProfiler CSV (header row + one row per frame) and computes
// frame-time percentiles from the frame-time column. hitchMs counts frames slower
// than that (0 => a 33.3ms default). The frame-time column is found by header
// name (FrameTime / Frame / GameThreadTime as a fallback).
func ParseCSV(content string, hitchMs float64) (FrameStats, error) {
	if hitchMs <= 0 {
		hitchMs = 33.3
	}
	lines := splitNonEmpty(content)
	if len(lines) < 2 {
		return FrameStats{HitchMs: hitchMs}, nil
	}
	headers := splitCSV(lines[0])
	col := pickFrameCol(headers)
	if col < 0 {
		return FrameStats{HitchMs: hitchMs}, nil
	}
	var vals []float64
	for _, ln := range lines[1:] {
		fields := splitCSV(ln)
		if col >= len(fields) {
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(fields[col]), 64); err == nil {
			vals = append(vals, f)
		}
	}
	return stats(vals, hitchMs, strings.TrimSpace(headers[col])), nil
}

func stats(vals []float64, hitchMs float64, col string) FrameStats {
	s := FrameStats{Count: len(vals), HitchMs: hitchMs, Column: col}
	if len(vals) == 0 {
		return s
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range vals {
		sum += v
		if v > hitchMs {
			s.HitchCount++
		}
	}
	s.MeanMs = sum / float64(len(vals))
	s.P50Ms = percentile(sorted, 0.50)
	s.P95Ms = percentile(sorted, 0.95)
	s.P99Ms = percentile(sorted, 0.99)
	s.MaxMs = sorted[len(sorted)-1]
	return s
}

// percentile uses nearest-rank on a pre-sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p*float64(len(sorted)-1) + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func pickFrameCol(headers []string) int {
	want := []string{"frametime", "frame", "gamethreadtime", "renderthreadtime"}
	for _, w := range want {
		for i, h := range headers {
			if strings.EqualFold(strings.TrimSpace(h), w) || strings.Contains(strings.ToLower(h), w) {
				return i
			}
		}
	}
	return -1
}

// MemReport holds figures pulled from a `memreport` dump (in MB where available).
type MemReport struct {
	PhysicalMB float64            `json:"physical_mb"`
	VirtualMB  float64            `json:"virtual_mb"`
	Buckets    map[string]float64 `json:"buckets,omitempty"` // named memory lines -> MB
}

var (
	rePhysical = regexp.MustCompile(`(?i)(?:used physical|physical memory|physical used)\D*([0-9.]+)\s*(MB|GB|KB)`)
	reVirtual  = regexp.MustCompile(`(?i)(?:used virtual|virtual memory)\D*([0-9.]+)\s*(MB|GB|KB)`)
	reBucket   = regexp.MustCompile(`(?m)^\s*([A-Za-z][\w /()-]+?)\s+([0-9][0-9.]*)\s*(MB|KB|GB)\s*$`)
)

// ParseMemReport extracts memory figures from a memreport dump.
func ParseMemReport(content string) MemReport {
	m := MemReport{Buckets: map[string]float64{}}
	if g := rePhysical.FindStringSubmatch(content); g != nil {
		m.PhysicalMB = toMB(g[1], g[2])
	}
	if g := reVirtual.FindStringSubmatch(content); g != nil {
		m.VirtualMB = toMB(g[1], g[2])
	}
	for _, g := range reBucket.FindAllStringSubmatch(content, -1) {
		name := strings.TrimSpace(g[1])
		if len(name) < 3 {
			continue
		}
		m.Buckets[name] = toMB(g[2], g[3])
	}
	if len(m.Buckets) == 0 {
		m.Buckets = nil
	}
	return m
}

func toMB(num, unit string) float64 {
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(unit) {
	case "GB":
		return f * 1024
	case "KB":
		return f / 1024
	default:
		return f
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, ln := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func splitCSV(line string) []string {
	fields := strings.Split(line, ",")
	// CsvProfiler often has a trailing comma; that's harmless (empty last field).
	return fields
}
