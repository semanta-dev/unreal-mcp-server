package scenespec

import (
	"math"
	"testing"
)

// --- shared test helpers ---

const eps = 1e-9

func approx(a, b float64) bool { return math.Abs(a-b) <= eps }

func approxVec(t *testing.T, got, want [3]float64) {
	t.Helper()
	if !approx(got[0], want[0]) || !approx(got[1], want[1]) || !approx(got[2], want[2]) {
		t.Fatalf("vec = %v, want %v", got, want)
	}
}

// findDiag reports whether diags contains one matching field and severity.
func findDiag(diags []Diagnostic, severity, field string) bool {
	for _, d := range diags {
		if d.Severity == severity && d.Field == field {
			return true
		}
	}
	return false
}

func hasSeverity(diags []Diagnostic, severity string) bool {
	for _, d := range diags {
		if d.Severity == severity {
			return true
		}
	}
	return false
}

func placementByLabel(p Plan, label string) (Placement, bool) {
	for _, pl := range p.Placements {
		if pl.Label == label {
			return pl, true
		}
	}
	return Placement{}, false
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// --- Parse ---

const validSpec = `{
  "schema": "unreal.scene/v1",
  "scene_id": "arena01",
  "defaults": {"folder": "MCP/arena01", "scale": [2, 2, 2]},
  "environment": {"preset": "daytime_clear"},
  "prefabs": {
    "torch": {"members": [
      {"role": "base", "kind": "static_mesh", "static_mesh_path": "/Game/SM_Base"},
      {"role": "flame", "kind": "static_mesh", "static_mesh_path": "/Game/SM_Flame", "location": [0, 0, 100]}
    ]}
  },
  "actors": [
    {"label": "hero", "kind": "class", "class_path": "/Game/BP_Hero", "location": [10, 20, 30]}
  ],
  "groups": [
    {"name": "pillars", "kind": "static_mesh", "static_mesh_path": "/Game/SM_Pillar",
     "layout": {"type": "grid", "count": 4, "spacing": 200}}
  ],
  "cameras": [{"name": "hero_cam", "targets": ["arena01.hero"], "fov_deg": 60}],
  "checks": {"require_environment_lit": true, "require_no_missing_meshes": true}
}`

func TestParseValidRoundTrips(t *testing.T) {
	s, diags, err := Parse([]byte(validSpec))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if hasSeverity(diags, "error") {
		t.Fatalf("unexpected error diagnostics: %+v", diags)
	}
	if s.Schema != Schema || s.SceneID != "arena01" {
		t.Fatalf("schema/scene_id mismatch: %q %q", s.Schema, s.SceneID)
	}
	if s.Defaults.Folder != "MCP/arena01" || s.Defaults.Scale == nil || (*s.Defaults.Scale)[0] != 2 {
		t.Fatalf("defaults not decoded: %+v", s.Defaults)
	}
	if s.Environment == nil || s.Environment.Preset != "daytime_clear" {
		t.Fatalf("environment not decoded: %+v", s.Environment)
	}
	if len(s.Prefabs["torch"].Members) != 2 {
		t.Fatalf("prefab members: %+v", s.Prefabs)
	}
	if len(s.Actors) != 1 || s.Actors[0].Label != "hero" {
		t.Fatalf("actors: %+v", s.Actors)
	}
	if len(s.Groups) != 1 || s.Groups[0].Layout.Type != "grid" || s.Groups[0].Layout.Count != 4 {
		t.Fatalf("groups: %+v", s.Groups)
	}
	if len(s.Cameras) != 1 || !s.Checks.RequireEnvironmentLit {
		t.Fatalf("cameras/checks: %+v %+v", s.Cameras, s.Checks)
	}
}

func TestParseStructuralError(t *testing.T) {
	if _, _, err := Parse([]byte(`{not json`)); err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestParseSemanticDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		severity string
		field    string
	}{
		{"bad schema", `{"schema":"nope","scene_id":"a"}`, "error", "schema"},
		{"missing scene_id", `{"schema":"unreal.scene/v1"}`, "error", "scene_id"},
		{"bad scene_id", `{"schema":"unreal.scene/v1","scene_id":"Arena 01"}`, "error", "scene_id"},
		{"unknown preset", `{"schema":"unreal.scene/v1","scene_id":"a","environment":{"preset":"space"}}`, "error", "environment.preset"},
		{"missing preset", `{"schema":"unreal.scene/v1","scene_id":"a","environment":{}}`, "error", "environment.preset"},
		{"unknown actor kind", `{"schema":"unreal.scene/v1","scene_id":"a","actors":[{"label":"x","kind":"widget"}]}`, "error", "actors[0].kind"},
		{"missing actor label", `{"schema":"unreal.scene/v1","scene_id":"a","actors":[{"kind":"class"}]}`, "error", "actors[0].label"},
		{"unknown layout", `{"schema":"unreal.scene/v1","scene_id":"a","groups":[{"name":"g","kind":"static_mesh","layout":{"type":"spiral"}}]}`, "error", "groups[0].layout.type"},
		{"missing group kind", `{"schema":"unreal.scene/v1","scene_id":"a","groups":[{"name":"g","layout":{"type":"grid"}}]}`, "error", "groups[0].kind"},
		{"prefab member missing role", `{"schema":"unreal.scene/v1","scene_id":"a","prefabs":{"p":{"members":[{"kind":"static_mesh"}]}}}`, "error", "prefabs[p].members[0].role"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, diags, err := Parse([]byte(tc.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !findDiag(diags, tc.severity, tc.field) {
				t.Fatalf("missing diagnostic %s/%s; got %+v", tc.severity, tc.field, diags)
			}
		})
	}
}

func TestParsePrefabGroupOmitsKind(t *testing.T) {
	// A group that instantiates a prefab need not carry its own kind.
	body := `{"schema":"unreal.scene/v1","scene_id":"a",
	  "prefabs":{"p":{"members":[{"role":"r","kind":"static_mesh"}]}},
	  "groups":[{"name":"g","use":"p","layout":{"type":"grid","count":1}}]}`
	_, diags, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if hasSeverity(diags, "error") {
		t.Fatalf("unexpected errors: %+v", diags)
	}
}

// --- Compile ---

func TestCompileDefaultsLabelsTags(t *testing.T) {
	s, _, err := Parse([]byte(validSpec))
	if err != nil {
		t.Fatal(err)
	}
	plan, diags := Compile(s)
	if hasSeverity(diags, "error") {
		t.Fatalf("unexpected compile errors: %+v", diags)
	}
	if plan.SceneID != "arena01" {
		t.Fatalf("scene id: %q", plan.SceneID)
	}

	// Explicit actor label + scene tag + folder + no scale override => default [2,2,2].
	hero, ok := placementByLabel(plan, "arena01.hero")
	if !ok {
		t.Fatal("missing arena01.hero")
	}
	if !hasTag(hero.Tags, "mcp_scene:arena01") {
		t.Fatalf("hero tags: %v", hero.Tags)
	}
	if hero.Folder != "MCP/arena01" {
		t.Fatalf("hero folder: %q", hero.Folder)
	}
	approxVec(t, hero.Scale, [3]float64{2, 2, 2})
	approxVec(t, hero.Location, [3]float64{10, 20, 30})

	// Group grid produces 4 zero-padded single-actor labels (width 1 for count 4).
	for _, lbl := range []string{"arena01.pillars.0", "arena01.pillars.1", "arena01.pillars.2", "arena01.pillars.3"} {
		if _, ok := placementByLabel(plan, lbl); !ok {
			t.Fatalf("missing group placement %q", lbl)
		}
	}

	// Environment folded in: the sun placement exists and is scene-scoped.
	sun, ok := placementByLabel(plan, "arena01.env.sun")
	if !ok {
		t.Fatal("missing arena01.env.sun")
	}
	if sun.Kind != "directional_light" || sun.RotationPyr[0] >= 0 {
		t.Fatalf("bad sun: %+v", sun)
	}
	if sun.Folder != "MCP/arena01" || !hasTag(sun.Tags, "mcp_scene:arena01") {
		t.Fatalf("sun not scoped: %+v", sun)
	}
}

func TestCompileZeroPadWidth(t *testing.T) {
	s := &Spec{Schema: Schema, SceneID: "s", Groups: []Group{{
		Name: "g", Kind: "static_mesh",
		Layout: Layout{Type: "grid", Rows: 4, Cols: 3, Spacing: 100}, // 12 instances
	}}}
	plan, _ := Compile(s)
	if _, ok := placementByLabel(plan, "s.g.00"); !ok {
		t.Fatal("expected zero-padded s.g.00")
	}
	if _, ok := placementByLabel(plan, "s.g.11"); !ok {
		t.Fatal("expected zero-padded s.g.11")
	}
}

func TestCompilePrefabMemberLabels(t *testing.T) {
	s := &Spec{Schema: Schema, SceneID: "s",
		Prefabs: map[string]Prefab{"torch": {Members: []PrefabMember{
			{Role: "base", Kind: "static_mesh"},
			{Role: "flame", Kind: "static_mesh", Location: &[3]float64{0, 0, 100}},
		}}},
		Groups: []Group{{Name: "torches", Use: "torch",
			Layout: Layout{Type: "line", Count: 2, Start: [3]float64{0, 0, 0}, End: [3]float64{100, 0, 0}}}},
	}
	plan, diags := Compile(s)
	if hasSeverity(diags, "error") {
		t.Fatalf("unexpected errors: %+v", diags)
	}
	for _, lbl := range []string{"s.torches.0.base", "s.torches.0.flame", "s.torches.1.base", "s.torches.1.flame"} {
		if _, ok := placementByLabel(plan, lbl); !ok {
			t.Fatalf("missing prefab member label %q", lbl)
		}
	}
}

func TestCompileSunPitchActorError(t *testing.T) {
	s := &Spec{Schema: Schema, SceneID: "s", Actors: []Actor{
		{Label: "sun", Kind: "directional_light", RotationPyr: &[3]float64{10, 0, 0}},
	}}
	_, diags := Compile(s)
	if !findDiag(diags, "error", "s.sun") {
		t.Fatalf("expected sun-pitch error on actor; got %+v", diags)
	}
}

func TestCompileUnknownPrefabError(t *testing.T) {
	s := &Spec{Schema: Schema, SceneID: "s", Groups: []Group{{
		Name: "g", Use: "missing", Layout: Layout{Type: "grid", Count: 1},
	}}}
	_, diags := Compile(s)
	if !findDiag(diags, "error", "groups[0].use") {
		t.Fatalf("expected unknown-prefab error; got %+v", diags)
	}
}

func TestCompileMaterialFromProperties(t *testing.T) {
	s := &Spec{Schema: Schema, SceneID: "s", Actors: []Actor{
		{Label: "a", Kind: "static_mesh", Properties: map[string]any{"material": "/Game/M_Red"}},
	}}
	plan, _ := Compile(s)
	a, _ := placementByLabel(plan, "s.a")
	if a.Material != "/Game/M_Red" {
		t.Fatalf("material = %q", a.Material)
	}
}
