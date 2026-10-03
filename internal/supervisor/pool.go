// pool.go (formerly package editorpool) is the liveness state machine that lets one server (the
// Model-A daemon) hold N Unreal Editor instances and lease them 1:1 to agent
// sessions — the core of MULTI_PROJECT_SYSTEM.md. It implements that design's §3.2
// (state × PID) machine faithfully: a standing PID heartbeat renews every alive
// instance so the reaper (ReapStale) only ever acts on a genuinely dead process;
// an explicit expected-dead set {Starting, Restarting, NeedsRelaunch} is skipped
// by both reaper and crash-detector (their PID is legitimately dead while a lease
// is held); CrashDetected is a single locked op (no TOCTOU); a controlled restart
// is transparent to the lease, a failed one degrades to a retryable NeedsRelaunch,
// and only a genuine crash (Leased+dead-and-not-expected) loses the lease.
//
// The pool does not itself kill or spawn processes — the daemon does that and
// reports outcomes here — but it tells the daemon WHEN a kill-before-teardown is
// required (any {Starting,Restarting,NeedsRelaunch} instance can hold a transiently
// LIVE PID). The node id lives in the uexec.Session, not here; the pool tracks
// lease/PID/health/token per the design.
package supervisor

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// State is an instance's lifecycle position (§3.2 rows).
type State string

const (
	// Starting: spawned, not yet accepting reverse-connects; not leasable. Governed
	// by its own accepting-wait supervisor, not the generic heartbeat/reaper.
	Starting State = "starting"
	// Idle: accepting-ready and warm; leasable.
	Idle State = "idle"
	// Leased: held by exactly one session.
	Leased State = "leased"
	// Restarting: editor deliberately killed for a rebuild/editor_restart; PID is
	// expected-dead (or a fresh relaunch PID mid re-pin). Lease preserved.
	Restarting State = "restarting"
	// NeedsRelaunch: a controlled restart failed (build error / launch error / re-pin
	// timeout); lease preserved, holder retries via ensure_open.
	NeedsRelaunch State = "needs_relaunch"
	// Unhealthy: a genuine crash (LEASE_LOST already fired) or a reaped Idle editor.
	Unhealthy State = "unhealthy"
	// Stopped: intentionally terminal.
	Stopped State = "stopped"
)

// expectedDead is the set skipped by the reaper AND the crash-detector: their PID
// being dead is EXPECTED (or transiently alive during a relaunch), so neither may
// treat them as a crash.
func (s State) expectedDead() bool {
	return s == Starting || s == Restarting || s == NeedsRelaunch
}

