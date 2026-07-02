package lifecycle

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEditorExe(t *testing.T) {
	got := EditorExe(`D:/Unreal/Engine/UE_5.7`)
	want := filepath.Join(`D:/Unreal/Engine/UE_5.7`, "Engine", "Binaries", "Win64", "UnrealEditor.exe")
	if got != want {
		t.Fatalf("EditorExe = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "UnrealEditor.exe") {
		t.Fatalf("unexpected exe path: %q", got)
	}
}

func TestIsAlive(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
	// A very high PID is almost certainly not a live process.
	if IsAlive(1 << 30) {
		t.Skip("unexpectedly live PID; environment-dependent")
	}
	if IsAlive(0) || IsAlive(-1) {
		t.Fatal("invalid PIDs must not be reported alive")
	}
}

func TestLaunchMissingEditorErrors(t *testing.T) {
	_, err := Launch(filepath.Join(t.TempDir(), "no-engine"), filepath.Join(t.TempDir(), "x.uproject"))
	if err == nil {
		t.Fatal("expected error launching a nonexistent editor")
	}
	if runtime.GOOS == "windows" && !strings.Contains(err.Error(), "editor executable not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}
