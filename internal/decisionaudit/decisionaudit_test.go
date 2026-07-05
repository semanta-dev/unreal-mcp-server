package decisionaudit

import (
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/gametrace"
)

func TestDecisionAuditMetronomeFails(t *testing.T) {
	report := DecisionAudit(gametrace.MetronomeDecisions(30))
	if report.Pass {
		t.Fatalf("DecisionAudit(MetronomeDecisions) Pass=true, want false: %+v", report)
	}
	if report.CadenceEntropy >= minCadenceEntropy {
		t.Fatalf("metronome CadenceEntropy=%v, want below %v", report.CadenceEntropy, minCadenceEntropy)
	}
}

func TestDecisionAuditLivePasses(t *testing.T) {
	report := DecisionAudit(gametrace.LiveDecisions(30))
	if !report.Pass {
		t.Fatalf("DecisionAudit(LiveDecisions) Pass=false, want true: %+v", report)
	}
}

func TestNoveltyAuditDefaultDeadSessionFails(t *testing.T) {
	report := NoveltyAuditDefault(gametrace.DeadNoveltySession())
	if report.Pass {
		t.Fatalf("NoveltyAuditDefault(DeadNoveltySession) Pass=true, want false: %+v", report)
	}
	if report.LongestDeadStretch <= defaultMaxDeadStretch {
		t.Fatalf("dead session LongestDeadStretch=%v, want > %v", report.LongestDeadStretch, defaultMaxDeadStretch)
	}
}

func TestNoveltyAuditDefaultPacedSessionPasses(t *testing.T) {
	report := NoveltyAuditDefault(gametrace.PacedNoveltySession())
	if !report.Pass {
		t.Fatalf("NoveltyAuditDefault(PacedNoveltySession) Pass=false, want true: %+v", report)
	}
	if report.ScheduledCount != report.DeliveredCount || report.ScheduledCount == 0 {
		t.Fatalf("paced session should deliver all scheduled elements: %+v", report)
	}
}

// A build with NO dead stretch but that dropped promised reveals must still FAIL on
// schedule adherence (§6.1b: cadence vs the AUTHORED schedule).
func TestNoveltyAuditScheduleShortfallFails(t *testing.T) {
	tr := gametrace.SessionTrace{
		Duration: 200,
		// Observed elements are well-paced (gaps 0,100,80,20 — no dead stretch)...
		Observed: []gametrace.NewElement{
			{ID: "a", Kind: "enemy", T: 0},
			{ID: "x", Kind: "spawn", T: 100},
			{ID: "y", Kind: "modifier", T: 180},
		},
		// ...but the authored schedule promised b and c, which never shipped.
		Schedule: []gametrace.NewElement{
			{ID: "a", Kind: "enemy", T: 0},
			{ID: "b", Kind: "enemy", T: 60},
			{ID: "c", Kind: "setpiece", T: 120},
		},
	}
	report := NoveltyAuditDefault(tr)
	if report.Pass {
		t.Fatalf("schedule shortfall passed; want fail: %+v", report)
	}
	if report.ScheduledCount != 3 || report.DeliveredCount != 1 {
		t.Fatalf("adherence = %d/%d, want 1/3: %+v", report.DeliveredCount, report.ScheduledCount, report)
	}
}
