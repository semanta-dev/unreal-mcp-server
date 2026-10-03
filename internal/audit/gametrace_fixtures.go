package audit

import "math"

// Synthetic defect/fixed fixtures modelling the exact ground-truth defects named in
// AGENTIC_GAMEDEV_PLAN.md §1 and Phase 0. Each audit's falsifiable test asserts the
// audit FAILS the defect fixture and PASSES the fixed one — the plan's Phase-0 gate
// ("if any audit passes either current build, the audit is wrong"). These builders
// are pure (no disk), so they run in CI without the game projects present.

// MetronomeDecisions models PolyWorld's uniform-cadence loop (PolyDirector.cpp:46-49):
// one action ("lane") chosen on a fixed beat, no other action ever viable. Low
// cadence entropy, single dominant action — decision_audit must FAIL this.
func MetronomeDecisions(n int) []DecisionPoint {
	pts := make([]DecisionPoint, n)
	for i := range pts {
		pts[i] = DecisionPoint{
			T:         float64(i) * 5.0, // perfectly periodic
			State:     "steady",
			Available: []string{"lane"}, // only one viable action
			Chosen:    "lane",
		}
	}
	return pts
}

// LiveDecisions models a state-responsive loop: multiple viable actions and a
// non-periodic, state-driven chosen sequence — decision_audit must PASS this.
func LiveDecisions(n int) []DecisionPoint {
	acts := []string{"single_target", "aoe", "expand", "bank"}
	pts := make([]DecisionPoint, n)
	for i := range pts {
		// Irregular cadence + state-driven choice cycling across all four actions.
		pts[i] = DecisionPoint{
			T:         float64(i)*3.0 + float64(i%3),
			State:     []string{"swarm", "elite", "spike"}[i%3],
			Available: acts,
			Chosen:    acts[(i*3+i/2)%len(acts)],
		}
	}
	return pts
}

// DeadNoveltySession models PolyWorld's "nothing new after 90s": one element at t=0
// then a long dead stretch across a 600s session — novelty_audit must FAIL.
func DeadNoveltySession() SessionTrace {
	return SessionTrace{
		Duration: 600,
		Observed: []NewElement{{ID: "wave", Kind: "spawn", T: 0}},
		Schedule: []NewElement{{ID: "wave", Kind: "spawn", T: 0}},
	}
}

// PacedNoveltySession introduces a new element on a steady schedule with no dead
// stretch longer than the genre threshold — novelty_audit must PASS.
func PacedNoveltySession() SessionTrace {
	els := []NewElement{
		{ID: "wave", Kind: "spawn", T: 0},
		{ID: "elite", Kind: "enemy", T: 90},
		{ID: "modifier_fog", Kind: "modifier", T: 180},
		{ID: "boss", Kind: "enemy", T: 300},
		{ID: "setpiece_breach", Kind: "setpiece", T: 420},
		{ID: "mechanic_overcharge", Kind: "mechanic", T: 510},
	}
	return SessionTrace{Duration: 600, Observed: els, Schedule: els}
}

// SilentDebugLineFire models Aesir's turret (Turret.cpp:173): a fire event with NO
// visual/audio/camera response — feel_audit and audio_audit must FAIL.
func SilentDebugLineFire() []Event {
	evs := make([]Event, 5)
	for i := range evs {
		evs[i] = Event{T: float64(i), Kind: EventFire, Instigator: "Turret"} // no response Ts
	}
	return evs
}

// JuicyFire models a rebuilt turret: each fire has a flash + sound + shake within
// 120ms and stays under the over-juice FX cap — feel_audit must PASS.
func JuicyFire() []Event {
	evs := make([]Event, 5)
	for i := range evs {
		t := float64(i)
		evs[i] = Event{
			T: t, Kind: EventFire, Instigator: "Turret",
			VisualT: t + 0.010, AudioT: t + 0.008, CameraT: t + 0.015, FXCount: 2,
		}
	}
	return evs
}

// wrapDeg normalizes an angle to [-180,180) as the engine reports facing yaw.
func wrapDeg(a float64) float64 {
	a = math.Mod(a, 360)
	if a >= 180 {
		a -= 360
	}
	if a < -180 {
		a += 360
	}
	return a
}

// FootSlideMotion models EnemyCharacter.cpp:188/47 — a fixed-rate clip under variable
// ground speed (root travels far while the clip's stride stays fixed) plus the raw
// RotationRate=540°/s snap-rotation. Yaw is REALISTIC: normalized to [-180,180) and
// sampled at 60fps so the per-second rate is a true 540°/s (9°/frame) — well above a
// smooth-turn cap. in_motion_audit must FAIL (on both the slide and snap legs).
func FootSlideMotion(n int) []MotionSample {
	ms := make([]MotionSample, n)
	yaw := 0.0
	for i := range ms {
		yaw += 540.0 / 60.0 // 540°/s at 60fps
		ms[i] = MotionSample{T: float64(i) / 60.0, RootTravel: 12.0, AnimStride: 3.0, Yaw: wrapDeg(yaw)}
	}
	return ms
}

