package daemon

import (
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"log/slog"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
)

func newReattachDaemon(dir string) *Daemon {
	return &Daemon{
		records: supervisor.NewRecordStore(dir, slog.New(slog.DiscardHandler)),
		isAlive: func(int) bool { return false },
		logger:  slog.New(slog.DiscardHandler),
	}
}

func TestRecordStoreRoundTripAndAtomicUpgrade(t *testing.T) {
	rs := supervisor.NewRecordStore(t.TempDir(), slog.New(slog.DiscardHandler))
	rs.Write(supervisor.ReattachRecord{Token: "t1", Project: "/A"})                            // pre-Launch intent
	rs.Write(supervisor.ReattachRecord{Token: "t1", Project: "/A", PID: 100, Identity: "id1"}) // upgrade
	recs := rs.Read()
	if len(recs) != 1 || recs[0].PID != 100 {
		t.Fatalf("upgrade should overwrite in place, got %+v", recs)
	}
	rs.Remove("t1")
	if len(rs.Read()) != 0 {
		t.Fatal("remove should drop the record")
	}
}

func TestReconcileKillsOnlyEnumeratedOrphans(t *testing.T) {
	dir := t.TempDir()
	dm := newReattachDaemon(dir)
	// A steady-state editor (record has pid) still running.
	dm.records.Write(supervisor.ReattachRecord{Token: "alive", Project: "/A", PID: 100, Identity: "idA"})
	// A Launch-window editor: intent only (pid=0 in the record), but enumeration finds it.
	dm.records.Write(supervisor.ReattachRecord{Token: "window", Project: "/B"})
	// A record whose editor is gone (enumeration doesn't return it).
	dm.records.Write(supervisor.ReattachRecord{Token: "gone", Project: "/C", PID: 200, Identity: "idC"})

	// Enumeration ground truth: MY tokens "alive" (pid 100) and "window" (pid 300 —
	// the pid the record never captured). "gone" is absent.
	enum := func() []lifecycle.TokenProc {
		return []lifecycle.TokenProc{
			{PID: 100, Token: "alive"},
			{PID: 300, Token: "window"},
			{PID: 999, Token: "someone-elses-daemon"}, // foreign token -> ignored
		}
	}
	verify := func(pid int) string {
		switch pid {
		case 100:
			return "alive"
		case 300:
			return "window"
		}
		return ""
	}
	isAlive := func(pid int) bool { return false } // after kill, confirmed dead
	var killed []int
	kill := func(pid int) error { killed = append(killed, pid); return nil }

	sum := dm.reconcileRecords(dm.records.Read(), enum, verify, isAlive, kill)

	// Both daemon-owned live editors are killed — incl. the Launch-window one found
	// ONLY via enumeration (pid 300, which the record didn't have).
	if len(killed) != 2 {
		t.Fatalf("want 2 kills (alive + window), got %v", killed)
	}
	gotKilled := map[int]bool{killed[0]: true, killed[1]: true}
	if !gotKilled[100] || !gotKilled[300] {
		t.Fatalf("expected pids 100 and 300 killed, got %v", killed)
	}
	if sum.Killed != 2 || sum.Stale != 1 {
		t.Fatalf("want 2 killed + 1 stale, got %+v", sum)
	}
	if len(dm.records.Read()) != 0 {
		t.Fatalf("all records should be cleaned after reconcile, %d remain", len(dm.records.Read()))
	}
}

func TestReconcileKeepsRecordOnKillFailure(t *testing.T) {
	dir := t.TempDir()
	dm := newReattachDaemon(dir)
	dm.records.Write(supervisor.ReattachRecord{Token: "stubborn", Project: "/A", PID: 100, Identity: "idA"})
	enum := func() []lifecycle.TokenProc { return []lifecycle.TokenProc{{PID: 100, Token: "stubborn"}} }
	verify := func(pid int) string {
		if pid == 100 {
			return "stubborn"
		}
		return ""
	}
	isAlive := func(pid int) bool { return true } // editor refuses to die
	kill := func(pid int) error { return nil }

	sum := dm.reconcileRecords(dm.records.Read(), enum, verify, isAlive, kill)
	if sum.KillFailed != 1 || sum.Killed != 0 {
		t.Fatalf("an unconfirmed kill must count as KillFailed, got %+v", sum)
	}
	if len(dm.records.Read()) != 1 {
		t.Fatal("a record whose editor would not die must be KEPT for a later retry")
	}
}

func TestReconcileEnumMissFallbackKills(t *testing.T) {
	dir := t.TempDir()
	dm := newReattachDaemon(dir)
	dm.records.Write(supervisor.ReattachRecord{Token: "missed", Project: "/A", PID: 100, Identity: "idA"})
	enum := func() []lifecycle.TokenProc { return nil } // enumeration transiently returns nothing
	verify := func(pid int) string {                    // but the persisted pid still positively bears my token
		if pid == 100 {
			return "missed"
		}
		return ""
	}
	var killed []int
	kill := func(pid int) error { killed = append(killed, pid); return nil }
	sum := dm.reconcileRecords(dm.records.Read(), enum, verify, func(int) bool { return false }, kill)
	if len(killed) != 1 || killed[0] != 100 || sum.Killed != 1 {
		t.Fatalf("an enum-miss but token-verified live editor must still be killed, got %v %+v", killed, sum)
	}
}

func TestReconcileRecycledPidNotKilled(t *testing.T) {
	dir := t.TempDir()
	dm := newReattachDaemon(dir)
	dm.records.Write(supervisor.ReattachRecord{Token: "mine", Project: "/A", PID: 100, Identity: "idA"})
	enum := func() []lifecycle.TokenProc { return []lifecycle.TokenProc{{PID: 100, Token: "mine"}} }
	// Between enum and kill, pid 100 exited and recycled to a token-less process.
	verify := func(pid int) string { return "" }
	var killed []int
	kill := func(pid int) error { killed = append(killed, pid); return nil }
	sum := dm.reconcileRecords(dm.records.Read(), enum, verify, func(int) bool { return false }, kill)
	if len(killed) != 0 {
		t.Fatalf("a pid recycled to a token-less process must NOT be killed, got %v", killed)
	}
	if sum.KillFailed != 1 {
		t.Fatalf("the un-verified kill should count as KillFailed (record kept), got %+v", sum)
	}
}

func TestPruneDeadRecords(t *testing.T) {
	rs := supervisor.NewRecordStore(t.TempDir(), slog.New(slog.DiscardHandler))
	rs.Write(supervisor.ReattachRecord{Token: "live", Project: "/A", PID: 100, Identity: "idA"})
	rs.Write(supervisor.ReattachRecord{Token: "dead", Project: "/B", PID: 200, Identity: "idB"})
	rs.Write(supervisor.ReattachRecord{Token: "fresh-intent", Project: "/C"}) // pid=0, just written
	isAlive := func(pid int) bool { return pid == 100 }
	identity := func(pid int) string {
		if pid == 100 {
			return "idA"
		}
		return ""
	}
	// staleAfter huge => the fresh intent is kept; the dead pid=200 record is pruned.
	rs.PruneDead(isAlive, identity, time.Hour, time.Unix(0, 0))
	got := map[string]bool{}
	for _, r := range rs.Read() {
		got[r.Token] = true
	}
	if !got["live"] || got["dead"] || !got["fresh-intent"] {
		t.Fatalf("prune should keep live+fresh-intent, drop dead; got %v", got)
	}
}
