// Package build orchestrates C++ compilation (Build.bat / Live Coding), parses
// compiler diagnostics, and classifies the right build strategy from a diff.
// The parsing/classification are pure and unit-tested; the subprocess run is
// live (GO_REWRITE_PLAN.md §9 Group F, §10).
package build

import (
	"regexp"
	"strconv"
	"strings"
)

// Severity of a diagnostic.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
)

// Diagnostic is one parsed compiler/tool message.
type Diagnostic struct {
	Tool     string   `json:"tool"`     // MSVC | UHT | Clang | Linker | UBT
	Severity Severity `json:"severity"` // error | warning
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Column   int      `json:"column,omitempty"`
	Code     string   `json:"code,omitempty"` // e.g. C2065, LNK2019
	Message  string   `json:"message"`
}

var (
	// C:\path\File.cpp(123): error C2065: 'x': undeclared identifier
	// C:\path\File.cpp(123,45): warning C4100: ...
	reMSVC = regexp.MustCompile(`^\s*(.+?)\((\d+)(?:,(\d+))?\)\s*:\s*(error|warning|fatal error)\s+([A-Z]+\d+)\s*:\s*(.*)$`)
	// file.cpp:123:45: error: message   (clang toolchain)
	reClang = regexp.MustCompile(`^\s*(.+?):(\d+):(\d+):\s*(error|warning):\s*(.*)$`)
	// File.h(12): Error: message   (UnrealHeaderTool; word severity, no code)
	reUHT = regexp.MustCompile(`^\s*(.+?)\((\d+)\)\s*:\s*(Error|Warning)\s*:\s*(.*)$`)
	// Linker: error LNK2019: unresolved external symbol ...
	reLNK = regexp.MustCompile(`\b(error|warning)\s+(LNK\d+)\s*:\s*(.*)$`)
)

// ParseDiagnostics extracts structured diagnostics from raw build output.
// Deduplicated, order-preserving.
func ParseDiagnostics(output string) []Diagnostic {
	var out []Diagnostic
	seen := make(map[string]bool)
	add := func(d Diagnostic) {
		key := d.Tool + "|" + string(d.Severity) + "|" + d.File + "|" + strconv.Itoa(d.Line) + "|" + d.Code + "|" + d.Message
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, d)
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := reMSVC.FindStringSubmatch(line); m != nil {
			d := Diagnostic{Tool: "MSVC", Severity: normSeverity(m[4]), File: m[1], Code: m[5], Message: m[6]}
			d.Line, _ = strconv.Atoi(m[2])
			if m[3] != "" {
				d.Column, _ = strconv.Atoi(m[3])
			}
			if strings.HasPrefix(m[5], "LNK") {
				d.Tool = "Linker"
			}
			add(d)
			continue
		}
		if m := reClang.FindStringSubmatch(line); m != nil {
			d := Diagnostic{Tool: "Clang", Severity: normSeverity(m[4]), File: m[1], Message: m[5]}
			d.Line, _ = strconv.Atoi(m[2])
			d.Column, _ = strconv.Atoi(m[3])
			add(d)
			continue
		}
		if m := reUHT.FindStringSubmatch(line); m != nil {
			d := Diagnostic{Tool: "UHT", Severity: normSeverity(m[3]), File: m[1], Message: m[4]}
			d.Line, _ = strconv.Atoi(m[2])
			add(d)
			continue
		}
		if m := reLNK.FindStringSubmatch(line); m != nil {
			add(Diagnostic{Tool: "Linker", Severity: normSeverity(m[1]), Code: m[2], Message: m[3]})
			continue
		}
	}
	return out
}

func normSeverity(s string) Severity {
	if strings.EqualFold(s, "warning") {
		return SevWarning
	}
	return SevError // "error" or "fatal error"
}

// CountErrors returns the number of error-severity diagnostics.
func CountErrors(ds []Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == SevError {
			n++
		}
	}
	return n
}
