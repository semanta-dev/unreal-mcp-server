package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/design"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

type primitiveAuditIn struct {
	Scene audit.Scene `json:"scene"`
}

type decisionAuditIn struct {
	Points []audit.DecisionPoint `json:"points"`
}

type noveltyAuditIn struct {
	Trace          audit.SessionTrace `json:"trace"`
	MaxDeadStretch float64                `json:"max_dead_stretch,omitempty"`
}

type feelAuditIn struct {
	Events        []audit.Event `json:"events"`
	WithinMs      float64           `json:"within_ms,omitempty"`
	MaxFXPerEvent int               `json:"max_fx_per_event,omitempty"`
}

type verbResponseIn struct {
	Burst    []audit.BurstSample `json:"burst"`
	Envelope audit.VerbEnvelope  `json:"envelope"`
}

type inMotionAuditIn struct {
	Samples []audit.MotionSample `json:"samples"`
}

type renderHealthIn struct {
	Config       audit.EngineConfig     `json:"config"`
	Timeline     []audit.TimelineSample `json:"timeline,omitempty"`
	TimelinePath string                     `json:"timeline_path,omitempty"`
}

type audioAuditIn struct {
	Track  []audit.AudioSample `json:"track"`
	Events []audit.Event       `json:"events"`
}

type assetUtilizationIn struct {
	Inventory audit.PackInventory `json:"inventory"`
}

type luminanceReportIn struct {
	FramePaths []string `json:"frame_paths"`
}

type luminanceReportOut struct {
	Aggregate visual.LumReport   `json:"aggregate"`
	PerFrame  []visual.LumReport `json:"per_frame"`
	Error     string            `json:"error,omitempty"`
}

type styleCohesionIn struct {
	FramePaths []string `json:"frame_paths"`
}

type styleCohesionOut struct {
	Aggregate visual.StyleReport   `json:"aggregate"`
	PerFrame  []visual.StyleReport `json:"per_frame"`
	Error     string                 `json:"error,omitempty"`
}

type balanceSweepIn struct {
	Scaffold design.Scaffold `json:"scaffold"`
	Grid     design.Grid     `json:"grid,omitempty"`
}

type balanceSweepOut struct {
	Sweep balanceSweepReportOut `json:"sweep"`
	Gate  design.GateReport    `json:"gate"`
}

type balanceSweepReportOut struct {
	DominantPolicy      bool                      `json:"dominant_policy"`
	DegenerateOptimum   bool                      `json:"degenerate_optimum"`
	AxisLiveness        float64                   `json:"axis_liveness"`
	FencedCorners       []string                  `json:"fenced_corners"`
	WinRateBySpike      map[string]float64        `json:"win_rate_by_spike"`
	BestPolicyByVariant map[string]design.Policy `json:"best_policy_by_variant"`
}

type designExploreIn struct {
	Seed        design.Genotype   `json:"seed"`
	Evaluations int                `json:"evaluations,omitempty"`
	SearchSeed  int64              `json:"search_seed,omitempty"`
	TopK        int                `json:"top_k,omitempty"`
	Donors      []design.Scaffold `json:"donors,omitempty"`
}

type designExploreOut struct {
	Filled int                `json:"filled"`
	Elites []design.Genotype `json:"elites"`
}

func registerDesignTools(s *mcp.Server) {
	add(s, "primitive_audit", "Run the primitive art Layer-A deterministic audit (readonly) against a captured scene.", primitiveAudit)
	add(s, "decision_audit", "Run the decision cadence Layer-A deterministic audit (readonly) against observed decision points.", decisionAudit)
	add(s, "novelty_audit", "Run the novelty cadence Layer-A deterministic audit (readonly) against a session trace.", noveltyAudit)
	add(s, "feel_audit", "Run the feedback coverage Layer-A deterministic audit (readonly) against gameplay events.", feelAudit)
	add(s, "verb_response", "Run the verb response Layer-A deterministic audit (readonly) against an input burst and envelope.", verbResponse)
	add(s, "in_motion_audit", "Run the locomotion Layer-A deterministic audit (readonly) against motion samples.", inMotionAudit)
	add(s, "render_health", "Run the render health Layer-A deterministic audit (readonly) against packaged config and render timeline.", renderHealth)
	add(s, "audio_audit", "Run the audio Layer-A deterministic audit (readonly) against an RMS track and gameplay events.", audioAudit)
	add(s, "asset_utilization", "Report diagnostic asset-pack utilization (readonly) for a referenced inventory.", assetUtilization)
	add(s, "luminance_report", "Run the luminance Layer-A deterministic audit (readonly) against captured frame paths.", luminanceReport)
	add(s, "style_cohesion", "Run the style cohesion Layer-A deterministic audit (readonly) against captured frame paths.", styleCohesion)
	add(s, "balance_sweep", "Run the balance sweep and gate keystone analysis (readonly) against a scaffold.", balanceSweep)
	add(s, "design_explore", "Run the design exploration keystone search (readonly) from a seed genotype.", designExplore)
}

func primitiveAudit(ctx context.Context, _ *mcp.CallToolRequest, in primitiveAuditIn) (*mcp.CallToolResult, audit.PrimitiveReport, error) {
	return nil, audit.Audit(in.Scene), nil
}

func decisionAudit(ctx context.Context, _ *mcp.CallToolRequest, in decisionAuditIn) (*mcp.CallToolResult, audit.DecisionReport, error) {
	return nil, audit.DecisionAudit(in.Points), nil
}

