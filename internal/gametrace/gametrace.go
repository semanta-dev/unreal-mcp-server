// Package gametrace defines the shared input contract for the Agentic Game-Dev
// Layer-A audits (AGENTIC_GAMEDEV_PLAN.md §7.1). Every deterministic audit
// (luminance, primitive, render-health, feel, verb-response, in-motion, audio,
// decision, novelty, style-cohesion, utilization) consumes one or more of these
// types, so they are declared once here rather than reinvented per package.
//
// The types model captured playtest artifacts already produced by the server:
// PNG frame paths, a per-frame timeline (JSONL), a structured gameplay event log
// (Tier-2) or a heuristic event list (Tier-1), an audio RMS/peak envelope, 60fps
// burst samples, a decision trace, and a session first-appearance trace. Field
// names use snake_case JSON tags to match the editor-side capture format.
//
// Nothing here depends on the Unreal editor: audits over these types run headless
// and are unit-testable against both the real PolySlice fixtures (see real.go) and
// synthetic defect/fixed builders (see fixtures.go).
package gametrace

// ---------------------------------------------------------------------------
// Visual — frames and per-frame timeline
// ---------------------------------------------------------------------------

// Frame is one captured still on disk plus its capture time. Path points at a PNG.
type Frame struct {
	Path    string  `json:"path"`
	Tag     string  `json:"tag,omitempty"`
	SimTime float64 `json:"sim_time"`
	Seq     int     `json:"seq"`
}

// TimelineSample is one row of the per-frame capture timeline (JSONL). It carries
// the generic render-health signal `Static` (count of static-geometry pixels/actors
// rendered; 0 == nothing drawn == a black screen) alongside free-form numeric
// fields captured per game. Extra holds game-specific keys (e.g. PolyWorld's
// population / min_supply_ratio / primary_lane / gauge) without a fixed schema.
type TimelineSample struct {
	Tag     string             `json:"tag"`
	Seq     int                `json:"seq"`
	SimTime float64            `json:"sim_time"`
	Static  int                `json:"static"`
	Extra   map[string]float64 `json:"-"`
}

// ---------------------------------------------------------------------------
// Scene — actors and materials (for primitive_audit)
// ---------------------------------------------------------------------------

// Actor is a placed actor with the asset paths a primitive audit scans. DebugDraw
// flags an actor whose only visual is a DrawDebugLine/Shape (Aesir's turret).
type Actor struct {
	Label        string `json:"label"`
	Class        string `json:"class"`
	MeshPath     string `json:"mesh_path,omitempty"`
	MaterialPath string `json:"material_path,omitempty"`
	Hero         bool   `json:"hero,omitempty"` // hero/structural slot (gates §7.5)
	DebugDraw    bool   `json:"debug_draw,omitempty"`
}

// Scene is the actor set of a captured level.
type Scene struct {
	Level  string  `json:"level"`
	Actors []Actor `json:"actors"`
}

// ---------------------------------------------------------------------------
// Config — packaged-build settings (for render_health, RC7)
// ---------------------------------------------------------------------------

// EngineConfig is the subset of DefaultEngine.ini render_health inspects.
type EngineConfig struct {
	GameDefaultMap   string `json:"game_default_map"`
	EditorStartupMap string `json:"editor_startup_map,omitempty"`
	// GameMaps is the set of maps that ARE the game (a GameDefaultMap outside this
	// set — e.g. /Engine/Maps/Templates/OpenWorld — is the RC7 boot-to-empty defect).
	GameMaps []string `json:"game_maps,omitempty"`
}

// ---------------------------------------------------------------------------
// Audio — envelope track (for audio_audit)
// ---------------------------------------------------------------------------

// AudioSample is one point of the master-submix RMS/peak envelope, timestamped to
// align with video frames (ISubmixBufferListener tap, §6.3).
type AudioSample struct {
	T    float64 `json:"t"`
	RMS  float64 `json:"rms"`
	Peak float64 `json:"peak"`
}

// ---------------------------------------------------------------------------
// Events — gameplay events (for feel_audit, audio_audit)
// ---------------------------------------------------------------------------

// EventKind enumerates the detectable gameplay events (Tier-1 heuristic or Tier-2
// instrumented). fire | hit | death plus UI/objective beats.
type EventKind string

const (
	EventFire      EventKind = "fire"
	EventHit       EventKind = "hit"
	EventDeath     EventKind = "death"
	EventObjective EventKind = "objective"
)

