package crash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An assert dump: UE writes the [File:Line] INSIDE <ErrorMessage>, and the
// runtime-xml <CallStack> is bare module names (not symbolicated) — realistic.
const assertCrashContext = `<?xml version="1.0" encoding="UTF-8"?>
<FGenericCrashContext>
	<RuntimeProperties>
		<CrashType>Assert</CrashType>
		<ErrorMessage>Assertion failed: Target != nullptr [File:D:/proj/Source/AesirWaveDefense/EnemyCharacter.cpp] [Line: 142] </ErrorMessage>
		<CallStack>UnrealEditor-AesirWaveDefense.dll
UnrealEditor-Engine.dll
UnrealEditor-Core.dll</CallStack>
	</RuntimeProperties>
</FGenericCrashContext>`

func TestParseCrashContextAssert(t *testing.T) {
	r := ParseCrashContext(assertCrashContext)
	if r == nil {
		t.Fatal("expected a report")
	}
	if r.Kind != "assert" {
		t.Errorf("kind = %q, want assert", r.Kind)
	}
	// For an assert the origin comes from the ErrorMessage, not the (unsymbolicated) CallStack.
	if r.File != "D:/proj/Source/AesirWaveDefense/EnemyCharacter.cpp" || r.Line != 142 {
		t.Errorf("origin = %s:%d, want EnemyCharacter.cpp:142", r.File, r.Line)
	}
}

func TestParseCrashContextNotACrash(t *testing.T) {
	if r := ParseCrashContext("<xml>no error here</xml>"); r != nil {
		t.Fatalf("expected nil for non-crash xml, got %+v", r)
	}
}

