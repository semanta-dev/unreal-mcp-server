// Package crash extracts a structured crash/fatal-error report after a UE editor
// or PIE session dies. It is deliberately Go-side (not a bridge op): on a hard
// crash the in-editor Python bridge dies WITH the editor, so the surviving Go
// server is the only thing that can read the wreckage. Two sources are parsed:
//
//   - Saved/Crashes/UECC-*/CrashContext.runtime-xml (the crash reporter's dump),
//   - the project log, scanned for a critical-error block (Assertion failed /
//     Fatal error / === Critical error ===) between two logs_mark markers.
//
// Both yield a Report{Kind, Summary, File:Line, Frames[]} so an autonomous agent
// can branch on WHY a run died and jump straight to the offending source line,
// instead of a human reading the crash window.
package crash

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Frame is one parsed call-stack entry. Any field may be zero if the stack line
// was unsymbolicated (e.g. only a module + address was available).
type Frame struct {
	Module string `json:"module,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// Report is a structured crash. Kind is one of assert|fatal|ensure|crash. File/
// Line are the best-guess origin (from the error message or the top symbolicated
// frame). Source records where the report came from (a crash dir or "log").
type Report struct {
	Kind    string  `json:"kind"`
	Summary string  `json:"summary"`
	File    string  `json:"file,omitempty"`
	Line    int     `json:"line,omitempty"`
	Frames  []Frame `json:"frames,omitempty"`
	Source  string  `json:"source"`
}

var (
	reErrorMsg = regexp.MustCompile(`(?s)<ErrorMessage>(.*?)</ErrorMessage>`)
	reCallStk  = regexp.MustCompile(`(?s)<CallStack>(.*?)</CallStack>`)
	reCrashTyp = regexp.MustCompile(`(?s)<CrashType>(.*?)</CrashType>`)
	// A [File:...] [Line: N] pair as UE writes it in error messages.
	reFileLine = regexp.MustCompile(`\[File:\s*([^\]]+?)\s*\]\s*\[Line:\s*(\d+)\s*\]`)
	// A frame line: optional "Module!", a symbol, optional " [loc]".
	reFrame = regexp.MustCompile(`^\s*(?:0x[0-9a-fA-F]+\s+)?(?:([\w.+-]+)!)?([^\[\]]+?)\s*(?:\[([^\]]*)\])?\s*$`)
)

// ParseCrashContext parses the contents of a CrashContext.runtime-xml. It returns
// nil if no ErrorMessage is present (i.e. the file is not a crash dump).
func ParseCrashContext(xml string) *Report {
	m := reErrorMsg.FindStringSubmatch(xml)
	if m == nil {
		return nil
	}
	r := &Report{Kind: "crash", Summary: xmlUnescape(strings.TrimSpace(m[1])), Source: "crashcontext"}
	if t := reCrashTyp.FindStringSubmatch(xml); t != nil {
		r.Kind = classifyKind(t[1], r.Summary)
	} else {
		r.Kind = classifyKind("", r.Summary)
	}
	if fl := reFileLine.FindStringSubmatch(r.Summary); fl != nil {
		r.File = normFile(fl[1])
		r.Line, _ = strconv.Atoi(fl[2])
	}
	if cs := reCallStk.FindStringSubmatch(xml); cs != nil {
		r.Frames = parseFrames(xmlUnescape(cs[1]))
	}
	r.fillOriginFromFrames()
	return r
}

// FromCrashDir finds the newest Saved/Crashes/UECC-* dir modified at/after
// `since` and parses its CrashContext.runtime-xml. Returns (nil, nil) if the
// project has no crash newer than `since`. `since` should be the run's start
// (e.g. the time of logs_mark) so a stale crash is not misattributed.
func FromCrashDir(projectDir string, since time.Time) (*Report, error) {
	root := filepath.Join(projectDir, "Saved", "Crashes")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type cand struct {
		dir string
		mod time.Time
	}
	var cands []cand
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "UECC-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(since) {
			continue
		}
		cands = append(cands, cand{filepath.Join(root, e.Name()), info.ModTime()})
	}
	if len(cands) == 0 {
		return nil, nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	newest := cands[0].dir
	data, err := os.ReadFile(filepath.Join(newest, "CrashContext.runtime-xml"))
	if err != nil {
		return nil, nil // dir exists but no dump yet
	}
	r := ParseCrashContext(string(data))
	if r != nil {
		r.Source = newest
	}
	return r, nil
}

// Log-scan markers, in priority order (most specific first).
var logMarkers = []struct {
	marker string
	kind   string
}{
	{"=== Critical error: ===", "fatal"},
	{"Assertion failed:", "assert"},
	{"Fatal error:", "fatal"},
	{"Ensure condition failed:", "ensure"},
}

// ScanLog scans a chunk of log text (e.g. logtail.ReadFrom since a marker) for
// the first critical-error block and parses it: the message + [File:Line] plus
// any symbolicated frames that follow. Real UE5 stack frames are logged as
// "LogOutputDevice: Error: [Callstack] 0x... Module!Symbol() [file:line]" — the
// per-frame "[Callstack]" token is stripped so the symbol+location parse. When
// the matched marker is the bare "=== Critical error: ===" banner (which carries
// no message — an access violation puts the cause on the following line), the
// real cause line is pulled forward for the summary. Returns nil if none.
func ScanLog(text string) *Report {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		for _, m := range logMarkers {
			if strings.Index(line, m.marker) < 0 {
				continue
			}
			r := &Report{Kind: m.kind, Source: "log"}
			msg := stripLogPrefix(line)
			if isBanner(msg) {
				// The banner has no message; the cause (Unhandled Exception: /
				// Assertion failed: / LowLevelFatalError:) is on a following line.
				for j := i + 1; j < len(lines) && j < i+12; j++ {
					cand := stripLogPrefix(lines[j])
					if cand == "" || isBanner(cand) {
						continue
					}
					msg = cand
					// Keep the banner's severe kind (fatal) unless the cause line
					// is a more specific assert/ensure.
					if k := classifyKind("", cand); k != "crash" {
						r.Kind = k
					}
					break
				}
			}
			r.Summary = msg
			if fl := reFileLine.FindStringSubmatch(msg); fl != nil {
				r.File = normFile(fl[1])
				r.Line, _ = strconv.Atoi(fl[2])
			}
			// Gather following lines that look like stack frames.
			var stack []string
			for j := i + 1; j < len(lines) && j < i+80; j++ {
				fl := stripLogPrefix(lines[j])
				if looksLikeFrame(fl) {
					stack = append(stack, fl)
				}
			}
			r.Frames = parseFrames(strings.Join(stack, "\n"))
			r.fillOriginFromFrames()
			return r
		}
	}
	return nil
}

// isBanner reports whether a stripped line is the decorative "=== Critical error:
// ===" banner rather than an informative message.
func isBanner(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "===") || s == ""
}

// --- helpers ---

func parseFrames(block string) []Frame {
	var frames []Frame
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if f, ok := parseFrame(line); ok {
			frames = append(frames, f)
		}
	}
	return frames
}

// parseFrame parses one call-stack line into a Frame. It reports false only for
// lines that carry no usable symbol/module at all.
func parseFrame(line string) (Frame, bool) {
	m := reFrame.FindStringSubmatch(line)
	if m == nil {
		return Frame{}, false
	}
	f := Frame{Module: m[1], Symbol: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[2]), "()"))}
	if loc := strings.TrimSpace(m[3]); loc != "" {
		f.File, f.Line = splitFileLine(loc)
	}
	if f.Module == "" && f.Symbol == "" && f.File == "" {
		return Frame{}, false
	}
	return f, true
}

// splitFileLine splits a "path\to\file.cpp:142" (or "[File:...][Line:..]") locator.
func splitFileLine(loc string) (string, int) {
	if fl := reFileLine.FindStringSubmatch(loc); fl != nil {
		n, _ := strconv.Atoi(fl[2])
		return normFile(fl[1]), n
	}
	if i := strings.LastIndex(loc, ":"); i > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(loc[i+1:])); err == nil {
			return normFile(loc[:i]), n
		}
	}
	return "", 0
}

// looksLikeFrame is a cheap pre-filter for log lines that could be stack frames
// (they carry a Module! token or a [file:line] locator).
func looksLikeFrame(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return strings.Contains(s, "!") || reFileLine.MatchString(s) ||
		(strings.Contains(s, ".cpp") || strings.Contains(s, ".h")) && strings.Contains(s, ":")
}

// stripLogPrefix removes UE's "[timestamp][frame]LogCat: Verbosity:" prefix so
// the message/frame text is bare. It is defensive: a line without a prefix is
// returned unchanged.
func stripLogPrefix(line string) string {
	s := line
	// Drop leading "[...][...]" timestamp/frame tags.
	for strings.HasPrefix(strings.TrimSpace(s), "[") {
		t := strings.TrimSpace(s)
		end := strings.IndexByte(t, ']')
		if end < 0 {
			break
		}
		s = t[end+1:]
	}
	// Drop a leading "LogCategory: Error: " / "LogCategory: Warning: " tag.
	if m := reLogCat.FindStringIndex(s); m != nil && m[0] == 0 {
		s = s[m[1]:]
	}
	// Drop the per-frame "[Callstack]" token UE prefixes every logged stack line
	// with (the trailing "[file:line]" locator is kept for parseFrame).
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[Callstack]")
	return strings.TrimSpace(s)
}

var reLogCat = regexp.MustCompile(`^\s*Log[\w]+:\s*(?:Error|Warning|Display|Fatal):\s*`)

func classifyKind(crashType, summary string) string {
	ct := strings.ToLower(strings.TrimSpace(crashType))
	switch {
	case strings.Contains(ct, "assert") || strings.Contains(summary, "Assertion failed"):
		return "assert"
	case strings.Contains(ct, "ensure") || strings.Contains(summary, "Ensure condition failed"):
		return "ensure"
	case strings.Contains(ct, "fatal") || strings.Contains(summary, "Fatal error"):
		return "fatal"
	}
	return "crash"
}

// fillOriginFromFrames sets File/Line from the first frame that has a source
// location, when the error message didn't carry one.
func (r *Report) fillOriginFromFrames() {
	if r.File != "" {
		return
	}
	for _, f := range r.Frames {
		if f.File != "" {
			r.File, r.Line = f.File, f.Line
			return
		}
	}
}

func normFile(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
}

func xmlUnescape(s string) string {
	rep := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&#xD;", "", "&#13;", "")
	return rep.Replace(s)
}