// Event is one detected gameplay event. Responses record which feedback channels
// fired and when, so feel_audit can assert a visual+audio+camera response within
// N ms of the event.
type Event struct {
	T          float64    `json:"t"`
	Kind       EventKind  `json:"kind"`
	Instigator string     `json:"instigator,omitempty"`
	Location   [3]float64 `json:"location,omitempty"`
	// Response channel timestamps (seconds, absolute). Zero == did not fire.
	VisualT float64 `json:"visual_t,omitempty"`
	AudioT  float64 `json:"audio_t,omitempty"`
	CameraT float64 `json:"camera_t,omitempty"`
	// FXCount is the number of distinct FX spawned for this event (over-juice cap).
	FXCount int `json:"fx_count,omitempty"`
}

// ---------------------------------------------------------------------------
// Verb response — 60fps burst (for verb_response)
// ---------------------------------------------------------------------------

// BurstSample is one 60fps sample of the pawn/weapon state during a verb-response
// window. Pos/Vel drive the movement fingerprint; Aim/Recoil/Damage the weapon one.
type BurstSample struct {
	T      float64    `json:"t"`
	Input  float64    `json:"input"` // input magnitude this frame [0,1]
	Pos    [3]float64 `json:"pos,omitempty"`
	Vel    [3]float64 `json:"vel,omitempty"`
	Aim    [2]float64 `json:"aim,omitempty"`
	Recoil float64    `json:"recoil,omitempty"`
}

// VerbEnvelope is the genre-preset expected response envelope verb_response compares
// against: acceptable rise-time and settle bounds for the input→state-delta curve.
type VerbEnvelope struct {
	Name          string  `json:"name"`
	MinRiseFrames int     `json:"min_rise_frames"` // 0 rise == teleport-snap (degenerate)
	MaxRiseFrames int     `json:"max_rise_frames"` // too slow == mushy
	MinDeltaMag   float64 `json:"min_delta_mag"`   // below == dead-zone (degenerate)
}

// ---------------------------------------------------------------------------
// In-motion — locomotion filmstrip (for in_motion_audit)
// ---------------------------------------------------------------------------

// MotionSample compares per-frame root translation against animation playback so a
// fixed-rate clip under variable ground speed reads as foot-sliding, and a large
// yaw delta per frame reads as snap-rotation (Aesir's EnemyCharacter defect).
type MotionSample struct {
	T          float64 `json:"t"`
	RootTravel float64 `json:"root_travel"` // world distance moved this frame
	AnimStride float64 `json:"anim_stride"` // distance the played clip implies
	Yaw        float64 `json:"yaw"`         // facing yaw (deg)
}

// ---------------------------------------------------------------------------
// Decisions — decision trace (for decision_audit)
// ---------------------------------------------------------------------------

// DecisionPoint is one (state, available_actions, chosen_action) observation. The
// audit scores density from AvailableActions, cadence entropy from the Chosen
// sequence, and action-frequency skew from Chosen — nothing more (§6.1b).
type DecisionPoint struct {
	T         float64  `json:"t"`
	State     string   `json:"state,omitempty"`
	Available []string `json:"available"`
	Chosen    string   `json:"chosen"`
}

// ---------------------------------------------------------------------------
// Novelty — session first-appearance trace (for novelty_audit)
// ---------------------------------------------------------------------------

// NewElement is one distinct session element whose FIRST appearance marks novelty:
// an enemy archetype, spawn config, modifier, objective type, setpiece beat, or
// mechanic toggle.
type NewElement struct {
	ID   string  `json:"id"`
	Kind string  `json:"kind"` // enemy|spawn|modifier|objective|setpiece|mechanic
	T    float64 `json:"t"`    // observed first-appearance time (seconds)
}

// SessionTrace is a full-session record: the observed first-appearances plus the
// AUTHORED schedule they are checked against, and the session length.
type SessionTrace struct {
	Duration float64      `json:"duration"`
	Observed []NewElement `json:"observed"`
	Schedule []NewElement `json:"schedule"` // the authored new-element schedule
}

// ---------------------------------------------------------------------------
// Utilization — pack reference (for asset_utilization, DIAGNOSTIC only)
// ---------------------------------------------------------------------------

// PackInventory is the set of a pack's assets and the subset the build references.
type PackInventory struct {
	Pack       string   `json:"pack"`
	AllAssets  []string `json:"all_assets"`
	Referenced []string `json:"referenced"`
}