// BlendedMotion models a ULocomotion-blended pawn: root travel matches anim stride
// (no slide) and yaw turns GRADUALLY at ~120°/s, normalized to [-180,180) so it
// wraps naturally through the ±180 boundary (exercising shortest-arc handling — a
// wrap must NOT read as a snap). in_motion_audit must PASS.
func BlendedMotion(n int) []MotionSample {
	ms := make([]MotionSample, n)
	yaw := 150.0 // start near +180 so the sequence crosses the wrap boundary
	for i := range ms {
		yaw += 120.0 / 60.0 // 120°/s at 60fps — smooth
		ms[i] = MotionSample{T: float64(i) / 60.0, RootTravel: 5.0, AnimStride: 5.0, Yaw: wrapDeg(yaw)}
	}
	return ms
}

// BootToEmptyConfig models PolyWorld's RC7 defect (DefaultEngine.ini:4): the packaged
// build boots to a stock engine map, not the game — render_health must FAIL.
func BootToEmptyConfig() EngineConfig {
	return EngineConfig{
		GameDefaultMap: "/Engine/Maps/Templates/OpenWorld",
		GameMaps:       []string{"/Game/Maps/Play"},
	}
}

// BootToGameConfig boots into a real game map — render_health must PASS (on the
// config leg).
func BootToGameConfig() EngineConfig {
	return EngineConfig{
		GameDefaultMap: "/Game/Maps/Play",
		GameMaps:       []string{"/Game/Maps/Play"},
	}
}

// DefaultMovementEnvelope is a reasonable genre preset for verb_response tests: a
// verb that must respond within a few frames and produce a non-trivial delta.
func DefaultMovementEnvelope() VerbEnvelope {
	return VerbEnvelope{Name: "ground_movement", MinRiseFrames: 1, MaxRiseFrames: 12, MinDeltaMag: 1.0}
}

// TeleportSnapBurst models a degenerate verb: full state delta on frame 1 with no
// rise and no settle (instant teleport-snap) — verb_response must FAIL.
func TeleportSnapBurst() []BurstSample {
	b := make([]BurstSample, 20)
	for i := range b {
		pos := 100.0 // jumps to max immediately, never eases
		if i == 0 {
			pos = 0
		}
		b[i] = BurstSample{T: float64(i) / 60.0, Input: 1, Pos: [3]float64{pos, 0, 0}, Vel: [3]float64{0, 0, 0}}
	}
	return b
}

// HealthyMovementBurst models a well-tuned verb: a smooth accel curve rising over a
// few frames to a settled velocity — verb_response must PASS DefaultMovementEnvelope.
func HealthyMovementBurst() []BurstSample {
	b := make([]BurstSample, 20)
	pos := 0.0
	vel := 0.0
	for i := range b {
		vel += (60.0 - vel) * 0.25 // exponential approach to target speed
		pos += vel / 60.0
		b[i] = BurstSample{T: float64(i) / 60.0, Input: 1, Pos: [3]float64{pos, 0, 0}, Vel: [3]float64{vel, 0, 0}}
	}
	return b
}

// SilentTrack models a build that emits no audio (PolyWorld; Aesir's turret) — a
// flatline envelope — audio_audit must FAIL.
func SilentTrack(n int) []AudioSample {
	t := make([]AudioSample, n)
	for i := range t {
		t[i] = AudioSample{T: float64(i) / 60.0, RMS: 0, Peak: 0}
	}
	return t
}

// LoudTrack models a build whose events produce audio: a non-zero RMS envelope with
// peaks near the fixed event times {0,1,2,3,4}s — audio_audit must PASS.
func LoudTrack(n int) []AudioSample {
	t := make([]AudioSample, n)
	for i := range t {
		sec := float64(i) / 60.0
		rms := 0.05
		if int(sec*10)%10 == 0 { // a burst near each whole second
			rms = 0.4
		}
		t[i] = AudioSample{T: sec, RMS: rms, Peak: rms * 1.5}
	}
	return t
}

// CubeScene models Aesir/PolyWorld programmer art (Turret.cpp:23-53, PolyField.cpp:352):
// engine BasicShapes + BasicShapeMaterial + a DrawDebugLine actor — primitive_audit
// must FAIL.
func CubeScene() Scene {
	return Scene{Level: "/Game/Maps/L_Arena", Actors: []Actor{
		{Label: "Turret", Class: "ATurret", MeshPath: "/Engine/BasicShapes/Cylinder",
			MaterialPath: "/Engine/BasicShapes/BasicShapeMaterial", Hero: true, DebugDraw: true},
		{Label: "Lane", Class: "AStaticMeshActor", MeshPath: "/Engine/BasicShapes/Cube",
			MaterialPath: "/Engine/BasicShapes/BasicShapeMaterial", Hero: true},
	}}
}

// DressedScene models a rebuilt scene using real kit meshes/materials in hero slots —
// primitive_audit must PASS.
func DressedScene() Scene {
	return Scene{Level: "/Game/Maps/L_Arena", Actors: []Actor{
		{Label: "Turret", Class: "ATurret", MeshPath: "/Game/MC_Turrets/Meshes/SM_Turret_01",
			MaterialPath: "/Game/MC_Turrets/Materials/MI_Turret", Hero: true},
		{Label: "Barricade", Class: "AStaticMeshActor", MeshPath: "/Game/Kit/SM_Barricade",
			MaterialPath: "/Game/Kit/MI_Metal", Hero: true},
	}}
}