func TestFromCrashDir(t *testing.T) {
	proj := t.TempDir()
	base := time.Now()
	oldDir := filepath.Join(proj, "Saved", "Crashes", "UECC-Windows-OLD")
	mustWrite(t, filepath.Join(oldDir, "CrashContext.runtime-xml"), assertCrashContext)
	old := base.Add(-time.Hour)
	_ = os.Chtimes(oldDir, old, old)
	newDir := filepath.Join(proj, "Saved", "Crashes", "UECC-Windows-NEW")
	mustWrite(t, filepath.Join(newDir, "CrashContext.runtime-xml"), assertCrashContext)

	r, err := FromCrashDir(proj, base.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Line != 142 || r.Source != newDir {
		t.Fatalf("expected the newest crash at 142 from %s, got %+v", newDir, r)
	}
	if r2, _ := FromCrashDir(proj, base.Add(time.Hour)); r2 != nil {
		t.Errorf("expected no crash newer than a future `since`, got %+v", r2)
	}
}

func TestFromCrashDirNoDir(t *testing.T) {
	r, err := FromCrashDir(t.TempDir(), time.Now())
	if err != nil || r != nil {
		t.Fatalf("expected (nil,nil) when no Saved/Crashes, got (%v,%v)", r, err)
	}
}

// A real UE5 access-violation log: a "=== Critical error: ===" banner, the cause
// on a following line (no [File:Line]), then "[Callstack]"-prefixed symbolicated
// frames. This is the P1 headline scenario (a null-deref in Tick).
const accessViolationLog = `[2026.07.03-05.18.20:123][ 42]LogTemp: Display: starting wave 2
[2026.07.03-05.18.21:001][ 43]LogWindows: Error: === Critical error: ===
[2026.07.03-05.18.21:001][ 43]LogWindows: Error:
[2026.07.03-05.18.21:001][ 43]LogWindows: Error: Unhandled Exception: EXCEPTION_ACCESS_VIOLATION reading address 0x0000000000000000
[2026.07.03-05.18.21:001][ 43]LogWindows: Error:
[2026.07.03-05.18.21:002][ 43]LogWindows: Error: [Callstack] 0x00007ff6abcd1234 UnrealEditor-AesirWaveDefense.dll!AEnemyCharacter::Tick() [D:\proj\Source\AesirWaveDefense\EnemyCharacter.cpp:142]
[2026.07.03-05.18.21:002][ 43]LogWindows: Error: [Callstack] 0x00007ff6def05678 UnrealEditor-Engine.dll!AActor::TickActor() [D:\engine\Actor.cpp:900]
[2026.07.03-05.18.21:050][ 43]LogTemp: Display: unrelated`

func TestScanLogAccessViolation(t *testing.T) {
	r := ScanLog(accessViolationLog)
	if r == nil {
		t.Fatal("expected a report")
	}
	// The banner must NOT be the summary; the real cause line is pulled forward.
	if strings.HasPrefix(r.Summary, "===") || !strings.Contains(r.Summary, "EXCEPTION_ACCESS_VIOLATION") {
		t.Errorf("summary should be the exception line, got %q", r.Summary)
	}
	// Origin comes from the [Callstack]-prefixed symbolicated frame (the whole point).
	if r.File != "D:/proj/Source/AesirWaveDefense/EnemyCharacter.cpp" || r.Line != 142 {
		t.Fatalf("origin = %s:%d, want EnemyCharacter.cpp:142 (frame parse of [Callstack] line)", r.File, r.Line)
	}
	if len(r.Frames) < 2 {
		t.Fatalf("want >=2 frames from [Callstack] lines, got %d: %+v", len(r.Frames), r.Frames)
	}
	if r.Frames[0].Symbol != "AEnemyCharacter::Tick" || r.Frames[0].Module != "UnrealEditor-AesirWaveDefense.dll" {
		t.Errorf("frame0 = %+v", r.Frames[0])
	}
}

func TestScanLogAssertion(t *testing.T) {
	log := `[2026.07.03-05.18.21:001][ 43]LogWindows: Error: Assertion failed: Target != nullptr [File:D:/proj/Source/EnemyCharacter.cpp] [Line: 142]
[2026.07.03-05.18.21:002][ 43]LogWindows: Error: [Callstack] 0x00007ff6abcd UnrealEditor-AesirWaveDefense.dll!AEnemyCharacter::Tick() [EnemyCharacter.cpp:142]`
	r := ScanLog(log)
	if r == nil || r.Kind != "assert" {
		t.Fatalf("assert not parsed: %+v", r)
	}
	if r.File != "D:/proj/Source/EnemyCharacter.cpp" || r.Line != 142 {
		t.Errorf("origin = %s:%d", r.File, r.Line)
	}
	if len(r.Frames) == 0 {
		t.Error("expected a parsed frame from the [Callstack] line")
	}
}

func TestScanLogFatal(t *testing.T) {
	log := "[..]LogCore: Fatal error: [File:Foo.cpp] [Line: 7] out of memory"
	r := ScanLog(log)
	if r == nil || r.Kind != "fatal" || r.Line != 7 {
		t.Fatalf("fatal not parsed: %+v", r)
	}
}

func TestScanLogNone(t *testing.T) {
	if r := ScanLog("[..]LogTemp: Display: all good\n[..]LogTemp: Warning: minor"); r != nil {
		t.Fatalf("expected nil for a clean log, got %+v", r)
	}
}

func TestParseFrameRealFormats(t *testing.T) {
	cases := []struct {
		in     string
		module string
		symbol string
		file   string
		line   int
	}{
		{"0x00007ff6abcd UnrealEditor-Game.dll!AEnemyCharacter::Tick() [D:\\p\\EnemyCharacter.cpp:142]",
			"UnrealEditor-Game.dll", "AEnemyCharacter::Tick", "D:/p/EnemyCharacter.cpp", 142},
		{"UnrealEditor-Core.dll!0x00007ff600112233", "UnrealEditor-Core.dll", "0x00007ff600112233", "", 0},
	}
	for _, c := range cases {
		f, ok := parseFrame(c.in)
		if !ok {
			t.Fatalf("failed to parse %q", c.in)
		}
		if f.Module != c.module || f.File != c.file || f.Line != c.line {
			t.Errorf("parseFrame(%q) = %+v", c.in, f)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
