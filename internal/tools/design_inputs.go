package tools

import (
	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/design"
)

type primitiveAuditIn struct {
	Scene audit.Scene `json:"scene"`
}

type decisionAuditIn struct {
	Points []audit.DecisionPoint `json:"points"`
}

type noveltyAuditIn struct {
	Trace          audit.SessionTrace `json:"trace"`
	MaxDeadStretch float64            `json:"max_dead_stretch,omitempty"`
}

type feelAuditIn struct {
	Events        []audit.Event `json:"events"`
	WithinMs      float64       `json:"within_ms,omitempty"`
	MaxFXPerEvent int           `json:"max_fx_per_event,omitempty"`
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
	TimelinePath string                 `json:"timeline_path,omitempty"`
}

type audioAuditIn struct {
	Track  []audit.AudioSample `json:"track"`
	Events []audit.Event       `json:"events"`
}

type assetUtilizationIn struct {
	Inventory audit.PackInventory `json:"inventory"`
}

type balanceSweepReportOut struct {
	DominantPolicy      bool                     `json:"dominant_policy"`
	DegenerateOptimum   bool                     `json:"degenerate_optimum"`
	AxisLiveness        float64                  `json:"axis_liveness"`
	FencedCorners       []string                 `json:"fenced_corners"`
	WinRateBySpike      map[string]float64       `json:"win_rate_by_spike"`
	BestPolicyByVariant map[string]design.Policy `json:"best_policy_by_variant"`
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
