// events (merged into package logs) tails an NDJSON event stream the editor appends to
// Saved/PyMCP/events.ndjson (PIE-end, asset import, log errors/ensures). It reads
// by byte offset — like logtail — so an autonomous agent can observe editor
// events via a cheap file read EVEN WHILE the single-flight command channel is
// blocked by a long op, and react (e.g. abort a run the instant an ensure fires).
package logs

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// Event is one parsed NDJSON record. Common fields the recorder writes are
// surfaced; the whole record is kept in Raw.
type Event struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// Path is the conventional event-stream path for a project.
func Path(projectDir string) string {
	return filepath.Join(projectDir, "Saved", "PyMCP", "events.ndjson")
}

// EventsSize returns the current byte length of the stream (a marker for a later Tail),
// or 0 if it doesn't exist yet.
func EventsSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Tail reads NDJSON records appended since `offset` and returns them plus the new
// offset. A file shorter than `offset` (rotated/truncated) restarts from 0. A
// missing file yields no events (not an error).
func Tail(path string, offset int64) ([]Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	size := fi.Size()
	if offset < 0 || offset > size {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}

	// Only consume COMPLETE lines (through the last newline actually read). Bytes
	// after the last '\n' are a still-being-written record: left unconsumed so the
	// next Tail re-reads and delivers it once complete. The new offset is derived
	// from what we consumed from `offset` — NOT the pre-read Stat size — so a
	// concurrent append between Stat and ReadAll can neither drop nor duplicate a
	// record.
	var consumed int64
	var region []byte
	if nl := bytes.LastIndexByte(data, '\n'); nl >= 0 {
		region = data[:nl+1]
		consumed = int64(nl + 1)
	} // else: no complete line yet — region stays nil, consumed stays 0

	var out []Event
	for _, line := range bytes.Split(region, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var typed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &typed); err != nil {
			continue // skip a malformed line (its bytes are still counted as consumed)
		}
		out = append(out, Event{Type: typed.Type, Raw: append(json.RawMessage(nil), line...)})
	}
	return out, offset + consumed, nil
}

// CountByType tallies events by their "type" field (e.g. how many ensures).
func CountByType(evs []Event) map[string]int {
	m := map[string]int{}
	for _, e := range evs {
		m[e.Type]++
	}
	return m
}
