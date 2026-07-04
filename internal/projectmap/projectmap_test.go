package projectmap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseUProject(t *testing.T) {
	data := []byte(`{
      "FileVersion": 3,
      "Modules": [
        {"Name": "AesirWaveDefense", "Type": "Runtime", "LoadingPhase": "Default"},
        {"Name": "AesirEditor", "Type": "Editor"}
      ]
    }`)
	mods := ParseUProject(data)
	if len(mods) != 2 || mods[0].Name != "AesirWaveDefense" || mods[0].Type != "Runtime" {
		t.Fatalf("modules = %+v", mods)
	}
}

func TestParseBuildCs(t *testing.T) {
	content := `public class AesirWaveDefense : ModuleRules {
      public AesirWaveDefense(ReadOnlyTargetRules Target) : base(Target) {
        PublicDependencyModuleNames.AddRange(new string[] { "Core", "CoreUObject", "Engine" });
        PrivateDependencyModuleNames.AddRange(new string[]{"Slate","SlateCore"});
      }
    }`
	deps := ParseBuildCs(content)
	want := map[string]bool{"Core": true, "CoreUObject": true, "Engine": true, "Slate": true, "SlateCore": true}
	if len(deps) != len(want) {
		t.Fatalf("deps = %v", deps)
	}
	for _, d := range deps {
		if !want[d] {
			t.Errorf("unexpected dep %q", d)
		}
	}
}

func TestScanHeaderClass(t *testing.T) {
	h := `#pragma once
#include "CoreMinimal.h"

UCLASS()
class AESIRWAVEDEFENSE_API AEnemyCharacter : public ACharacter
{
	GENERATED_BODY()
};

UENUM(BlueprintType)
enum class EWaveState : uint8
{
	WaitingToStart,
	InProgress,
};

USTRUCT()
struct FWaveConfig
{
	GENERATED_BODY()
};`
	types := ScanHeader(h, "AesirWaveDefense", "Source/AesirWaveDefense/EnemyCharacter.h")
	byName := map[string]TypeEntry{}
	for _, e := range types {
		byName[e.Name] = e
	}
	if e := byName["AEnemyCharacter"]; e.ScriptPath != "/Script/AesirWaveDefense.EnemyCharacter" || e.Kind != "class" || e.Parent != "ACharacter" {
		t.Errorf("AEnemyCharacter = %+v", e)
	}
	if e := byName["EWaveState"]; e.ScriptPath != "/Script/AesirWaveDefense.EWaveState" || e.Kind != "enum" {
		t.Errorf("EWaveState = %+v", e)
	}
	if e := byName["FWaveConfig"]; e.ScriptPath != "/Script/AesirWaveDefense.WaveConfig" || e.Kind != "struct" {
		t.Errorf("FWaveConfig = %+v", e)
	}
}

func TestReflectedName(t *testing.T) {
	for cpp, want := range map[string]string{
		"AEnemyCharacter": "EnemyCharacter", "UBuildComponent": "BuildComponent",
		"FWaveConfig": "WaveConfig", "EWaveState": "EWaveState", // enums keep their prefix
		"Foo": "Foo", "A": "A", // too short / no prefix -> unchanged
	} {
		if got := reflectedName(cpp); got != want {
			t.Errorf("reflectedName(%q) = %q, want %q", cpp, got, want)
		}
	}
}

func TestScanIntegration(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "Game.uproject"), `{"Modules":[{"Name":"Game","Type":"Runtime"}]}`)
	mustWrite(t, filepath.Join(dir, "Source", "Game", "Game.Build.cs"),
		`PublicDependencyModuleNames.AddRange(new string[]{"Core","Engine"});`)
	mustWrite(t, filepath.Join(dir, "Source", "Game", "Hero.h"),
		"UCLASS()\nclass GAME_API AHero : public APawn\n{\n};")
	m, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "Game.uproject" {
		t.Errorf("project = %q", m.Project)
	}
	if len(m.Modules) == 0 || m.Modules[0].Name != "Game" {
		t.Fatalf("modules = %+v", m.Modules)
	}
	if len(m.Modules[0].Dependencies) != 2 {
		t.Errorf("deps = %v", m.Modules[0].Dependencies)
	}
	var found bool
	for _, ty := range m.Types {
		if ty.Name == "AHero" && ty.ScriptPath == "/Script/Game.Hero" && ty.Module == "Game" && ty.Parent == "APawn" {
			found = true
		}
	}
	if !found {
		t.Errorf("AHero not mapped: %+v", m.Types)
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
