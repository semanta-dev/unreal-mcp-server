package tools

import (
	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

type viewportSetIn struct {
	CameraLocation    []float64 `json:"camera_location,omitempty" jsonschema:"[x,y,z] to move the editor viewport camera to"`
	CameraRotationPyr []float64 `json:"camera_rotation_pyr,omitempty" jsonschema:"[pitch,yaw,roll]"`
	PilotActor        string    `json:"pilot_actor,omitempty" jsonschema:"actor label to pilot the viewport camera with"`
	Eject             bool      `json:"eject,omitempty" jsonschema:"stop piloting"`
	GameView          *bool     `json:"game_view,omitempty" jsonschema:"toggle Game View (hides editor-only actors); like pressing G"`
	Console           []string  `json:"console,omitempty" jsonschema:"console commands to run against the viewport (e.g. 'r.ScreenPercentage 100')"`
}

type focusActorsIn struct {
	Targets       []string `json:"targets,omitempty" jsonschema:"actor labels to frame; empty = the current editor selection"`
	Pitch         float64  `json:"pitch,omitempty" jsonschema:"camera pitch in degrees (negative looks down); default -30"`
	DistanceScale float64  `json:"distance_scale,omitempty" jsonschema:"multiplier on the framing distance; default 2.0"`
}

type selectActorsIn struct {
	Labels []string `json:"labels,omitempty" jsonschema:"actor labels to select"`
	Mode   string   `json:"mode,omitempty" jsonschema:"replace|add|remove|none; default replace"`
	Frame  bool     `json:"frame,omitempty" jsonschema:"frame the selection in the viewport after selecting"`
}

// registerViewportTools adds tighter editor-integration tools: viewport camera
// control, selection, focus/framing, and a rich editor_state superset of
// editor_status (the frozen editor_status is left byte-compatible).
func registerViewportTools(s *registrar, b *bridge.Bridge) {
	add(s, "viewport_set",
		"Control the editor viewport: move the camera to a pose, pilot/eject an actor, toggle game view, or run viewport console commands. Returns the resulting camera pose.",
		structHandler[viewportSetIn](b, "viewport_set", func(in viewportSetIn) map[string]any {
			m := map[string]any{}
			cam := map[string]any{}
			if len(in.CameraLocation) > 0 {
				cam["location"] = in.CameraLocation
			}
			if len(in.CameraRotationPyr) > 0 {
				cam["rotation_pyr"] = in.CameraRotationPyr
			}
			if len(cam) > 0 {
				m["camera"] = cam
			}
			if in.PilotActor != "" {
				m["pilot_actor"] = in.PilotActor
			}
			if in.Eject {
				m["eject"] = true
			}
			if in.GameView != nil {
				m["game_view"] = *in.GameView
			}
			if len(in.Console) > 0 {
				m["console"] = in.Console
			}
			return m
		}))

	add(s, "viewport_get",
		"Get the editor viewport camera pose (and game-view state where available). Feeds camera framing for capture.",
		structHandler[noArgs](b, "viewport_get", func(noArgs) map[string]any { return map[string]any{} }))

	add(s, "focus_actors",
		"Frame actors (or the current selection) in the editor viewport by aiming the camera at their combined bounds.",
		structHandler[focusActorsIn](b, "focus_actors", func(in focusActorsIn) map[string]any {
			m := map[string]any{}
			if len(in.Targets) > 0 {
				m["targets"] = in.Targets
			} else {
				m["targets"] = "selection"
			}
			if in.Pitch != 0 {
				m["pitch"] = in.Pitch
			}
			if in.DistanceScale != 0 {
				m["distance_scale"] = in.DistanceScale
			}
			return m
		}))

	add(s, "select_actors",
		"Set the editor selection by actor labels (mode replace|add|remove|none), optionally framing the result.",
		structHandler[selectActorsIn](b, "select_actors", func(in selectActorsIn) map[string]any {
			m := map[string]any{"labels": in.Labels}
			if in.Mode != "" {
				m["mode"] = in.Mode
			}
			if in.Frame {
				m["frame"] = true
			}
			return m
		}))

	add(s, "get_selection",
		"Get the currently selected editor actors (labels + classes).",
		structHandler[noArgs](b, "get_selection", func(noArgs) map[string]any { return map[string]any{} }))

}
