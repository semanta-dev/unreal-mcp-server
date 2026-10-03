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

	"github.com/jdziat/unreal-mcp-server/internal/session"
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

	bindings  map[string]string            // sessionID -> instanceID
	editors   map[string]supervisor.Editor // instanceID -> its bridge
	pending   map[string]*pendingSpawn     // sessionID -> its editor's cold start in progress
	abandoned map[string][]*pendingSpawn   // project key -> starts whose session ended (adoptable)
	launchMu  map[string]*sync.Mutex       // project key -> serializes launches (see launchLock)
	life      context.Context              // cancelled by Close: no spawn outlives the daemon
	stop      context.CancelFunc
	seq       int
}

// pendingSpawn is one editor cold start. It runs detached from the attach call: a cold
// start (tens of seconds to minutes) outlives a tool call, and found live (P7) tying it
// to the call's deadline killed every editor that was still loading. session is
// guarded by Router.mu; id and err are written before done closes.
type pendingSpawn struct {
	project string
	session string // who gets the lease ("" while abandoned)
	done    chan struct{}
	id      string
	err     error
}

// NewRouter builds a router. newToken generates a fresh -MCPInstanceToken (uuid in
// prod); nil defaults to a monotonic stub (fine for a single-daemon test).
func NewRouter(pool *supervisor.Pool, spawner supervisor.Spawner, newToken func() string) *Router {
	if newToken == nil {
		var n int
		var mu sync.Mutex
		newToken = func() string { mu.Lock(); n++; mu.Unlock(); return fmt.Sprintf("tok-%d", n) }
	}
	life, stop := context.WithCancel(context.Background())
	return &Router{
		pool: pool, spawner: spawner, newToken: newToken,
		bindings: map[string]string{}, editors: map[string]supervisor.Editor{},
		pending: map[string]*pendingSpawn{}, abandoned: map[string][]*pendingSpawn{},
		launchMu: map[string]*sync.Mutex{}, life: life, stop: stop,
	}
}

// Close cancels in-flight cold starts (the daemon is shutting down).
func (r *Router) Close() { r.stop() }

// Attach binds a session to a 1:1 editor lease for project, REUSING a free warm
// editor if one exists (that reuse is the double-spawn guard), adopting a cold start
// an ended session left behind, and spawning a fresh one otherwise. Idempotent per
// session. Two different sessions on the same project each get their OWN editor (1:1).
// When the call's context ends before the editor is ready it returns
// session.ErrEditorStarting; attaching again resumes the wait.
func (r *Router) Attach(ctx context.Context, sessionID, project string) (instanceID string, err error) {
	key := session.ProjectKey(project)
	r.mu.Lock()
	if id, ok := r.bindings[sessionID]; ok {
		r.mu.Unlock()
		return id, nil // already attached (idempotent)
	}
	p := r.pending[sessionID]
	if p != nil && isDone(p) {
		// A start that finished while nobody waited: hand back its result once, or
		// drop it when the session now wants another project.
		delete(r.pending, sessionID)
		if session.ProjectKey(p.project) == key {
			r.mu.Unlock()
			return p.id, p.err
		}
		p = nil
	}
	if p != nil && session.ProjectKey(p.project) != key {
		r.mu.Unlock()
		return "", fmt.Errorf("%w: this session is still starting an editor for %s", session.ErrEditorStarting, p.project)
	}
	if p == nil {
		if lease, lerr := r.pool.Lease(project, sessionID); lerr == nil {
			r.bindings[sessionID] = lease.ID
			r.mu.Unlock()
			return lease.ID, nil
		}
		for len(r.abandoned[key]) > 0 && p == nil {
			q := r.abandoned[key]
			r.abandoned[key] = q[1:]
			if !isDone(q[0]) {
				p = q[0] // adopt a start an ended session left
			}
		}
		if p == nil {
			p = &pendingSpawn{project: project, done: make(chan struct{})}
			go r.spawnFor(p)
		}
		p.session = sessionID
		r.pending[sessionID] = p
	}
	r.mu.Unlock()
	select {
	case <-p.done:
	case <-ctx.Done():
		if !isDone(p) {
			return "", fmt.Errorf("%w: %s", session.ErrEditorStarting, project) // it keeps starting
		}
	}
	r.mu.Lock()
	if r.pending[sessionID] == p {
		delete(r.pending, sessionID)
	}
	r.mu.Unlock()
	return p.id, p.err
}

func isDone(p *pendingSpawn) bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// launchLock serializes cold starts of one project: a spawner treats every node it
// saw before its launch as not its own, so a second start must begin after the first
// editor is known, or it could probe (and take over) that editor's channel.
func (r *Router) launchLock(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.launchMu[key]
	if m == nil {
		m = &sync.Mutex{}
		r.launchMu[key] = m
	}
	return m
}

// spawnFor runs one cold start and leases the editor to whichever session holds the
// start when it is ready; with none (ended, not adopted) it stays warm and unleased.
func (r *Router) spawnFor(p *pendingSpawn) {
	key := session.ProjectKey(p.project)
	token := r.newToken()
	ed, pid, identity, serr := r.SpawnSerialized(r.life, p.project, token)
	r.mu.Lock()
	defer func() {
		// done closes while r.mu is held: Release never sees a finished start as
		// in flight (it would queue it for adoption after its cleanup ran).
		close(p.done)
		r.mu.Unlock()
	}()
	r.dropAbandonedLocked(key, p)
	if serr != nil {
		p.err = serr
		return
	}
	// The session got bound meanwhile (e.g. it leased a warm editor): drop the extra.
	if id, ok := r.bindings[p.session]; ok && p.session != "" {
		_ = r.spawner.Kill(pid)
		_ = ed.Close()
		p.id = id
		return
	}
	id := r.newInstanceID()
	r.pool.RegisterStarting(id, p.project, r.pool.NextInstanceID(), pid, token, identity)
	_ = r.pool.MarkAccepting(id) // Spawn already confirmed accepting-ready
	r.editors[id] = ed
	if p.session == "" {
		p.err = ErrNoProjectAttached // nobody to lease it to: it stays warm (Idle) for re-lease
		return
	}
	lease, lerr := r.pool.Lease(p.project, p.session)
	if lerr != nil {
		r.teardownLocked(id, pid) // clean up the editor we just made
		p.err = fmt.Errorf("daemon: lease after spawn failed: %w", lerr)
		return
	}
	r.bindings[p.session] = lease.ID
	p.id = lease.ID
}

// SpawnSerialized launches an editor for project under the project's launch lock (cold
// starts and restart relaunches alike), and never after the router was closed.
func (r *Router) SpawnSerialized(ctx context.Context, project, token string) (supervisor.Editor, int, string, error) {
	lm := r.launchLock(session.ProjectKey(project))
	lm.Lock()
	defer lm.Unlock()
	if err := r.life.Err(); err != nil {
		return nil, 0, "", fmt.Errorf("daemon shutting down: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, "", err
	}
	return r.spawner.Spawn(ctx, project, token)
}

func (r *Router) dropAbandonedLocked(key string, p *pendingSpawn) {
	q := r.abandoned[key]
	for i, x := range q {
		if x == p {
			r.abandoned[key] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
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
	if p := r.pending[sessionID]; p != nil {
		delete(r.pending, sessionID)
		if !isDone(p) {
			// Its editor comes up warm and unleased, unless another session adopts it.
			p.session = ""
			key := session.ProjectKey(p.project)
			r.abandoned[key] = append(r.abandoned[key], p)
		}
	}
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
