package tools

import ()

type pieInputIn struct {
	Key       string  `json:"key" jsonschema:"UE key name: W, A, S, D, SpaceBar, LeftMouseButton, Gamepad_FaceButton_Bottom, etc."`
	Action    string  `json:"action,omitempty" jsonschema:"tap (press+auto-release) | press | release | hold | release_all; default tap"`
	DurationS float64 `json:"duration_s,omitempty" jsonschema:"for action=hold: seconds to hold the key; default 1.0"`
}

// registerControlTools adds P7 input synthesis: drive the live game via injected
// keypresses so a playtest is ACTIVE (walk the pawn, fire, jump) rather than
// passive observation. Backed by the UnrealMCP C++ plugin's control subsystem.
func registerControlTools(s *registrar, d Deps) {
	add(s, "pie_input",
		"Synthesize input into the running game (PIE): tap/hold a key or button so an agent can actually PLAY — hold W to walk the pawn, tap SpaceBar to jump, click to fire. Legacy action+axis mappings fire as if a human pressed the key. Needs the UnrealMCP plugin compiled in.",
		structHandler[pieInputIn](d.Bridge, "pie_input", func(in pieInputIn) map[string]any {
			m := map[string]any{"key": in.Key}
			if in.Action != "" {
				m["action"] = in.Action
			}
			if in.DurationS > 0 {
				m["duration_s"] = in.DurationS
			}
			return m
		}))
}
