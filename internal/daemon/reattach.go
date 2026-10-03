package daemon

import (
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"time"
)

// ReattachSummary reports what the boot barrier did.
type ReattachSummary struct {
	Records    int
	Killed     int
	KillFailed int
	Stale      int
}

// ReconcileAtStartup is the §6 boot barrier — call it BEFORE serving project_attach.
func (dm *Daemon) ReconcileAtStartup() ReattachSummary {
	return dm.reconcileRecords(
		dm.records.Read(),
		func() []lifecycle.TokenProc {
			return lifecycle.EnumerateTokenProcesses("UnrealEditor", supervisor.InstanceTokenFlag)
		},
		func(pid int) string { return lifecycle.ProcessToken(pid, supervisor.InstanceTokenFlag) },
		lifecycle.IsAlive, lifecycle.Kill,
	)
}

// reconcileRecords is the testable core. verify(pid) returns the pid's live
// -MCPInstanceToken ("" if absent) — used both as an enum-miss fallback and as the
// pre-kill positive re-verify.
func (dm *Daemon) reconcileRecords(recs []supervisor.ReattachRecord, enum func() []lifecycle.TokenProc, verify func(int) string, isAlive func(int) bool, kill func(int) error) ReattachSummary {
	myTokens := map[string]bool{}
	for _, r := range recs {
		if r.Token != "" {
			myTokens[r.Token] = true
		}
	}
	// Ground truth = process table ∪ records. A live editor is found by ENUMERATED
	// pid (immune to recycling + closes the Launch-window), matched to MY tokens.
	livePID := map[string]int{}
	for _, tp := range enum() {
		if myTokens[tp.Token] {
			livePID[tp.Token] = tp.PID
		}
	}
	// Enum-miss fallback: a record whose persisted pid STILL positively bears my token
	// (a transient PEB read miss during enumeration) is live too — don't drop it unkilled.
	for _, r := range recs {
		if _, seen := livePID[r.Token]; !seen && r.PID > 0 && verify(r.PID) == r.Token {
			livePID[r.Token] = r.PID
		}
	}

	var records []supervisor.Record
	var live []supervisor.LiveEditor
	for _, r := range recs {
		records = append(records, supervisor.Record{
			Token: r.Token, Project: r.Project, PID: r.PID,
			Identity: r.Identity, State: supervisor.Starting, // non-adoptable => Kill
		})
		if pid, ok := livePID[r.Token]; ok {
			live = append(live, supervisor.LiveEditor{Token: r.Token, PID: pid, Project: r.Project})
		}
	}

	sum := ReattachSummary{Records: len(recs)}
	for _, a := range supervisor.Reconcile(records, live) {
		switch a.Kind {
		case supervisor.Kill:
			if supervisor.KillConfirmed(a.PID, a.Token, verify, isAlive, kill) {
				sum.Killed++
				dm.records.Remove(a.Token)
			} else {
				sum.KillFailed++ // keep the record so the next boot/prune retries
			}
		case supervisor.RemoveStale:
			sum.Stale++
			dm.records.Remove(a.Token)
		}
	}
	if sum.Records > 0 {
		dm.logger.Info("reattach reconcile complete",
			"records", sum.Records, "killed_orphans", sum.Killed, "kill_failed", sum.KillFailed, "stale", sum.Stale)
	}
	return sum
}

// PruneDeadRecords bounds record accumulation during normal operation.
func (dm *Daemon) PruneDeadRecords() {
	dm.records.PruneDead(lifecycle.IsAlive, lifecycle.ProcessIdentity, 5*time.Minute, time.Now())
}
