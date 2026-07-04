package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, dir, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetGameModeCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	if err := SetGameMode(dir, "/Game/BP/BP_GM.BP_GM_C"); err != nil {
		t.Fatal(err)
	}
	c := read(t, dir, "DefaultEngine.ini")
	if !strings.Contains(c, "[/Script/EngineSettings.GameMapsSettings]") ||
		!strings.Contains(c, "GlobalDefaultGameMode=/Game/BP/BP_GM.BP_GM_C") {
		t.Fatalf("gamemode not set:\n%s", c)
	}
	// Re-set to a different value -> single line, replaced not duplicated.
	if err := SetGameMode(dir, "/Game/BP/BP_Other.BP_Other_C"); err != nil {
		t.Fatal(err)
	}
	c = read(t, dir, "DefaultEngine.ini")
	if strings.Count(c, "GlobalDefaultGameMode=") != 1 || !strings.Contains(c, "BP_Other") {
		t.Fatalf("expected one replaced gamemode line:\n%s", c)
	}
}

func TestActionMappingIdempotentAndRebind(t *testing.T) {
	dir := t.TempDir()
	m := ActionMapping{Name: "Dash", Key: "LeftShift"}
	if err := AddActionMapping(dir, m); err != nil {
		t.Fatal(err)
	}
	if err := AddActionMapping(dir, m); err != nil { // identical -> no dup
		t.Fatal(err)
	}
	c := read(t, dir, "DefaultInput.ini")
	if strings.Count(c, `ActionName="Dash"`) != 1 {
		t.Fatalf("duplicate Dash mapping:\n%s", c)
	}
	if !strings.Contains(c, `+ActionMappings=(ActionName="Dash",bShift=False,bCtrl=False,bAlt=False,bCmd=False,Key=LeftShift)`) {
		t.Fatalf("action line wrong:\n%s", c)
	}
	// Rebind Dash to a different key -> replace, still one line.
	if err := AddActionMapping(dir, ActionMapping{Name: "Dash", Key: "SpaceBar"}); err != nil {
		t.Fatal(err)
	}
	c = read(t, dir, "DefaultInput.ini")
	if strings.Count(c, `ActionName="Dash"`) != 1 || !strings.Contains(c, "Key=SpaceBar") {
		t.Fatalf("rebind should replace:\n%s", c)
	}
}

func TestAxisMappingAndTag(t *testing.T) {
	dir := t.TempDir()
	if err := AddAxisMapping(dir, AxisMapping{Name: "MoveForward", Key: "W", Scale: 1.0}); err != nil {
		t.Fatal(err)
	}
	if err := AddAxisMapping(dir, AxisMapping{Name: "MoveForward", Key: "S", Scale: -1.0}); err != nil {
		t.Fatal(err)
	}
	c := read(t, dir, "DefaultInput.ini")
	// Different keys for the same axis are distinct bindings (both kept).
	if strings.Count(c, `AxisName="MoveForward"`) != 2 {
		t.Fatalf("both axis bindings should exist:\n%s", c)
	}
	if !strings.Contains(c, `Scale=1,Key=W`) || !strings.Contains(c, `Scale=-1,Key=S`) {
		t.Fatalf("axis lines wrong:\n%s", c)
	}
	if err := AddGameplayTag(dir, "Ability.Dash", "dash ability"); err != nil {
		t.Fatal(err)
	}
	if err := AddGameplayTag(dir, "Ability.Dash", "dash ability"); err != nil {
		t.Fatal(err)
	}
	tc := read(t, dir, "DefaultGameplayTags.ini")
	if strings.Count(tc, `Tag="Ability.Dash"`) != 1 {
		t.Fatalf("duplicate tag:\n%s", tc)
	}
}

func TestPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	seed := "[/Script/Engine.InputSettings]\n+ActionMappings=(ActionName=\"Jump\",Key=SpaceBar)\n\n[OtherSection]\nFoo=Bar\n"
	if err := os.WriteFile(filepath.Join(dir, "DefaultInput.ini"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddActionMapping(dir, ActionMapping{Name: "Dash", Key: "LeftShift"}); err != nil {
		t.Fatal(err)
	}
	c := read(t, dir, "DefaultInput.ini")
	if !strings.Contains(c, `ActionName="Jump"`) || !strings.Contains(c, "Foo=Bar") || !strings.Contains(c, `ActionName="Dash"`) {
		t.Fatalf("clobbered existing content:\n%s", c)
	}
}
