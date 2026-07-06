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

type pawnStateIn struct {
	Player int    `json:"player,omitempty"`
	World  string `json:"world,omitempty"`
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
	add(s, "pawn_state",
		"Read the player pawn's location, velocity, and speed in the running PIE world. Sample it across an injected-input window (pie_input) to observe the verb responding — the frame-level feel read behind verb_response.",
		structHandler[pawnStateIn](b, "pawn_state", func(in pawnStateIn) map[string]any {
			m := map[string]any{}
			if in.Player > 0 {
				m["player"] = in.Player
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
}

type companySelectIn struct {
	Building int  `json:"building,omitempty"`
	Supplier *int `json:"supplier,omitempty"`
	Market   *int `json:"market,omitempty"`
}

// registerCompanyTools exposes the Company-MVP slice tools (read status + make the
// Capitalism-2 selection: who you buy from / sell to).
func registerCompanyTools(s *mcp.Server, b *bridge.Bridge) {
	add(s, "company_status",
		"Read the PolyWorld Company-MVP economy from the live PIE: company Capital + each production building's supplier/market and last-cycle profit.",
		structHandler[struct {
			World string `json:"world,omitempty"`
		}](b, "company_status", func(in struct {
			World string `json:"world,omitempty"`
		}) map[string]any {
			m := map[string]any{}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
	add(s, "company_build",
		"Place a factory from the build catalog (option index) at a world [x,y,z] location — what a HUD build-palette click does; spends Capital. Returns {built,name,spent,capital}.",
		structHandler[struct {
			Option   int       `json:"option"`
			Location []float64 `json:"location,omitempty"`
			World    string    `json:"world,omitempty"`
		}](b, "company_build", func(in struct {
			Option   int       `json:"option"`
			Location []float64 `json:"location,omitempty"`
			World    string    `json:"world,omitempty"`
		}) map[string]any {
			m := map[string]any{"option": in.Option}
			if len(in.Location) == 3 {
				m["location"] = in.Location
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
	add(s, "company_select",
		"Set a production building's SUPPLIER (who you buy inputs from) and/or MARKET (who you sell the product to) by catalog index — the core Capitalism-2 choice. Profit updates next cycle.",
		structHandler[companySelectIn](b, "company_select", func(in companySelectIn) map[string]any {
			m := map[string]any{"building": in.Building}
			if in.Supplier != nil {
				m["supplier"] = *in.Supplier
			}
			if in.Market != nil {
				m["market"] = *in.Market
			}
			return m
		}))
}

// registerWidgetRenderTool exposes widget_render (offscreen UMG capture via FWidgetRenderer).
func registerWidgetRenderTool(s *mcp.Server, b *bridge.Bridge) {
	add(s, "widget_render",
		"Render a UserWidget class (e.g. /Script/PolyWorld.CityBuildWidget) OFFSCREEN to a PNG at out_path (width x height) via FWidgetRenderer — no PIE. The UMG HUD visual-iteration loop.",
		structHandler[struct {
			WidgetClass string `json:"widget_class"`
			OutPath     string `json:"out_path"`
			Width       int    `json:"width,omitempty"`
			Height      int    `json:"height,omitempty"`
		}](b, "widget_render", func(in struct {
			WidgetClass string `json:"widget_class"`
			OutPath     string `json:"out_path"`
			Width       int    `json:"width,omitempty"`
			Height      int    `json:"height,omitempty"`
		}) map[string]any {
			m := map[string]any{"widget_class": in.WidgetClass, "out_path": in.OutPath}
			if in.Width > 0 {
				m["width"] = in.Width
			}
			if in.Height > 0 {
				m["height"] = in.Height
			}
			return m
		}))
}

// registerDemolishTool exposes company_demolish (bulldoze nearest building + refund).
func registerDemolishTool(s *mcp.Server, b *bridge.Bridge) {
	add(s, "company_demolish",
		"Bulldoze the building nearest a world [x,y,z] location — refunds half its cost and destroys it (frees its grid cells). Returns {demolished,name,refund,capital}.",
		structHandler[struct {
			Location []float64 `json:"location,omitempty"`
			World    string    `json:"world,omitempty"`
		}](b, "company_demolish", func(in struct {
			Location []float64 `json:"location,omitempty"`
			World    string    `json:"world,omitempty"`
		}) map[string]any {
			m := map[string]any{}
			if len(in.Location) == 3 {
				m["location"] = in.Location
			}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
}

// registerRoadTool exposes company_road (drag-build a clamped road line).
func registerRoadTool(s *mcp.Server, b *bridge.Bridge) {
	add(s, "company_road",
		"Drag-build a road line from grid cell start=[x,y] to end=[x,y] (X-first L), clamped to the affordable/unblocked prefix. Returns {placed,capital,road_cells}.",
		structHandler[struct {
			Start []int  `json:"start"`
			End   []int  `json:"end"`
			World string `json:"world,omitempty"`
		}](b, "company_road", func(in struct {
			Start []int  `json:"start"`
			End   []int  `json:"end"`
			World string `json:"world,omitempty"`
		}) map[string]any {
			m := map[string]any{"start": in.Start, "end": in.End}
			if in.World != "" {
				m["world"] = in.World
			}
			return m
		}))
}
