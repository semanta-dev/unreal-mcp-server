package scenespec

import (
	"reflect"
	"testing"
)

func TestDiffClassification(t *testing.T) {
	plan := Plan{SceneID: "arena", Placements: []Placement{
		{Label: "arena.keep", StaticMeshPath: "/Game/SM_A"},
		{Label: "arena.new", StaticMeshPath: "/Game/SM_A"}, // duplicate asset ref
		{Label: "arena.hero", ClassPath: "/Game/BP_Hero"},
	}}
	before := map[string]SnapActor{
		"arena.keep": {Class: "StaticMeshActor"}, // present in plan -> Update
		"arena.hero": {Class: "BP_Hero_C"},       // present in plan -> Update
		"arena.gone": {Class: "StaticMeshActor"}, // scene-scoped, absent -> Prune
		"other.x":    {Class: "StaticMeshActor"}, // different scene -> ignored
	}

	res := Diff("arena", plan, before)

	// Add preserves plan order.
	if !reflect.DeepEqual(res.Add, []string{"arena.new"}) {
		t.Fatalf("Add = %v", res.Add)
	}
	// Update preserves plan order.
	if !reflect.DeepEqual(res.Update, []string{"arena.keep", "arena.hero"}) {
		t.Fatalf("Update = %v", res.Update)
	}
	// Prune is scene-scoped and sorted; other.x is excluded.
	if !reflect.DeepEqual(res.Prune, []string{"arena.gone"}) {
		t.Fatalf("Prune = %v", res.Prune)
	}
	// MissingAssetRefs dedups /Game/SM_A and keeps first-seen order.
	if !reflect.DeepEqual(res.MissingAssetRefs, []string{"/Game/SM_A", "/Game/BP_Hero"}) {
		t.Fatalf("MissingAssetRefs = %v", res.MissingAssetRefs)
	}
}

func TestDiffPrunePrefixBoundary(t *testing.T) {
	// "arena2.x" must NOT be pruned by scene "arena" (prefix is "arena.").
	plan := Plan{SceneID: "arena"}
	before := map[string]SnapActor{
		"arena.a":  {},
		"arena2.x": {},
		"arena":    {}, // exact scene id without dot -> not scoped
	}
	res := Diff("arena", plan, before)
	if !reflect.DeepEqual(res.Prune, []string{"arena.a"}) {
		t.Fatalf("Prune = %v, want [arena.a]", res.Prune)
	}
}

func TestDiffEmptyBefore(t *testing.T) {
	plan := Plan{SceneID: "s", Placements: []Placement{{Label: "s.a"}, {Label: "s.b"}}}
	res := Diff("s", plan, nil)
	if !reflect.DeepEqual(res.Add, []string{"s.a", "s.b"}) {
		t.Fatalf("Add = %v", res.Add)
	}
	if len(res.Update) != 0 || len(res.Prune) != 0 {
		t.Fatalf("expected empty update/prune, got %v %v", res.Update, res.Prune)
	}
}
