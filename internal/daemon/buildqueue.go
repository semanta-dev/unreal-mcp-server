package daemon

import "sync"

// BuildQueue serializes C++ builds per engine install (≤1 running per engine) with
// OBSERVABLE state (MULTI_PROJECT_SYSTEM.md §7). This is what lets the §3.1 restart
// watchdog distinguish a KNOWN-queued build (healthy, waiting its turn — never
// cancelled, at any concurrency) from a hung RUNNING build (stopped emitting compile
// output — cancelled). Owning the queue replaces reliance on Build.bat's opaque
// host-wide -WaitMutex, whose queue wait is silent and thus indistinguishable from a
// hang.
//
// This is the pure state machine: Submit/Finish/Cancel return the next build the
// caller should start, so the daemon owns the actual Build.bat execution (with its
// stall timer + Job Object). ≤1 build per engine is enforced here.
type BuildQueue struct {
	mu      sync.Mutex
	state   map[string]BuildState // build id -> state
	engine  map[string]string     // build id -> engine
	running map[string]string     // engine -> the running build id ("" = free)
	queued  map[string][]string   // engine -> FIFO of queued build ids
}

// BuildState is a build's observable position.
type BuildState string

const (
	BuildQueued    BuildState = "queued"
	BuildRunning   BuildState = "running"
	BuildDone      BuildState = "done"
	BuildFailed    BuildState = "failed"
	BuildCancelled BuildState = "cancelled"
)

// NewBuildQueue creates an empty queue.
func NewBuildQueue() *BuildQueue {
	return &BuildQueue{
		state: map[string]BuildState{}, engine: map[string]string{},
		running: map[string]string{}, queued: map[string][]string{},
	}
}

// Submit enqueues a build on engine. Returns startNow=true iff the engine was free
// (the caller should start it immediately); otherwise it is Queued (known-healthy)
// until an earlier build on that engine finishes.
func (q *BuildQueue) Submit(id, engine string) (startNow bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.engine[id] = engine
	if q.running[engine] == "" {
		q.running[engine] = id
		q.state[id] = BuildRunning
		return true
	}
	q.queued[engine] = append(q.queued[engine], id)
	q.state[id] = BuildQueued
	return false
}

// Finish marks a RUNNING build done/failed and returns the next queued build id on
// its engine to start (or "" if none). The caller starts nextID.
func (q *BuildQueue) Finish(id string, ok bool) (nextID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ok {
		q.state[id] = BuildDone
	} else {
		q.state[id] = BuildFailed
	}
	return q.advanceLocked(id)
}

// Cancel marks a build cancelled. A queued build is simply dropped (never was
// running); a running build frees its engine and returns the next to start.
func (q *BuildQueue) Cancel(id string) (nextID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	eng := q.engine[id]
	if q.state[id] == BuildQueued {
		// remove from the queue without advancing (engine's runner is unaffected).
		q.queued[eng] = removeStr(q.queued[eng], id)
		q.state[id] = BuildCancelled
		return ""
	}
	q.state[id] = BuildCancelled
	return q.advanceLocked(id)
}

// advanceLocked frees the engine held by id (if it was running) and promotes the
// next queued build. Caller holds q.mu.
func (q *BuildQueue) advanceLocked(id string) string {
	eng := q.engine[id]
	if q.running[eng] != id {
		return "" // wasn't the running build; nothing to advance
	}
	q.running[eng] = ""
	if len(q.queued[eng]) == 0 {
		return ""
	}
	next := q.queued[eng][0]
	q.queued[eng] = q.queued[eng][1:]
	q.running[eng] = next
	q.state[next] = BuildRunning
	return next
}

// State returns a build's observable state ("" if unknown).
func (q *BuildQueue) State(id string) BuildState {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.state[id]
}

// IsRunning reports whether a build is actively running (the ONLY state the §3.1
// stall-timer watches — a queued build is never stall-cancelled).
func (q *BuildQueue) IsRunning(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.state[id] == BuildRunning
}

func removeStr(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