// Instance is one editor the pool manages.
type Instance struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`     // project dir it has open
	InstanceID  int       `json:"instance_id"` // isolation id: filesystem-namespace / jobs key
	PID         int       `json:"pid"`         // OS process id
	Token       string    `json:"token"`       // -MCPInstanceToken correlation (write-ahead / kill authorization)
	Identity    string    `json:"identity"`    // PID-identity guard (start-time/image-path) vs PID recycling
	State       State     `json:"state"`
	LeasedBy    string    `json:"leased_by,omitempty"`
	LeasedAt    time.Time `json:"leased_at,omitempty"`
	LastHealthy time.Time `json:"last_healthy,omitempty"`
}

// AliveAndOwned reports whether this instance's PID is alive AND still the process
// we spawned (identity matches) — the recycled-PID guard. lv.Identity("") ok.
func (i *Instance) aliveAndOwned(lv Liveness) bool {
	if i.PID == 0 || !lv.IsAlive(i.PID) {
		return false
	}
	// Empty stored identity = not captured (older record); fall back to liveness only.
	return i.Identity == "" || lv.Identity(i.PID) == i.Identity
}

// Liveness abstracts OS process checks so the pool is unit-testable. Production
// wires it to lifecycle.IsAlive + a start-time/image-path identity reader.
//
// CONTRACT: implementations MUST be fast and side-effect-free, and MUST NOT call
// back into Pool — RenewAlive invokes these while holding the pool mutex (to keep
// the alive-check and the LastHealthy write atomic), and the mutex is non-reentrant,
// so a callback into Pool would self-deadlock.
type Liveness interface {
	IsAlive(pid int) bool
	// Identity returns a stable per-process token (e.g. start-time+image-path);
	// "" when the process is gone. Used to defeat PID recycling.
	Identity(pid int) string
}

var (
	ErrNotFound      = errors.New("editorpool: instance not found")
	ErrNoneAvailable = errors.New("editorpool: no idle instance for project")
	ErrNotLeased     = errors.New("editorpool: instance not leased by caller")
	ErrBadState      = errors.New("editorpool: instance not in the required state")
)

// Pool is a concurrency-safe registry of editor instances.
type Pool struct {
	mu        sync.Mutex
	instances map[string]*Instance
	now       func() time.Time
}

// New creates an empty pool. now defaults to time.Now (inject for tests).
func NewPool(now func() time.Time) *Pool {
	if now == nil {
		now = time.Now
	}
	return &Pool{instances: map[string]*Instance{}, now: now}
}

// RegisterStarting adds a freshly-spawned instance in the Starting state (not yet
// leasable). token/identity are captured at spawn (identity re-captured on each
// relaunch, see RestartEnd). MarkAccepting promotes it to Idle once it ACKs.
func (p *Pool) RegisterStarting(id, project string, instanceID, pid int, token, identity string) *Instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst := &Instance{
		ID: id, Project: project, InstanceID: instanceID, PID: pid,
		Token: token, Identity: identity, State: Starting, LastHealthy: p.now(),
	}
	p.instances[id] = inst
	return cloneInstance(inst)
}

// MarkAccepting promotes a Starting instance to Idle (accepting-ready, leasable).
func (p *Pool) MarkAccepting(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State != Starting {
		return ErrBadState
	}
	inst.State = Idle
	inst.LastHealthy = p.now()
	return nil
}

// Lease hands the caller an Idle (accepting-ready) instance for the project (or any
// project when project==""), transitioning it to Leased. 1:1 enforced. Only Idle
// instances are handed out, so a Starting/cold or expected-dead editor is never
// leased into an instant failure.
func (p *Pool) Lease(project, holder string) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var candidates []*Instance
	for _, inst := range p.instances {
		if inst.State != Idle {
			continue
		}
		if project != "" && inst.Project != project {
			continue
		}
		candidates = append(candidates, inst)
	}
	if len(candidates) == 0 {
		return nil, ErrNoneAvailable
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	inst := candidates[0]
	inst.State = Leased
	inst.LeasedBy = holder
	inst.LeasedAt = p.now()
	inst.LastHealthy = p.now()
	return cloneInstance(inst), nil
}

// Release returns a Leased instance to Idle. Only the holder may release it (empty
// holder forces it). Expected-dead / terminal states are left as-is.
func (p *Pool) Release(id, holder string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State == Leased && holder != "" && inst.LeasedBy != holder {
		return ErrNotLeased
	}
	if inst.State != Leased && inst.State != Idle {
		return nil // don't disturb Starting/Restarting/NeedsRelaunch/Unhealthy/Stopped
	}
	inst.State = Idle
	inst.LeasedBy = ""
	inst.LeasedAt = time.Time{}
	return nil
}

// LeaseRenew refreshes LastHealthy for the matching holder only, changing neither
// state nor ownership (the design's ownership-safe keepalive — never steals).
func (p *Pool) LeaseRenew(id, holder string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State == Leased && inst.LeasedBy != holder {
		return ErrNotLeased
	}
	inst.LastHealthy = p.now()
	return nil
}

// RenewAlive is the STANDING PID heartbeat: for every Idle/Leased instance whose
// process is alive-and-owned, refresh LastHealthy. This is what PID-gates the
// reaper — an alive editor (warm Idle OR Leased, whatever it is doing) is never
// reaped. Expected-dead states are intentionally NOT renewed (their PID is dead by
// design; their own supervisors own them). Call periodically.
func (p *Pool) RenewAlive(lv Liveness) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, inst := range p.instances {
		if inst.State != Idle && inst.State != Leased {
			continue
		}
		if inst.aliveAndOwned(lv) {
			inst.LastHealthy = now
		}
	}
}

// Reaped is one outcome of ReapStale.
type Reaped struct {
	ID       string
	State    State  // the state it was reaped FROM
	LeasedBy string // set when a Leased instance crashed -> LEASE_LOST target
	Removed  bool   // true when an Idle-warm dead editor was Removed
}

// ReapStale acts ONLY on Idle/Leased instances whose PID is no longer being renewed
// (RenewAlive skipped them because they are dead), older than ttl. A dead Leased
// instance is a CRASH -> Unhealthy + LEASE_LOST (LeasedBy returned). A dead Idle
// warm editor is Removed. Expected-dead {Starting,Restarting,NeedsRelaunch} and
// terminal {Unhealthy,Stopped} are skipped. Returns the reaped instances so the
// daemon can fire LEASE_LOST / free RAM.
func (p *Pool) ReapStale(ttl time.Duration) []Reaped {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := p.now().Add(-ttl)
	var out []Reaped
	for id, inst := range p.instances {
		if inst.State != Idle && inst.State != Leased {
			continue // expected-dead + terminal are exempt
		}
		if !inst.LastHealthy.Before(cutoff) {
			continue // still being renewed => alive
		}
		switch inst.State {
		case Leased:
			r := Reaped{ID: id, State: Leased, LeasedBy: inst.LeasedBy}
			inst.State = Unhealthy
			inst.LeasedBy = ""
			inst.LeasedAt = time.Time{}
			out = append(out, r)
		case Idle:
			delete(p.instances, id)
			out = append(out, Reaped{ID: id, State: Idle, Removed: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CrashDetected is the single locked crash-mark: given a PID observed dead, it
// re-reads state==Leased-and-not-expected UNDER THE LOCK before acting (closing the
// Leased->Restarting TOCTOU), and only then marks Unhealthy and returns the prior
// LeasedBy for a LEASE_LOST. Returns crashed=false (with LeasedBy="") if the
// instance has since moved to an expected-dead state (a controlled restart began in
// the gap) or the PID no longer matches — i.e. it was NOT a crash.
func (p *Pool) CrashDetected(id string, pid int) (leasedBy string, crashed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return "", false
	}
	// Re-validate under the lock: only a Leased (not expected-dead) instance whose
	// PID still matches the observed-dead pid is a genuine crash.
	if inst.State != Leased || inst.PID != pid {
		return "", false
	}
	leasedBy = inst.LeasedBy
	inst.State = Unhealthy
	inst.LeasedBy = ""
	inst.LeasedAt = time.Time{}
	return leasedBy, true
}

// --- controlled restart (§3.1) ------------------------------------------------

// RestartBegin transitions Leased -> Restarting under the lock (the caller kills the
// editor strictly AFTER this returns, so a concurrent CrashDetected can't see
// Leased+dead). Only the holder may begin its own restart.
func (p *Pool) RestartBegin(id, holder string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State != Leased {
		return ErrBadState
	}
	if holder != "" && inst.LeasedBy != holder {
		return ErrNotLeased
	}
	inst.State = Restarting
	return nil
}

// RestartEnd records a fresh relaunch PID+identity on a Restarting/NeedsRelaunch
// instance (identity re-captured per §3.2 so the new editor isn't a false recycled-
// PID crash). The hold stays until RepinSucceeded / the failure path.
func (p *Pool) RestartEnd(id string, newPID int, newIdentity string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State != Restarting && inst.State != NeedsRelaunch {
		return ErrBadState
	}
	inst.PID = newPID
	inst.Identity = newIdentity
	return nil
}

// RepinSucceeded ends a controlled restart successfully: Restarting/NeedsRelaunch ->
// Leased, refreshing LastHealthy in the same step (so the reaper doesn't trip the
// instant the hold lifts).
func (p *Pool) RepinSucceeded(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State != Restarting && inst.State != NeedsRelaunch {
		return ErrBadState
	}
	inst.State = Leased
	inst.LastHealthy = p.now()
	return nil
}

// RestartFailed degrades a failed controlled restart to the lease-PRESERVING
// NeedsRelaunch state (build error / launch error / re-pin timeout). The holder
// keeps the lease and retries via ensure_open (RestartEnd + RepinSucceeded).
func (p *Pool) RestartFailed(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return ErrNotFound
	}
	if inst.State != Restarting && inst.State != NeedsRelaunch {
		return ErrBadState
	}
	inst.State = NeedsRelaunch
	return nil
}

// MarkUnhealthy forces an instance to Unhealthy and frees its lease (a genuine
// crash detected out of band). Returns the prior holder for LEASE_LOST.
func (p *Pool) MarkUnhealthy(id string) (leasedBy string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return "", ErrNotFound
	}
	leasedBy = inst.LeasedBy
	inst.State = Unhealthy
	inst.LeasedBy = ""
	inst.LeasedAt = time.Time{}
	return leasedBy, nil
}

// NeedsKillBeforeTeardown reports whether tearing down / relaunching this instance
// must kill its PID first — true for any expected-dead state, because it can hold a
// transiently LIVE (alive-but-not-accepting) editor. The daemon consults this before
// Remove/relaunch/collapse.
func (p *Pool) NeedsKillBeforeTeardown(id string) (pid int, mustKill bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return 0, false
	}
	return inst.PID, inst.State.expectedDead()
}

// Remove drops an instance (after a confirmed kill / confirmed death).
func (p *Pool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.instances, id)
}

// RemoveIfIdle atomically removes an instance ONLY if it is still Idle, returning
// true on removal. The §3.2 warm-Idle accepting-probe uses this to close the
// Idle→Leased TOCTOU (symmetric to CrashDetected): a probe that decided to kill a
// non-ACKing warm editor must RemoveIfIdle FIRST and kill the PID only if it
// returns true; if Lease() won the race and moved it to Leased, RemoveIfIdle
// returns false and the probe aborts the kill (the lease now owns liveness), so a
// just-leased live editor is never killed on its holder's first call.
func (p *Pool) RemoveIfIdle(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok || inst.State != Idle {
		return false
	}
	delete(p.instances, id)
	return true
}

// Get returns a snapshot of one instance.
func (p *Pool) Get(id string) (Instance, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return Instance{}, false
	}
	return *inst, true
}

// List returns a snapshot of all instances, sorted by id.
func (p *Pool) List() []Instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Instance, 0, len(p.instances))
	for _, inst := range p.instances {
		out = append(out, *inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NextInstanceID returns the smallest non-negative isolation id not in use.
func (p *Pool) NextInstanceID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	used := map[int]bool{}
	for _, inst := range p.instances {
		used[inst.InstanceID] = true
	}
	for i := 0; ; i++ {
		if !used[i] {
			return i
		}
	}
}

func cloneInstance(inst *Instance) *Instance {
	c := *inst
	return &c
}
