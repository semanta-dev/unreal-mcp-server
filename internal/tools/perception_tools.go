package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

// assetThumbnailIn is the input for the asset_thumbnail perception tool.
type assetThumbnailIn struct {
	AssetPath string `json:"asset_path"`
	Size      int    `json:"size,omitempty"`
}

// audioCaptureStartIn / audioCaptureStopIn drive the §6.3 submix-tap (RC9).
type audioCaptureStartIn struct {
	Session string `json:"session,omitempty"`
	World   string `json:"world,omitempty"`
}

type audioCaptureStopIn struct {
	OutDir string `json:"out_dir,omitempty"`
	World  string `json:"world,omitempty"`
}

type playTestSoundIn struct {
	Sound  string  `json:"sound"`
	Volume float64 `json:"volume,omitempty"`
	World  string  `json:"world,omitempty"`
}

// registerPerceptionTools adds the AGENTIC_GAMEDEV_PLAN.md §3.1 perception primitive:
// asset_thumbnail renders a browser StaticMesh into a PNG from a canonical 3/4 angle
// and returns the hard facts (tri/vert count, material slots, LOD count, bounds). It
// fixes RC2 — the agent could not SEE a purchased asset before using it. Bridge op
// (runs in the editor), so it dispatches through the companion module.
func registerPerceptionTools(s *mcp.Server, b *bridge.Bridge) {
	add(s, "asset_thumbnail",
		"Render a Content Browser StaticMesh into a PNG thumbnail (canonical 3/4 angle) and return hard facts (tri/vert count, material slot names, LOD count, bounds). The keystone perception primitive so the agent can SEE an asset before placing it. Requires a live editor.",
		structHandler[assetThumbnailIn](b, "asset_thumbnail", func(in assetThumbnailIn) map[string]any {
			m := map[string]any{"asset_path": in.AssetPath}
			if in.Size > 0 {
				m["size"] = in.Size
			}
			return m
		}))

	// Audio submix tap (§6.3, RC9): start/stop an ISubmixBufferListener on the main
	// submix and reduce it to an RMS/peak envelope JSONL the audio_audit tool consumes.
	// Requires a live PIE session (audio only renders in play).
	add(s, "audio_capture_start",
		"Start tapping the game's main audio submix during PIE, recording an RMS/peak envelope. Requires a running play session. Pair with audio_capture_stop; the envelope feeds audio_audit to certify a build is not silent.",
		structHandler[audioCaptureStartIn](b, "audio_capture_start", func(in audioCaptureStartIn) map[string]any {
			m := map[string]any{}
			if in.Session != "" {
				m["session"] = in.Session
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
	add(s, "audio_capture_stop",
		"Stop the audio submix tap and write <session>_audio.jsonl ({t,rms,peak} per buffer) to out_dir. Returns {path,points,max_rms,duration}.",
		structHandler[audioCaptureStopIn](b, "audio_capture_stop", func(in audioCaptureStopIn) map[string]any {
			m := map[string]any{}
			if in.OutDir != "" {
				m["out_dir"] = in.OutDir
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
	add(s, "play_test_sound",
		"Play a sound cue into the running PIE world (a test signal for audio_capture / audio_audit validation).",
		structHandler[playTestSoundIn](b, "play_test_sound", func(in playTestSoundIn) map[string]any {
			m := map[string]any{"sound": in.Sound}
			if in.Volume > 0 {
				m["volume"] = in.Volume
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
}
