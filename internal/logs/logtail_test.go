package logs

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleLog = `[2026.07.02-16.00.00:000][  0]LogInit: Display: Running engine
[2026.07.02-16.00.01:000][  1]LogLiveCoding: Display: Starting Live Coding compile
[2026.07.02-16.00.02:000][  2]LogStreaming: Warning: Failed to load /Game/Missing
[2026.07.02-16.00.03:000][  3]LogPython: Error: Traceback boom
[2026.07.02-16.00.04:000][  4]LogTemp: Log: chatty line`

func TestLineSeverity(t *testing.T) {
	cases := map[string]string{
		"LogStreaming: Warning: x": "warning",
		"LogPython: Error: y":      "error",
		"LogInit: Display: z":      "display",
		"LogTemp: Log: q":          "log",
	}
	for line, want := range cases {
		if got := LineSeverity(line); got != want {
			t.Errorf("LineSeverity(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestFilterLinesBySeverity(t *testing.T) {
	warn := FilterLines(sampleLog, "Warning", nil)
	if len(warn) != 2 {
		t.Fatalf("want 2 warning+error lines, got %d: %v", len(warn), warn)
	}
	errs, warns, _ := CountBySeverity(warn)
	if errs != 1 || warns != 1 {
		t.Fatalf("counts wrong: errs=%d warns=%d", errs, warns)
	}
}

func TestFilterLinesByCategory(t *testing.T) {
	lc := FilterLines(sampleLog, "Verbose", []string{"LogLiveCoding"})
	if len(lc) != 1 {
		t.Fatalf("want 1 LogLiveCoding line, got %d: %v", len(lc), lc)
	}
}

func TestReadFromOffsetAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, off, err := ReadFrom(path, 0)
	if err != nil || off != 6 {
		t.Fatalf("first read off=%d err=%v", off, err)
	}
	// append
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("world\n")
	f.Close()
	chunk, off2, _ := ReadFrom(path, off)
	if chunk != "world\n" || off2 != 12 {
		t.Fatalf("incremental read chunk=%q off=%d", chunk, off2)
	}
	// rotation: offset beyond size -> read from start
	chunk3, _, _ := ReadFrom(path, 9999)
	if len(chunk3) == 0 {
		t.Fatal("expected full re-read after rotation")
	}
}

func TestLogPathFindsLog(t *testing.T) {
	dir := t.TempDir()
	if LogPath(dir) != "" {
		t.Fatal("expected empty LogPath when no Saved/Logs")
	}
	logs := filepath.Join(dir, "Saved", "Logs")
	os.MkdirAll(logs, 0o755)
	os.WriteFile(filepath.Join(logs, "Project.log"), []byte("x"), 0o644)
	if got := LogPath(dir); filepath.Base(got) != "Project.log" {
		t.Fatalf("LogPath = %q, want .../Project.log", got)
	}
}
