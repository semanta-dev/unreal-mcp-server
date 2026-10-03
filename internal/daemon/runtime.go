package daemon

import (
	"context"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
)

// Runtime drives the pool's background liveness (MULTI_PROJECT_SYSTEM.md §3): a
// standing loop that renews every PID-alive instance (PID-gating the reaper) and
// then reaps genuinely-dead ones — crashed Leased editors become LEASE_LOST for
// their holder, dead warm Idle editors are removed, and the Router's bridge/binding
// maps are reconciled so nothing dangles. Expected-dead states are untouched (their
// own supervisors own them). Tick is separated from the ticker so it is
// deterministically unit-testable.
type Runtime struct {
	router      *Router
	lv          supervisor.Liveness
	reapTTL     time.Duration
	interval    time.Duration
	onLeaseLost func(sessionID string) // notify a session its lease crashed (HTTP push)
}

// NewRuntime builds the daemon liveness loop. reapTTL MUST exceed the renew interval
// (a live instance renewed each tick is never older than ~interval), and both are
// wall-clock-independent of any command/build (§3: the reaper is PID-gated, not
// duration-gated). onLeaseLost may be nil.
func NewRuntime(router *Router, lv supervisor.Liveness, interval, reapTTL time.Duration, onLeaseLost func(string)) *Runtime {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if reapTTL <= interval {
		reapTTL = 3 * interval // never reap something renewed within the last few ticks
	}
	return &Runtime{router: router, lv: lv, reapTTL: reapTTL, interval: interval, onLeaseLost: onLeaseLost}
}

// Tick performs one liveness pass: renew alive, reap dead, reconcile bridges, deliver
// LEASE_LOST. Returns the reaped set (for tests / metrics).
func (rt *Runtime) Tick() []supervisor.Reaped {
	pool := rt.router.Pool()
	pool.RenewAlive(rt.lv) // standing PID heartbeat: keep every alive editor fresh
	reaped := pool.ReapStale(rt.reapTTL)
	if len(reaped) == 0 {
		return nil
	}
	rt.router.ReconcileReaped(reaped) // close/drop crashed bridges + bindings
	for _, r := range reaped {
		if r.State == supervisor.Leased && r.LeasedBy != "" && rt.onLeaseLost != nil {
			rt.onLeaseLost(r.LeasedBy) // the holder's next call already gets LEASE_LOST; also push
		}
	}
	return reaped
}

// Run drives Tick on a ticker until ctx is cancelled.
func (rt *Runtime) Run(ctx context.Context) {
	t := time.NewTicker(rt.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rt.Tick()
		}
	}
}
