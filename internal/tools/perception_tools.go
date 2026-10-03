package tools

import (
	"github.com/jdziat/unreal-mcp-server/internal/bridge"
)

// assetThumbnailIn is the input for the asset_thumbnail perception tool.
// audioCaptureStartIn / audioCaptureStopIn drive the §6.3 submix-tap (RC9).
type companySelectIn struct {
	Building int  `json:"building,omitempty"`
	Supplier *int `json:"supplier,omitempty"`
	Market   *int `json:"market,omitempty"`
}

// registerCompanyTools exposes the Company-MVP slice tools (read status + make the
// Capitalism-2 selection: who you buy from / sell to).
func registerCompanyTools(s *registrar, b *bridge.Bridge) {
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

// registerDemolishTool exposes company_demolish (bulldoze nearest building + refund).
func registerDemolishTool(s *registrar, b *bridge.Bridge) {
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
func registerRoadTool(s *registrar, b *bridge.Bridge) {
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
