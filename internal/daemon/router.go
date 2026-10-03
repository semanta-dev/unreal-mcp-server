// Package daemon is the Model-A router (MULTI_PROJECT_SYSTEM.md §0/§3): one
// long-lived process binds each agent MCP session to a 1:1 editor lease and routes
// that session's tool calls to the right per-instance bridge. It sits on top of
// supervisor.Pool (the liveness state machine) and a shared uexec.Discovery, adding the
// session→lease binding, lease-or-spawn with a per-project spawn guard (so two
// sessions attaching to the same project can't double-spawn), and the
// kill-before-teardown discipline for expected-dead instances.
//
// Editor bring-up (spawn + accepting-wait + node pin) and process kills are behind
// the supervisor.Spawner interface (lifecycle + uexec in production, fakes in tests), so the
// routing logic is unit-testable without a live editor.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"sync"

	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
)

// Errors surfaced to a session (mapped to MCP error codes by the transport layer).
var (
	ErrNoProjectAttached = errors.New("daemon: NO_PROJECT_ATTACHED")
	ErrLeaseLost         = errors.New("daemon: LEASE_LOST")
	ErrRestartInProgress = errors.New("daemon: RESTART_IN_PROGRESS")
)

// Router binds sessions to leases and routes to per-instance editors.
type Router struct {
	mu       sync.Mutex
	pool     *supervisor.Pool
	spawner  supervisor.Spawner
	newToken func() string

	bindings map[string]string            // sessionID -> instanceID
	editors  map[string]supervisor.Editor // instanceID -> its bridge
	seq      int
}

// NewRouter builds a router. newToken generates a fresh -MCPInstanceToken (uuid in
// prod); nil defaults to a monotonic stub (fine for a single-daemon test).
func NewRouter(pool *supervisor.Pool, spawner supervisor.Spawner, newToken func() string) *Router {
	if newToken == nil {
		var n int
		var mu sync.Mutex
		newToken = func() string { mu.Lock(); n++; mu.Unlock(); return fmt.Sprintf("tok-%d", n) }
	}
	return &Router{
		pool: pool, spawner: spawner, newToken: newToken,
		bindings: map[string]string{}, editors: map[string]supervisor.Editor{},
	}
}

// Attach binds a session to a 1:1 editor lease for project, REUSING a free warm
// editor if one exists (that reuse is the double-spawn guard) and spawning a fresh
// one otherwise. Idempotent per session. Two different sessions on the same project
// each get their OWN editor (1:1), which is correct — not a double-spawn.
func (r *Router) Attach(ctx context.Context, sessionID, project string) (instanceID string, err error) {
	r.mu.Lock()
	if id, ok := r.bindings[sessionID]; ok {
		r.mu.Unlock()
		return id, nil // already attached (idempotent)
	}
	// Prefer reusing a warm idle editor for this project (the double-spawn guard).
	if lease, lerr := r.pool.Lease(project, sessionID); lerr == nil {
		r.bindings[sessionID] = lease.ID
		r.mu.Unlock()
		return lease.ID, nil
	}
	r.mu.Unlock()

	// No warm editor: spawn one for THIS session. Spawn (slow: cold start) runs
	// outside the lock; it wrote the write-ahead intent + waited for accepting.
	token := r.newToken()
	ed, pid, identity, serr := r.spawner.Spawn(ctx, project, token)
	if serr != nil {
		return "", serr
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Idempotency double-check: a concurrent Attach for the SAME session may have
	// finished while we were spawning — if so, we double-spawned; tear down the extra.
	if id, ok := r.bindings[sessionID]; ok {
		_ = r.spawner.Kill(pid)
		_ = ed.Close()
		return id, nil
	}
	id := r.newInstanceID()
	r.pool.RegisterStarting(id, project, r.pool.NextInstanceID(), pid, token, identity)
	_ = r.pool.MarkAccepting(id) // Spawn already confirmed accepting-ready
	r.editors[id] = ed
	lease, lerr := r.pool.Lease(project, sessionID)
	if lerr != nil {
		r.teardownLocked(id, pid) // clean up the editor we just made
		return "", fmt.Errorf("daemon: lease after spawn failed: %w", lerr)
	}
	r.bindings[sessionID] = lease.ID
	return lease.ID, nil
}

// editorBridge returns the bridge of an instance's current editor (nil if gone).
func (r *Router) editorBridge(id string) *bridge.Bridge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if eh, ok := r.editors[id].(*supervisor.EditorHandle); ok {
		return eh.Bridge()
	}
	return nil
}

// Transfer moves a lease binding from one session to another (draining-lease
// adoption). The instance never passes through Idle, so no other project can take it.
func (r *Router) Transfer(from, to string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bindings[from]
	if !ok {
		return "", ErrNoProjectAttached
	}
	if err := r.pool.Transfer(id, from, to); err != nil {
		return "", err
	}
	delete(r.bindings, from)
	r.bindings[to] = id
	return id, nil
}

