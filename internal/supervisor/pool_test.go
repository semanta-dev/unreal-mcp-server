package supervisor

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) adv(d time.Duration) { c.t = c.t.Add(d) }

// fakeLiveness: alive[pid] controls IsAlive; ident[pid] the identity token.
type fakeLiveness struct {
	alive map[int]bool
	ident map[int]string
}

func newLv() *fakeLiveness                      { return &fakeLiveness{alive: map[int]bool{}, ident: map[int]string{}} }
func (l *fakeLiveness) IsAlive(pid int) bool    { return l.alive[pid] }
func (l *fakeLiveness) Identity(pid int) string { return l.ident[pid] }
func (l *fakeLiveness) set(pid int, alive bool, id string) {
	l.alive[pid] = alive
	l.ident[pid] = id
}

func newPool() (*Pool, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return NewPool(c.now), c
}

// helper: register-accepting-lease in one shot.
func (p *Pool) mustLease(t *testing.T, id, project string, pid int, holder string) *Instance {
	t.Helper()
	p.RegisterStarting(id, project, 0, pid, "tok-"+id, "idn-"+id)
	if err := p.MarkAccepting(id); err != nil {
		t.Fatalf("MarkAccepting: %v", err)
	}
	inst, err := p.Lease(project, holder)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	return inst
}

func TestStartingNotLeasableUntilAccepting(t *testing.T) {
	p, _ := newPool()
	p.RegisterStarting("a", "/p", 0, 100, "t", "i")
	if _, err := p.Lease("/p", "agent"); err != ErrNoneAvailable {
		t.Fatalf("Starting must not be leasable, got %v", err)
	}
	if err := p.MarkAccepting("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Lease("/p", "agent"); err != nil {
		t.Fatalf("accepting instance should lease: %v", err)
	}
}

func TestLease1to1AndProjectAffinity(t *testing.T) {
	p, _ := newPool()
	l1 := p.mustLease(t, "a", "/A", 1, "agentX")
	if l1.State != Leased || l1.LeasedBy != "agentX" {
		t.Fatalf("bad lease %+v", l1)
	}
	// second lease of same project: no other instance -> none available
	if _, err := p.Lease("/A", "agentY"); err != ErrNoneAvailable {
		t.Fatalf("expected none, got %v", err)
	}
	// different project pins correctly
	p.mustLease(t, "b", "/B", 2, "agentZ")
	if _, err := p.Lease("/A", "agentY"); err != ErrNoneAvailable {
		t.Fatalf("project affinity leak: %v", err)
	}
}

func TestControlledRestartPreservesLease_NoFalseLeaseLost(t *testing.T) {
	p, c := newPool()
	lv := newLv()
	l := p.mustLease(t, "a", "/p", 100, "agent")
	lv.set(100, true, "idn-a")

	// Begin a controlled restart (editor about to be killed).
	if err := p.RestartBegin("a", "agent"); err != nil {
		t.Fatal(err)
	}
	// Editor now dead (killed for the rebuild).
	lv.set(100, false, "")
	// Minutes pass with no renewal.
	c.adv(10 * time.Minute)
	// Standing heartbeat skips expected-dead; reaper must NOT touch it.
	p.RenewAlive(lv)
	if reaped := p.ReapStale(time.Minute); len(reaped) != 0 {
		t.Fatalf("controlled restart must not be reaped, got %+v", reaped)
	}
	// A crash-detector observing the dead PID must NOT fire (state is Restarting).
	if by, crashed := p.CrashDetected("a", 100); crashed {
		t.Fatalf("controlled restart must not crash-detect, got holder=%q", by)
	}
	// Relaunch + re-pin succeeds -> lease intact.
	if err := p.RestartEnd("a", 200, "idn-a2"); err != nil {
		t.Fatal(err)
	}
	lv.set(200, true, "idn-a2")
	if err := p.RepinSucceeded("a"); err != nil {
		t.Fatal(err)
	}
	inst, _ := p.Get("a")
	if inst.State != Leased || inst.LeasedBy != "agent" || inst.PID != 200 {
		t.Fatalf("lease not preserved across restart: %+v", inst)
	}
	_ = l
}

