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

// Evidence (R0.8): every audit names what it scores and where that comes from, and
// refuses — PRECONDITION, details.reason insufficient_evidence — when the evidence is
// missing, instead of scoring an empty input as a clean pass.

// missingEvidence is implemented by audit inputs; it lists the absent evidence fields.
type missingEvidence interface{ missing() []string }

func (in primitiveAuditIn) missing() []string {
	if len(in.Scene.Actors) == 0 {
		return []string{"scene.actors"}
	}
	return nil
}

func (in decisionAuditIn) missing() []string {
	if len(in.Points) == 0 {
		return []string{"points"}
	}
	return nil
}

func (in noveltyAuditIn) missing() []string {
	if in.Trace.Duration <= 0 {
		return []string{"trace.duration"}
	}
	return nil
}

func (in feelAuditIn) missing() []string {
	if len(in.Events) == 0 {
		return []string{"events"}
	}
	return nil
}

func (in verbResponseIn) missing() []string {
	if len(in.Burst) < 2 {
		return []string{"burst (≥ 2 samples)"}
	}
	return nil
}

func (in inMotionAuditIn) missing() []string {
	if len(in.Samples) == 0 {
		return []string{"samples"}
	}
	return nil
}

func (in renderHealthIn) missing() []string {
	if in.Config.GameDefaultMap == "" {
		return []string{"config.game_default_map"}
	}
	return nil
}

func (in audioAuditIn) missing() []string {
	var m []string
	if len(in.Track) == 0 {
		m = append(m, "track")
	}
	if len(in.Events) == 0 {
		m = append(m, "events")
	}
	return m
}

func (in assetUtilizationIn) missing() []string {
	if len(in.Inventory.AllAssets) == 0 {
		return []string{"inventory.all_assets"}
	}
	return nil
}
