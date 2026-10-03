package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
)

func TestContentPackage(t *testing.T) {
	for path, want := range map[string]string{
		"Content/Maps/L_Arena.umap":               "/Game/Maps/L_Arena",
		filepath.FromSlash("Content/BP/X.uasset"): "/Game/BP/X",
		"Plugins/Poly/Content/Mesh/SM.uasset":     "/Poly/Mesh/SM",
		"Source/Game/Hero.h":                      "",
		"Content/readme.txt":                      "",
	} {
		got, ok := contentPackage(path)
		if got != want || ok != (want != "") {
			t.Errorf("contentPackage(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
}

func TestCheckpointNumbers(t *testing.T) {
	if cpNum("umcp/cp/12") != 12 || cpNum("umcp/cp/x") != -1 || cpNum("v1.0") != -1 {
		t.Fatal("cpNum")
	}
}

func TestPollTimeoutKeepsAMargin(t *testing.T) {
	if got := pollTimeout(context.Background(), 0, 20*time.Second); got != 20*time.Second {
		t.Fatalf("no deadline: %s", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got := pollTimeout(ctx, 10, time.Second); got >= 2*time.Second || got < time.Second {
		t.Fatalf("deadline 2s: %s (must end before it, with a margin)", got)
	}
	short, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()
	if got := pollTimeout(short, 10, time.Second); got <= 0 || got >= 500*time.Millisecond {
		t.Fatalf("deadline 0.5s: %s (a short window still polls)", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if orStr("", "d") != "d" || orStr("x", "d") != "x" {
		t.Fatal("orStr")
	}
	if v := anyVec3([]any{1.0, 2.0}); v != [3]float64{1, 2, 0} {
		t.Fatalf("anyVec3 = %v", v)
	}
	if v := anyVec3("nope"); v != [3]float64{} {
		t.Fatalf("anyVec3 bad input = %v", v)
	}
	for in, want := range map[string]string{"gamestate": "@gamestate", "pawn": "@pawn", "Hero": "Hero"} {
		if beatTarget(in) != want {
			t.Errorf("beatTarget(%q)", in)
		}
	}
	if maxWidthOr(nil) != 1600 {
		t.Fatal("maxWidthOr default")
	}
	zero := 0
	if maxWidthOr(&zero) != 0 {
		t.Fatal("maxWidthOr explicit 0 = full resolution")
	}
	if tailStr("short") != "short" || len([]rune(tailStr(string(make([]byte, 5000))))) > 4001 {
		t.Fatal("tailStr")
	}
}

func TestStrictDecodeRejectsUnknownFields(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	if err := strictDecode([]byte(`{"a":1}`), &v); err != nil || v.A != 1 {
		t.Fatalf("valid input: %v", err)
	}
	err := strictDecode([]byte(`{"b":1}`), &v)
	if e, ok := err.(*envelope.Error); !ok || e.Code != envelope.InvalidArgument {
		t.Fatalf("unknown field = %v", err)
	}
}

func TestReadImageFileWaitsAndCleansUp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.png")
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(p, []byte("png"), 0o644)
	}()
	data, err := readImageFile(p, 3*time.Second)
	if err != nil || string(data) != "png" {
		t.Fatalf("readImageFile = %q, %v", data, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the read file must be removed")
	}
	if _, err := readImageFile(filepath.Join(t.TempDir(), "never.png"), 300*time.Millisecond); err == nil {
		t.Fatal("a file that never appears must time out")
	}
}

func TestSummarizeAutomationAndLastLines(t *testing.T) {
	out := "x\nLogAutomationController: Test Completed. Result={Passed} Name={A}\n" +
		"LogAutomationController: Test Completed. Result={Failed} Name={B}\n"
	s := summarizeAutomation(out)
	if s["passed"] != 1 || s["failed"] != 1 || s["ok"] != false {
		t.Fatalf("summary = %v", s)
	}
	if lastLines("a\nb\nc", 2) != "b\nc" {
		t.Fatal("lastLines")
	}
}
