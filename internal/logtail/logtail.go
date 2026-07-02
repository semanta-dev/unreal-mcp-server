// Package logtail reads the Unreal project log (Saved/Logs) incrementally by
// byte offset, and filters lines by severity/category. Used by the log tools
// (P8) and Live Coding result detection (P7). File reading is pure and testable.
package logtail

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LogPath returns the newest .log under <projectDir>/Saved/Logs, or "" if none.
func LogPath(projectDir string) string {
	dir := filepath.Join(projectDir, "Saved", "Logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type ent struct {
		path string
		mod  int64
	}
	var logs []ent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		logs = append(logs, ent{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	if len(logs) == 0 {
		return ""
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod > logs[j].mod })
	return logs[0].path
}

// Size returns the current byte size of a file (0 if missing).
func Size(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ReadFrom reads path from byteOffset to EOF, returning the text and the new
// offset. If the file shrank (rotated), it reads from the start.
func ReadFrom(path string, byteOffset int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", byteOffset, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", byteOffset, err
	}
	size := fi.Size()
	if byteOffset > size {
		byteOffset = 0 // rotated/truncated
	}
	if _, err := f.Seek(byteOffset, 0); err != nil {
		return "", byteOffset, err
	}
	buf := make([]byte, size-byteOffset)
	n, _ := f.Read(buf)
	return string(buf[:n]), byteOffset + int64(n), nil
}

// Severity rank for min-severity filtering (Unreal verbosity levels).
var sevRank = map[string]int{
	"fatal": 5, "error": 4, "warning": 3, "display": 2, "log": 1, "verbose": 0,
}

// LineSeverity extracts the severity token from an Unreal log line like
// "[..]LogFoo: Warning: msg" or "LogFoo: Error: msg". Returns "log" if none.
func LineSeverity(line string) string {
	// find "Category: Severity:" — severity is the token before the 2nd colon
	// after a category. Cheap heuristic: look for known severity words.
	l := strings.ToLower(line)
	for _, s := range []string{"fatal:", "error:", "warning:", "display:"} {
		if strings.Contains(l, ": "+s) || strings.Contains(l, s) {
			return strings.TrimSuffix(s, ":")
		}
	}
	return "log"
}

// FilterLines returns lines from text at or above minSeverity, optionally
// restricted to the given categories (case-insensitive substring "Category:").
func FilterLines(text, minSeverity string, categories []string) []string {
	minRank := sevRank[strings.ToLower(minSeverity)]
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if sevRank[LineSeverity(line)] < minRank {
			continue
		}
		if len(categories) > 0 && !matchesCategory(line, categories) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func matchesCategory(line string, categories []string) bool {
	for _, c := range categories {
		if strings.Contains(line, c+":") {
			return true
		}
	}
	return false
}

// CountBySeverity returns counts of errors, warnings, and ensures in lines.
func CountBySeverity(lines []string) (errs, warns, ensures int) {
	for _, l := range lines {
		switch LineSeverity(l) {
		case "error", "fatal":
			errs++
		case "warning":
			warns++
		}
		if strings.Contains(l, "Ensure condition failed") || strings.Contains(strings.ToLower(l), "ensure ") {
			ensures++
		}
	}
	return
}
