package scenespec

import "fmt"

// This file holds the five environment presets as DATA and folds user overrides
// into a small set of lighting placements. The invariant baked into every preset
// is that the sun's pitch is negative (below the horizon line would render
// black; at/above 0 does). EnvPlacements re-checks the pitch after overrides.

// envPreset is the resolved lighting configuration for a named preset. A nil
// ExposureEV100 means auto-exposure (no manual post-process override).
type envPreset struct {
	SunRotationPyr    [3]float64
	SunIntensityLux   float64
	SkyLightIntensity float64
	FogDensity        float64
	ColorTemperatureK float64
	ExposureEV100     *float64
}

func fptr(f float64) *float64 { return &f }

// presets are the five documented lighting looks. Sun pitch is always negative.
var presets = map[string]envPreset{
	"daytime_clear": {
		SunRotationPyr: [3]float64{-45, -35, 0}, SunIntensityLux: 120000,
		SkyLightIntensity: 1.0, FogDensity: 0.02, ColorTemperatureK: 5500,
	},
	"overcast": {
		SunRotationPyr: [3]float64{-60, -20, 0}, SunIntensityLux: 20000,
		SkyLightIntensity: 1.75, FogDensity: 0.06, ColorTemperatureK: 7000,
	},
	"dusk": {
		SunRotationPyr: [3]float64{-6, 100, 0}, SunIntensityLux: 8000,
		SkyLightIntensity: 0.4, FogDensity: 0.10, ColorTemperatureK: 3800,
	},
	"night": {
		SunRotationPyr: [3]float64{-25, 150, 0}, SunIntensityLux: 80,
		SkyLightIntensity: 0.15, FogDensity: 0.04, ColorTemperatureK: 9000,
	},
	"studio": {
		SunRotationPyr: [3]float64{-50, 40, 0}, SunIntensityLux: 15000,
		SkyLightIntensity: 2.0, FogDensity: 0.0, ColorTemperatureK: 6500,
		ExposureEV100: fptr(1.0), // studio uses a fixed exposure by default
	},
}

// EnvPlacements resolves a preset and its overrides into unlabeled-by-scene
// lighting placements (labels like "env.sun" that Compile prefixes with the
// scene id). Numeric overrides replace preset values; sun_rotation_pyr replaces
// the sun rotator; exposure_ev100 non-null adds a manual-exposure post_process
// placement while null (or a preset with no manual value) leaves exposure auto.
// A non-negative resolved sun pitch yields an "error" Diagnostic.
func EnvPlacements(preset string, overrides map[string]any) ([]Placement, []Diagnostic) {
	var diags []Diagnostic
	p, ok := presets[preset]
	if !ok {
		return nil, []Diagnostic{{Severity: "error", Field: "environment.preset",
			Message: "unknown environment preset " + fmt.Sprintf("%q", preset)}}
	}

	// Apply overrides.
	if v, ok, bad := getVec3(overrides, "sun_rotation_pyr"); ok {
		p.SunRotationPyr = v
	} else if bad {
		diags = append(diags, Diagnostic{"warning", "environment.overrides.sun_rotation_pyr",
			"expected an array of 3 numbers; ignored"})
	}
	p.SunIntensityLux = getFloat(overrides, "sun_intensity_lux", p.SunIntensityLux)
	p.SkyLightIntensity = getFloat(overrides, "sky_light_intensity", p.SkyLightIntensity)
	p.FogDensity = getFloat(overrides, "fog_density", p.FogDensity)
	p.ColorTemperatureK = getFloat(overrides, "color_temperature_k", p.ColorTemperatureK)

	// Exposure: explicit key overrides the preset. null => auto (nil); a number
	// => manual. Absent key keeps the preset default.
	if raw, present := overrides["exposure_ev100"]; present {
		switch ev := raw.(type) {
		case nil:
			p.ExposureEV100 = nil
		case float64:
			p.ExposureEV100 = fptr(ev)
		default:
			diags = append(diags, Diagnostic{"warning", "environment.overrides.exposure_ev100",
				"expected a number or null; ignored"})
		}
	}

	diags = append(diags, sunPitchDiags("environment.sun_rotation_pyr", p.SunRotationPyr)...)

	unit := [3]float64{1, 1, 1}
	out := []Placement{
		{
			Label: "env.sun", Kind: "directional_light", Scale: unit,
			RotationPyr: p.SunRotationPyr,
			Properties: map[string]any{
				"intensity_lux":       p.SunIntensityLux,
				"color_temperature_k": p.ColorTemperatureK,
			},
		},
		{
			Label: "env.sky_light", Kind: "sky_light", Scale: unit,
			Properties: map[string]any{"intensity": p.SkyLightIntensity},
		},
		{
			Label: "env.sky_atmosphere", Kind: "sky_atmosphere", Scale: unit,
		},
		{
			Label: "env.height_fog", Kind: "height_fog", Scale: unit,
			Properties: map[string]any{"density": p.FogDensity},
		},
	}
	if p.ExposureEV100 != nil {
		out = append(out, Placement{
			Label: "env.post_process", Kind: "post_process", Scale: unit,
			Properties: map[string]any{
				"exposure_method": "manual",
				"exposure_ev100":  *p.ExposureEV100,
			},
		})
	}
	return out, diags
}

// getVec3 reads a 3-number array from a JSON-decoded map. ok is true on success;
// bad is true when the key is present but not a valid 3-number array.
func getVec3(m map[string]any, key string) (v [3]float64, ok, bad bool) {
	raw, present := m[key]
	if !present {
		return v, false, false
	}
	arr, isArr := raw.([]any)
	if !isArr || len(arr) != 3 {
		return v, false, true
	}
	for i, e := range arr {
		f, isNum := e.(float64)
		if !isNum {
			return v, false, true
		}
		v[i] = f
	}
	return v, true, false
}

// getFloat reads a numeric value, falling back to def when absent or non-numeric.
func getFloat(m map[string]any, key string, def float64) float64 {
	if raw, ok := m[key]; ok {
		if f, ok := raw.(float64); ok {
			return f
		}
	}
	return def
}
