package build

import "testing"

func TestParseDiagnosticsMSVC(t *testing.T) {
	out := `Building AesirWaveDefenseEditor...
C:\game\Source\AesirWaveDefense\AesirCharacter.cpp(142): error C2065: 'Healthh': undeclared identifier
C:\game\Source\AesirWaveDefense\AesirWeapon.cpp(88,12): warning C4100: 'DeltaTime': unreferenced formal parameter
  AesirGameMode.gen.cpp
error LNK2019: unresolved external symbol "public: void __cdecl AWave::Start(void)" referenced in function main`
	ds := ParseDiagnostics(out)
	if len(ds) != 3 {
		t.Fatalf("want 3 diagnostics, got %d: %+v", len(ds), ds)
	}
	if ds[0].Tool != "MSVC" || ds[0].Severity != SevError || ds[0].Code != "C2065" || ds[0].Line != 142 {
		t.Fatalf("bad first diag: %+v", ds[0])
	}
	if ds[1].Severity != SevWarning || ds[1].Column != 12 {
		t.Fatalf("bad warning diag: %+v", ds[1])
	}
	if ds[2].Tool != "Linker" || ds[2].Code != "LNK2019" {
		t.Fatalf("bad linker diag: %+v", ds[2])
	}
	if CountErrors(ds) != 2 {
		t.Fatalf("want 2 errors, got %d", CountErrors(ds))
	}
}

func TestParseDiagnosticsUHT(t *testing.T) {
	out := `C:\game\Source\AesirWaveDefense\EnemyCharacter.h(30): Error: Unrecognized type 'FBrokenType' - type must be a UCLASS, USTRUCT or UENUM`
	ds := ParseDiagnostics(out)
	if len(ds) != 1 || ds[0].Tool != "UHT" || ds[0].Severity != SevError || ds[0].Line != 30 {
		t.Fatalf("bad UHT parse: %+v", ds)
	}
}

func TestParseDiagnosticsDedup(t *testing.T) {
	line := `C:\a\B.cpp(1): error C2001: newline in constant`
	ds := ParseDiagnostics(line + "\n" + line + "\n" + line)
	if len(ds) != 1 {
		t.Fatalf("expected dedup to 1, got %d", len(ds))
	}
}

func TestClassifyFullOnNewFile(t *testing.T) {
	changed := []FileChange{{Status: 'A', Path: "Source/AesirWaveDefense/NewThing.cpp"}}
	s, reason := ClassifyStrategy(changed, "")
	if s != StrategyFull {
		t.Fatalf("added source should be full, got %s (%s)", s, reason)
	}
}

func TestClassifyFullOnBuildCs(t *testing.T) {
	changed := []FileChange{{Status: 'M', Path: "Source/AesirWaveDefense/AesirWaveDefense.Build.cs"}}
	if s, _ := ClassifyStrategy(changed, "+ PublicDependencyModuleNames.Add(\"AIModule\");"); s != StrategyFull {
		t.Fatalf("Build.cs change should be full, got %s", s)
	}
}

func TestClassifyFullOnReflectionChange(t *testing.T) {
	diff := `@@ -10,6 +10,7 @@ class AEnemy
 	int32 Health;
+	UPROPERTY(EditAnywhere)
+	float Speed;
 	void Tick();`
	changed := []FileChange{{Status: 'M', Path: "Source/AesirWaveDefense/EnemyCharacter.h"}}
	if s, reason := ClassifyStrategy(changed, diff); s != StrategyFull {
		t.Fatalf("reflection change should be full, got %s (%s)", s, reason)
	}
}

func TestClassifyLiveCodingOnBodyOnly(t *testing.T) {
	diff := `@@ -20,7 +20,7 @@ void AEnemy::Tick(float Dt)
-	Health -= 1;
+	Health -= 2;`
	changed := []FileChange{{Status: 'M', Path: "Source/AesirWaveDefense/EnemyCharacter.cpp"}}
	if s, reason := ClassifyStrategy(changed, diff); s != StrategyLiveCoding {
		t.Fatalf("body-only change should be livecoding, got %s (%s)", s, reason)
	}
}

func TestReflectionTouchedIgnoresContext(t *testing.T) {
	// A UPROPERTY on an unchanged context line (leading space) must NOT trigger full.
	diff := ` 	UPROPERTY(EditAnywhere)
-	float OldSpeed = 1;
+	float OldSpeed = 2;`
	if ReflectionTouchedInDiff(diff) {
		t.Fatal("unchanged UPROPERTY context line should not count as a reflection change")
	}
}

func TestParseNameStatus(t *testing.T) {
	out := "M\tSource/A.cpp\nA\tSource/B.h\nR100\tSource/Old.cpp\tSource/New.cpp"
	cs := ParseNameStatus(out)
	if len(cs) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(cs), cs)
	}
	if cs[2].Status != 'R' || cs[2].Path != "Source/New.cpp" {
		t.Fatalf("rename parse wrong: %+v", cs[2])
	}
}
