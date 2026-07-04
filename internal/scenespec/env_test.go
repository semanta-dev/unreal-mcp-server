package scenespec

import "testing"

func envByLabel(ps []Placement, label string) (Placement, bool) {
	for _, p := range ps {
		if p.Label == label {
			return p, true
		}
	}
	return Placement{}, false
}

func TestEnvEveryPresetSunPitchNegative(t *testing.T) {
	for name := range presets {
		ps, diags := EnvPlacements(name, nil)
		if hasSeverity(diags, "error") {
			t.Fatalf("preset %q had error diagnostics: %+v", name, diags)
		}
		sun, ok := envByLabel(ps, "env.sun")
		if !ok {
			t.Fatalf("preset %q missing env.sun", name)
		}
		if sun.RotationPyr[0] >= 0 {
			t.Fatalf("preset %q sun pitch = %v, must be < 0", name, sun.RotationPyr[0])
		}
		if sun.Kind != "directional_light" {
			t.Fatalf("preset %q sun kind = %q", name, sun.Kind)
		}
	}
}

func TestEnvBaselinePlacements(t *testing.T) {
	ps, _ := EnvPlacements("overcast", nil)
	for _, lbl := range []string{"env.sun", "env.sky_light", "env.sky_atmosphere", "env.height_fog"} {
		if _, ok := envByLabel(ps, lbl); !ok {
			t.Fatalf("missing %q", lbl)
		}
	}
}

func TestEnvUnknownPreset(t *testing.T) {
	ps, diags := EnvPlacements("space", nil)
	if ps != nil {
		t.Fatalf("expected no placements, got %v", ps)
	}
	if !findDiag(diags, "error", "environment.preset") {
		t.Fatalf("expected preset error, got %+v", diags)
	}
}

func TestEnvOverrideSunPitchError(t *testing.T) {
	// A positive pitch override must produce the black-render error.
	ov := map[string]any{"sun_rotation_pyr": []any{10.0, 0.0, 0.0}}
	ps, diags := EnvPlacements("daytime_clear", ov)
	if !findDiag(diags, "error", "environment.sun_rotation_pyr") {
		t.Fatalf("expected sun-pitch error, got %+v", diags)
	}
	sun, _ := envByLabel(ps, "env.sun")
	if sun.RotationPyr[0] != 10 {
		t.Fatalf("override not applied: %v", sun.RotationPyr)
	}
}

func TestEnvNumericOverridesApplied(t *testing.T) {
	ov := map[string]any{
		"sun_intensity_lux":   50000.0,
		"sky_light_intensity": 3.0,
		"fog_density":         0.25,
		"color_temperature_k": 4200.0,
	}
	ps, diags := EnvPlacements("daytime_clear", ov)
	if hasSeverity(diags, "error") {
		t.Fatalf("unexpected errors: %+v", diags)
	}
	sun, _ := envByLabel(ps, "env.sun")
	if sun.Properties["intensity_lux"].(float64) != 50000 {
		t.Fatalf("sun intensity = %v", sun.Properties["intensity_lux"])
	}
	if sun.Properties["color_temperature_k"].(float64) != 4200 {
		t.Fatalf("color temp = %v", sun.Properties["color_temperature_k"])
	}
	sky, _ := envByLabel(ps, "env.sky_light")
	if sky.Properties["intensity"].(float64) != 3.0 {
		t.Fatalf("sky intensity = %v", sky.Properties["intensity"])
	}
	fog, _ := envByLabel(ps, "env.height_fog")
	if fog.Properties["density"].(float64) != 0.25 {
		t.Fatalf("fog density = %v", fog.Properties["density"])
	}
}

func TestEnvExposureManual(t *testing.T) {
	// daytime_clear is auto by default; a numeric override => manual post_process.
	ps, _ := EnvPlacements("daytime_clear", map[string]any{"exposure_ev100": 2.5})
	pp, ok := envByLabel(ps, "env.post_process")
	if !ok {
		t.Fatal("expected env.post_process for numeric exposure_ev100")
	}
	if pp.Kind != "post_process" {
		t.Fatalf("kind = %q", pp.Kind)
	}
	if pp.Properties["exposure_method"] != "manual" {
		t.Fatalf("exposure_method = %v", pp.Properties["exposure_method"])
	}
	if pp.Properties["exposure_ev100"].(float64) != 2.5 {
		t.Fatalf("exposure_ev100 = %v", pp.Properties["exposure_ev100"])
	}
}

func TestEnvExposureAutoByDefault(t *testing.T) {
	// daytime_clear has no fixed exposure => no post_process placement.
	ps, _ := EnvPlacements("daytime_clear", nil)
	if _, ok := envByLabel(ps, "env.post_process"); ok {
		t.Fatal("did not expect a post_process placement under auto exposure")
	}
}

func TestEnvExposureNullOverridesPresetToAuto(t *testing.T) {
	// studio ships a fixed exposure; an explicit null override returns to auto.
	if _, ok := envByLabel(mustEnv(t, "studio", nil), "env.post_process"); !ok {
		t.Fatal("studio should default to manual exposure")
	}
	ps := mustEnv(t, "studio", map[string]any{"exposure_ev100": nil})
	if _, ok := envByLabel(ps, "env.post_process"); ok {
		t.Fatal("null exposure_ev100 should disable the manual post_process placement")
	}
}

func mustEnv(t *testing.T, preset string, ov map[string]any) []Placement {
	t.Helper()
	ps, diags := EnvPlacements(preset, ov)
	if hasSeverity(diags, "error") {
		t.Fatalf("unexpected errors for %q: %+v", preset, diags)
	}
	return ps
}