func TestLeasedToRestartingTOCTOU(t *testing.T) {
	p, _ := newPool()
	p.mustLease(t, "a", "/p", 100, "agent")
	// RestartBegin flips to Restarting; a crash-detector that snapshotted Leased+dead
	// in the gap must be refused by the under-lock re-read.
	if err := p.RestartBegin("a", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, crashed := p.CrashDetected("a", 100); crashed {
		t.Fatal("CrashDetected must re-read state under lock and refuse (now Restarting)")
	}
	// A stale PID also refuses.
	p.RepinSucceeded("a") // back to Leased with PID still 100
	if _, crashed := p.CrashDetected("a", 999); crashed {
		t.Fatal("CrashDetected with a mismatched pid must refuse")
	}
}

func TestStandingHeartbeatPIDGatesReaper(t *testing.T) {
	p, c := newPool()
	lv := newLv()
	// Busy Leased editor: alive, but issues NO LeaseRenew (single-flight busy).
	p.mustLease(t, "busy", "/p", 100, "agent")
	lv.set(100, true, "idn-busy")
	// Dead Leased editor (a real crash): not alive.
	p.mustLease(t, "dead", "/q", 200, "agent2")
	lv.set(200, false, "")

	c.adv(5 * time.Minute)
	p.RenewAlive(lv) // renews busy (alive), skips dead
	reaped := p.ReapStale(time.Minute)
	if len(reaped) != 1 || reaped[0].ID != "dead" || reaped[0].State != Leased || reaped[0].LeasedBy != "agent2" {
		t.Fatalf("only the dead leased editor should crash-reap: %+v", reaped)
	}
	// busy survived and is still Leased.
	if b, _ := p.Get("busy"); b.State != Leased {
		t.Fatalf("busy alive editor was reaped: %+v", b)
	}
}

func TestWarmIdleDeadRemovedNotLeaseLost(t *testing.T) {
	p, c := newPool()
	lv := newLv()
	p.RegisterStarting("w", "/p", 0, 100, "t", "i")
	p.MarkAccepting("w")   // Idle warm
	lv.set(100, false, "") // died while idle
	c.adv(5 * time.Minute)
	p.RenewAlive(lv)
	reaped := p.ReapStale(time.Minute)
	if len(reaped) != 1 || !reaped[0].Removed || reaped[0].LeasedBy != "" {
		t.Fatalf("dead warm Idle should be Removed (no LEASE_LOST): %+v", reaped)
	}
	if _, ok := p.Get("w"); ok {
		t.Fatal("removed instance should be gone")
	}
}

func TestFailedRestartToNeedsRelaunchThenRetry(t *testing.T) {
	p, c := newPool()
	lv := newLv()
	p.mustLease(t, "a", "/p", 100, "agent")
	p.RestartBegin("a", "agent")
	lv.set(100, false, "")
	// Build failed -> NeedsRelaunch (lease preserved).
	if err := p.RestartFailed("a"); err != nil {
		t.Fatal(err)
	}
	inst, _ := p.Get("a")
	if inst.State != NeedsRelaunch || inst.LeasedBy != "agent" {
		t.Fatalf("failed restart should preserve lease as NeedsRelaunch: %+v", inst)
	}
	// NeedsRelaunch is expected-dead: not reaped even long-idle.
	c.adv(20 * time.Minute)
	p.RenewAlive(lv)
	if reaped := p.ReapStale(time.Minute); len(reaped) != 0 {
		t.Fatalf("NeedsRelaunch must not be reaped: %+v", reaped)
	}
	// Holder retries via ensure_open: RestartEnd + RepinSucceeded -> Leased.
	p.RestartEnd("a", 300, "idn-a3")
	lv.set(300, true, "idn-a3")
	if err := p.RepinSucceeded("a"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := p.Get("a"); inst.State != Leased || inst.PID != 300 {
		t.Fatalf("retry should restore lease: %+v", inst)
	}
}

func TestNeedsKillBeforeTeardown(t *testing.T) {
	p, _ := newPool()
	p.mustLease(t, "a", "/p", 100, "agent")
	if _, must := p.NeedsKillBeforeTeardown("a"); must {
		t.Fatal("Leased (alive) should not need kill-before-teardown")
	}
	p.RestartBegin("a", "agent")
	if pid, must := p.NeedsKillBeforeTeardown("a"); !must || pid != 100 {
		t.Fatalf("Restarting must kill-first (pid=%d must=%v)", pid, must)
	}
	// Starting also needs kill-first (alive-but-never-accepting).
	p.RegisterStarting("b", "/p", 1, 200, "t", "i")
	if _, must := p.NeedsKillBeforeTeardown("b"); !must {
		t.Fatal("Starting must kill-first")
	}
}

func TestRecycledPIDGuard(t *testing.T) {
	p, c := newPool()
	lv := newLv()
	p.mustLease(t, "a", "/p", 100, "agent")
	// PID 100 is alive but is now a DIFFERENT process (recycled) -> identity mismatch.
	lv.set(100, true, "some-other-process")
	c.adv(5 * time.Minute)
	p.RenewAlive(lv) // must NOT renew (identity mismatch) -> treated as dead
	reaped := p.ReapStale(time.Minute)
	if len(reaped) != 1 || reaped[0].ID != "a" {
		t.Fatalf("recycled-PID (identity mismatch) must be treated as dead/crash: %+v", reaped)
	}
}

func TestCrashDetectedPositiveAndNegative(t *testing.T) {
	p, _ := newPool()
	p.mustLease(t, "a", "/p", 100, "owner")
	// Positive: a Leased instance whose PID is confirmed dead -> crash, returns holder.
	by, crashed := p.CrashDetected("a", 100)
	if !crashed || by != "owner" {
		t.Fatalf("expected crash with holder=owner, got crashed=%v by=%q", crashed, by)
	}
	if inst, _ := p.Get("a"); inst.State != Unhealthy || inst.LeasedBy != "" {
		t.Fatalf("crashed instance should be Unhealthy + lease cleared: %+v", inst)
	}
	// Negative: unknown id.
	if _, c := p.CrashDetected("nope", 1); c {
		t.Fatal("unknown id must not crash")
	}
	// Negative: already Unhealthy (not Leased) must not re-crash.
	if _, c := p.CrashDetected("a", 100); c {
		t.Fatal("non-Leased must not crash")
	}
}

func TestRemoveIfIdleTOCTOU(t *testing.T) {
	p, _ := newPool()
	// Idle -> removed.
	p.RegisterStarting("w", "/p", 0, 100, "t", "i")
	p.MarkAccepting("w")
	if !p.RemoveIfIdle("w") {
		t.Fatal("RemoveIfIdle should remove an Idle instance")
	}
	if _, ok := p.Get("w"); ok {
		t.Fatal("instance should be gone")
	}
	// Leased -> NOT removed (the probe must abort its kill).
	p.mustLease(t, "l", "/p", 200, "agent")
	if p.RemoveIfIdle("l") {
		t.Fatal("RemoveIfIdle must refuse a Leased instance")
	}
	if inst, ok := p.Get("l"); !ok || inst.State != Leased {
		t.Fatalf("Leased instance must survive RemoveIfIdle: %+v", inst)
	}
}

func TestGuardEdgeCases(t *testing.T) {
	p, _ := newPool()
	p.mustLease(t, "a", "/p", 100, "agent")
	// Double RestartBegin: second must fail (not Leased anymore).
	if err := p.RestartBegin("a", "agent"); err != nil {
		t.Fatal(err)
	}
	if err := p.RestartBegin("a", "agent"); err != ErrBadState {
		t.Fatalf("double RestartBegin should fail, got %v", err)
	}
	// Release during Restarting is a no-op (doesn't disturb the hold).
	if err := p.Release("a", "agent"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := p.Get("a"); inst.State != Restarting {
		t.Fatalf("Release must not disturb Restarting: %+v", inst)
	}
	// MarkAccepting on a non-Starting instance fails.
	if err := p.MarkAccepting("a"); err != ErrBadState {
		t.Fatalf("MarkAccepting non-Starting should fail, got %v", err)
	}
	// Lease must skip expected-dead: a project with only a Restarting instance -> none.
	if _, err := p.Lease("/p", "other"); err != ErrNoneAvailable {
		t.Fatalf("Lease must skip expected-dead, got %v", err)
	}
}

func TestLeaseRenewNeverSteals(t *testing.T) {
	p, _ := newPool()
	p.mustLease(t, "a", "/p", 100, "owner")
	if err := p.LeaseRenew("a", "intruder"); err != ErrNotLeased {
		t.Fatalf("non-holder renew must be refused, got %v", err)
	}
	if err := p.LeaseRenew("a", "owner"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := p.Get("a"); inst.State != Leased || inst.LeasedBy != "owner" {
		t.Fatalf("renew must not change ownership/state: %+v", inst)
	}
}
