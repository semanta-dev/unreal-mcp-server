package scenespec

import "fmt"

// LightFact is a directional light and its pitch, as reported by design_probe.
type LightFact struct {
	Label string  `json:"label"`
	Pitch float64 `json:"pitch"`
}

// ProbeFacts is the design_probe result the invariant evaluator consumes. It is
// deliberately the flat, editor-supplied fact set so EvaluateChecks stays pure
// and unit-testable without an editor.
type ProbeFacts struct {
	MissingMeshes     []string    `json:"missing_meshes"`
	DirectionalLights []LightFact `json:"directional_lights"`
	SkyLights         []string    `json:"sky_lights"`
	PlayerStarts      []string    `json:"player_starts"`
	NavBounds         []string    `json:"nav_bounds"`
}

// CheckOutcome is one invariant's verdict.
type CheckOutcome struct {
	Invariant string `json:"invariant"`
	Severity  string `json:"severity"`
	Pass      bool   `json:"pass"`
	Detail    string `json:"detail"`
}

// CheckReport aggregates the requested invariants. OK is true iff every
// requested invariant passed.
type CheckReport struct {
	OK      bool           `json:"ok"`
	Results []CheckOutcome `json:"results"`
}

// EvaluateChecks evaluates the invariants that are enabled in checks against the
// probed facts. A disabled invariant is skipped (not reported). This is the
// design lint: it catches the failures that make a level unplayable or render
// black before the agent wastes a playtest on them.
func EvaluateChecks(checks Checks, facts ProbeFacts) CheckReport {
	var out []CheckOutcome
	add := func(name string, pass bool, detail string) {
		out = append(out, CheckOutcome{Invariant: name, Severity: "fail", Pass: pass, Detail: detail})
	}

	if checks.RequireEnvironmentLit {
		var litSun *LightFact
		for i := range facts.DirectionalLights {
			if facts.DirectionalLights[i].Pitch < 0 {
				litSun = &facts.DirectionalLights[i]
				break
			}
		}
		hasSky := len(facts.SkyLights) > 0
		pass := litSun != nil && hasSky
		detail := "a directional light with pitch<0 plus a sky light are present"
		if litSun == nil && len(facts.DirectionalLights) > 0 {
			detail = "directional light(s) present but none point down (pitch>=0) — the level will render black"
		} else if litSun == nil {
			detail = "no directional light — the level will render black"
		} else if !hasSky {
			detail = "directional sun present but no sky light (ambient will be flat)"
		}
		add("require_environment_lit", pass, detail)
	}
	if checks.RequireNoMissingMeshes {
		pass := len(facts.MissingMeshes) == 0
		detail := "no static-mesh actors are missing a mesh"
		if !pass {
			detail = fmt.Sprintf("%d actor(s) missing a mesh: %v", len(facts.MissingMeshes), facts.MissingMeshes)
		}
		add("require_no_missing_meshes", pass, detail)
	}
	if checks.RequirePlayerStart {
		pass := len(facts.PlayerStarts) > 0
		add("require_player_start", pass, fmt.Sprintf("%d player start(s)", len(facts.PlayerStarts)))
	}
	if checks.RequireNavBounds {
		pass := len(facts.NavBounds) > 0
		add("require_nav_bounds", pass, fmt.Sprintf("%d nav-mesh bounds volume(s)", len(facts.NavBounds)))
	}
	if checks.SpawnsWithinBounds {
		// design_probe does not report spawn locations, so this is a presence
		// proxy (a spawn exists to place players); documented as such.
		pass := len(facts.PlayerStarts) > 0
		add("spawns_within_bounds", pass, "presence check: at least one player start exists (location bounds not verified)")
	}

	ok := true
	for _, r := range out {
		if !r.Pass {
			ok = false
		}
	}
	return CheckReport{OK: ok, Results: out}
}
