package scenespec

import "testing"

func TestEvaluateChecksLit(t *testing.T) {
	lit := ProbeFacts{DirectionalLights: []LightFact{{Label: "Sun", Pitch: -45}}, SkyLights: []string{"Sky"}}
	if r := EvaluateChecks(Checks{RequireEnvironmentLit: true}, lit); !r.OK {
		t.Errorf("expected lit environment to pass, got %+v", r.Results)
	}
	// sun pointing up (pitch>=0) must fail — the documented black-level case.
	up := ProbeFacts{DirectionalLights: []LightFact{{Label: "Sun", Pitch: 20}}, SkyLights: []string{"Sky"}}
	if r := EvaluateChecks(Checks{RequireEnvironmentLit: true}, up); r.OK {
		t.Error("expected sun-up environment to fail require_environment_lit")
	}
	// no sky light -> fail
	noSky := ProbeFacts{DirectionalLights: []LightFact{{Label: "Sun", Pitch: -45}}}
	if r := EvaluateChecks(Checks{RequireEnvironmentLit: true}, noSky); r.OK {
		t.Error("expected missing sky light to fail")
	}
}

func TestEvaluateChecksMissingMeshes(t *testing.T) {
	facts := ProbeFacts{MissingMeshes: []string{"Wall_North"}}
	r := EvaluateChecks(Checks{RequireNoMissingMeshes: true}, facts)
	if r.OK {
		t.Error("expected a missing mesh to fail")
	}
	if len(r.Results) != 1 || r.Results[0].Invariant != "require_no_missing_meshes" {
		t.Fatalf("unexpected results: %+v", r.Results)
	}
}

func TestEvaluateChecksDisabledSkipped(t *testing.T) {
	// Only the enabled invariant is reported.
	r := EvaluateChecks(Checks{RequirePlayerStart: true}, ProbeFacts{PlayerStarts: []string{"PS_0"}})
	if len(r.Results) != 1 || r.Results[0].Invariant != "require_player_start" || !r.OK {
		t.Fatalf("expected only require_player_start passing, got %+v", r.Results)
	}
}

func TestEvaluateChecksAllPass(t *testing.T) {
	facts := ProbeFacts{
		DirectionalLights: []LightFact{{Label: "Sun", Pitch: -50}},
		SkyLights:         []string{"Sky"},
		PlayerStarts:      []string{"PS_0"},
		NavBounds:         []string{"Nav"},
	}
	checks := Checks{RequireEnvironmentLit: true, RequireNoMissingMeshes: true, RequirePlayerStart: true, RequireNavBounds: true, SpawnsWithinBounds: true}
	r := EvaluateChecks(checks, facts)
	if !r.OK || len(r.Results) != 5 {
		t.Fatalf("expected all 5 checks to pass, got ok=%v results=%+v", r.OK, r.Results)
	}
}
