package logs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTailIncremental(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.ndjson")
	write(t, p, `{"type":"pie_start","t":1}
{"type":"ensure","msg":"x"}
`)
	evs, off, err := Tail(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[1].Type != "ensure" {
		t.Fatalf("events = %+v", evs)
	}
	// Append more; Tail from the marker returns only the new records.
	appendTo(t, p, `{"type":"pie_end"}`+"\n")
	evs2, off2, _ := Tail(p, off)
	if len(evs2) != 1 || evs2[0].Type != "pie_end" {
		t.Fatalf("incremental events = %+v", evs2)
	}
	if off2 <= off {
		t.Errorf("offset did not advance: %d -> %d", off, off2)
	}
}

func TestTailMissingFile(t *testing.T) {
	evs, off, err := Tail(filepath.Join(t.TempDir(), "nope.ndjson"), 0)
	if err != nil || evs != nil || off != 0 {
		t.Fatalf("missing file: %v %v %d", evs, err, off)
	}
}

func TestTailTruncationRestarts(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.ndjson")
	write(t, p, `{"type":"a"}`+"\n")
	// Marker beyond the (rewritten, shorter) file -> restart from 0.
	evs, _, _ := Tail(p, 9999)
	if len(evs) != 1 || evs[0].Type != "a" {
		t.Fatalf("truncation restart failed: %+v", evs)
	}
}

func TestTailSkipsPartialLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.ndjson")
	write(t, p, `{"type":"ok"}`+"\n"+`{"type":"half`) // no closing brace/newline
	evs, _, _ := Tail(p, 0)
	if len(evs) != 1 || evs[0].Type != "ok" {
		t.Fatalf("should skip the partial line: %+v", evs)
	}
}

// The partial trailing line must be re-delivered EXACTLY ONCE once completed —
// the offset must not have advanced past it (the Tail offset-bug regression).
func TestTailPartialLineCompletedLater(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.ndjson")
	write(t, p, `{"type":"ok"}`+"\n"+`{"type":"pending"`) // partial second line
	evs, off, _ := Tail(p, 0)
	if len(evs) != 1 || evs[0].Type != "ok" {
		t.Fatalf("first read should yield only the complete line: %+v", evs)
	}
	// Complete the pending line + append another.
	appendTo(t, p, "}\n"+`{"type":"after"}`+"\n")
	evs2, _, _ := Tail(p, off)
	if len(evs2) != 2 || evs2[0].Type != "pending" || evs2[1].Type != "after" {
		t.Fatalf("completed line must be delivered exactly once: %+v", evs2)
	}
}

func TestCountByType(t *testing.T) {
	evs := []Event{{Type: "ensure"}, {Type: "ensure"}, {Type: "pie_end"}}
	m := CountByType(evs)
	if m["ensure"] != 2 || m["pie_end"] != 1 {
		t.Fatalf("counts = %v", m)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}