// Resolve returns the editor bridge for a session's lease, or NO_PROJECT_ATTACHED /
// LEASE_LOST. It re-checks the lease is still Leased by this session (a reaped lease
// returns LEASE_LOST so the agent re-attaches).
func (r *Router) Resolve(sessionID string) (supervisor.Editor, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bindings[sessionID]
	if !ok {
		return nil, "", ErrNoProjectAttached
	}
	inst, ok := r.pool.Get(id)
	if !ok || inst.State == supervisor.Unhealthy || inst.State == supervisor.Stopped || inst.LeasedBy != sessionID {
		delete(r.bindings, sessionID) // lease is gone
		return nil, "", ErrLeaseLost
	}
	// During a controlled restart, ONLY the watchdog may touch the bridge/node pin
	// (§3.1 step 5) — a holder call routed to the bridge now would race the re-pin.
	if inst.State == supervisor.Restarting || inst.State == supervisor.NeedsRelaunch {
		return nil, inst.Project, ErrRestartInProgress // binding kept; holder retries
	}
	ed := r.editors[id]
	return ed, inst.Project, nil
}

// Release frees a session's lease (session close / project_release). An expected-dead
// instance (Starting/Restarting/NeedsRelaunch) can hold a live-but-not-accepting
// editor that pool.Release would leave orphaned (ReapStale skips expected-dead), so
// Release KILLS-first-and-removes it (universal kill-before-teardown, §3.2); a
// healthy Idle/Leased editor is kept warm for re-lease.
func (r *Router) Release(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bindings[sessionID]
	if !ok {
		return
	}
	delete(r.bindings, sessionID)
	if pid, must := r.pool.NeedsKillBeforeTeardown(id); must {
		r.teardownLocked(id, pid) // expected-dead may be alive -> kill + remove, no leak
		return
	}
	_ = r.pool.Release(id, sessionID) // healthy -> back to warm Idle
}

// teardownLocked kills-first-if-needed and removes an instance + its bridge. Caller
// holds r.mu.
func (r *Router) teardownLocked(id string, pid int) {
	if _, must := r.pool.NeedsKillBeforeTeardown(id); must && pid > 0 {
		_ = r.spawner.Kill(pid) // kill-before-teardown (expected-dead may be alive)
	}
	if ed := r.editors[id]; ed != nil {
		_ = ed.Close()
		delete(r.editors, id)
	}
	r.pool.Remove(id)
}

// Teardown kills+removes an instance (e.g. a reaped crash / admin evict). Cleans the
// bridge even if the pool already removed the instance out-of-band (so r.editors
// can't leak).
func (r *Router) Teardown(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid := 0
	if inst, ok := r.pool.Get(id); ok {
		pid = inst.PID
	}
	r.teardownLocked(id, pid) // closes r.editors[id] + pool.Remove(id) regardless
}

// ReconcileReaped is called by the daemon's reaper loop after ReapStale: it closes
// and drops the bridge for every instance the pool reaped/removed (a crashed Leased
// editor's LEASE_LOST is delivered separately from Reaped.LeasedBy), so a bridge is
// never left dangling after an out-of-band pool removal.
func (r *Router) ReconcileReaped(reaped []supervisor.Reaped) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rp := range reaped {
		if ed := r.editors[rp.ID]; ed != nil {
			_ = ed.Close()
			delete(r.editors, rp.ID)
		}
		// Drop any binding pointing at a crashed/removed instance.
		for sid, iid := range r.bindings {
			if iid == rp.ID {
				delete(r.bindings, sid)
			}
		}
	}
}

func (r *Router) newInstanceID() string {
	r.seq++
	return fmt.Sprintf("ed%d", r.seq)
}

// Pool exposes the underlying pool (for the daemon's heartbeat/reaper loops).
func (r *Router) Pool() *supervisor.Pool { return r.pool }

// InstanceFor returns the instance id a session is bound to (for per-lease scoping).
func (r *Router) InstanceFor(sessionID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bindings[sessionID]
	return id, ok
}

// RestartInfo is what a controlled restart needs to tear down + relaunch a lease.
type RestartInfo struct {
	ID        string
	Token     string
	Project   string
	OldPID    int
	OldEditor supervisor.Editor
}

// BeginRestart transitions the holder's lease Leased→Restarting (§3.1) so the lease is
// PRESERVED (the holder gets RESTART_IN_PROGRESS, not LEASE_LOST, and the reaper skips
// the expected-dead Restarting state) while the editor is rebuilt/relaunched. Returns
// the info to relaunch the SAME editor identity (same token). Only the holder may
// restart.
func (r *Router) BeginRestart(sessionID string) (RestartInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bindings[sessionID]
	if !ok {
		return RestartInfo{}, ErrNoProjectAttached
	}
	inst, ok := r.pool.Get(id)
	if !ok {
		delete(r.bindings, sessionID)
		return RestartInfo{}, ErrLeaseLost
	}
	if err := r.pool.RestartBegin(id, sessionID); err != nil {
		return RestartInfo{}, err
	}
	return RestartInfo{ID: id, Token: inst.Token, Project: inst.Project, OldPID: inst.PID, OldEditor: r.editors[id]}, nil
}

// AbortRestart returns a Restarting lease to Leased on the editor it already had
// (a graceful stop refused, nothing was killed).
func (r *Router) AbortRestart(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pool.RepinSucceeded(id)
}

// EndRestart re-pins the lease onto the freshly-relaunched editor: swap in the new
// bridge and complete the pool transition (RestartEnd + RepinSucceeded → Leased). The
// holder's lease survives the whole rebuild with no LEASE_LOST.
func (r *Router) EndRestart(id string, newEditor supervisor.Editor, newPID int, newIdentity string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.pool.RestartEnd(id, newPID, newIdentity); err != nil {
		return err
	}
	if err := r.pool.RepinSucceeded(id); err != nil {
		return err
	}
	r.editors[id] = newEditor // re-pin the per-instance bridge to the new editor
	return nil
}