func noveltyAudit(ctx context.Context, _ *mcp.CallToolRequest, in noveltyAuditIn) (*mcp.CallToolResult, audit.NoveltyReport, error) {
	if in.MaxDeadStretch > 0 {
		return nil, audit.NoveltyAudit(in.Trace, in.MaxDeadStretch), nil
	}
	return nil, audit.NoveltyAuditDefault(in.Trace), nil
}

func feelAudit(ctx context.Context, _ *mcp.CallToolRequest, in feelAuditIn) (*mcp.CallToolResult, audit.FeelReport, error) {
	withinMs := in.WithinMs
	if withinMs == 0 {
		withinMs = 120
	}
	maxFXPerEvent := in.MaxFXPerEvent
	if maxFXPerEvent == 0 {
		maxFXPerEvent = 4
	}
	return nil, audit.FeelAudit(in.Events, withinMs, maxFXPerEvent), nil
}

func verbResponse(ctx context.Context, _ *mcp.CallToolRequest, in verbResponseIn) (*mcp.CallToolResult, audit.VerbReport, error) {
	return nil, audit.VerbResponse(in.Burst, in.Envelope), nil
}

func inMotionAudit(ctx context.Context, _ *mcp.CallToolRequest, in inMotionAuditIn) (*mcp.CallToolResult, audit.MotionReport, error) {
	return nil, audit.InMotionAuditDefault(in.Samples), nil
}

func renderHealth(ctx context.Context, _ *mcp.CallToolRequest, in renderHealthIn) (*mcp.CallToolResult, audit.RenderHealthReport, error) {
	timeline := in.Timeline
	if in.TimelinePath != "" {
		var err error
		timeline, err = audit.LoadTimeline(in.TimelinePath)
		if err != nil {
			return nil, audit.RenderHealthReport{}, err
		}
	}
	return nil, audit.RenderHealth(in.Config, timeline), nil
}

func audioAudit(ctx context.Context, _ *mcp.CallToolRequest, in audioAuditIn) (*mcp.CallToolResult, audit.AudioReport, error) {
	return nil, audit.AudioAuditDefault(in.Track, in.Events), nil
}

func assetUtilization(ctx context.Context, _ *mcp.CallToolRequest, in assetUtilizationIn) (*mcp.CallToolResult, audit.UtilizationReport, error) {
	return nil, audit.Utilization(in.Inventory), nil
}

func luminanceReport(ctx context.Context, _ *mcp.CallToolRequest, in luminanceReportIn) (*mcp.CallToolResult, luminanceReportOut, error) {
	perFrame, aggregate, err := visual.AnalyzeLuminanceFrames(framesFromPaths(in.FramePaths))
	out := luminanceReportOut{Aggregate: aggregate, PerFrame: perFrame}
	if err != nil {
		out.Error = err.Error()
	}
	return nil, out, nil
}

func styleCohesion(ctx context.Context, _ *mcp.CallToolRequest, in styleCohesionIn) (*mcp.CallToolResult, styleCohesionOut, error) {
	perFrame, aggregate, err := visual.AnalyzeStyleFrames(framesFromPaths(in.FramePaths))
	out := styleCohesionOut{Aggregate: aggregate, PerFrame: perFrame}
	if err != nil {
		out.Error = err.Error()
	}
	return nil, out, nil
}

func balanceSweep(ctx context.Context, _ *mcp.CallToolRequest, in balanceSweepIn) (*mcp.CallToolResult, balanceSweepOut, error) {
	grid := in.Grid
	if grid == (design.Grid{}) {
		grid = design.Grid{SSteps: 5, ESteps: 5, SplashSteps: 5}
	}
	sweep := design.Sweep(in.Scaffold, grid)
	return nil, balanceSweepOut{
		Sweep: balanceSweepReport(sweep),
		Gate:  design.Gate(in.Scaffold, grid),
	}, nil
}

func designExplore(ctx context.Context, _ *mcp.CallToolRequest, in designExploreIn) (*mcp.CallToolResult, designExploreOut, error) {
	evaluations := in.Evaluations
	if evaluations == 0 {
		evaluations = 500
	}
	topK := in.TopK
	if topK == 0 {
		topK = 8
	}
	result := design.Search(in.Seed, design.Config{
		Evaluations: evaluations,
		Seed:        in.SearchSeed,
		TopK:        topK,
		Donors:      in.Donors,
	})
	return nil, designExploreOut{Filled: result.Filled, Elites: result.Elites}, nil
}

func framesFromPaths(paths []string) []audit.Frame {
	frames := make([]audit.Frame, len(paths))
	for i, path := range paths {
		frames[i] = audit.Frame{Path: path}
	}
	return frames
}

func balanceSweepReport(report design.SweepReport) balanceSweepReportOut {
	return balanceSweepReportOut{
		DominantPolicy:      report.DominantPolicy,
		DegenerateOptimum:   report.DegenerateOptimum,
		AxisLiveness:        report.AxisLiveness,
		FencedCorners:       report.FencedCorners,
		WinRateBySpike:      boolKeyedFloatMap(report.WinRateBySpike),
		BestPolicyByVariant: report.BestPolicyByVariant,
	}
}

func boolKeyedFloatMap(in map[bool]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		if k {
			out["true"] = v
		} else {
			out["false"] = v
		}
	}
	return out
}
