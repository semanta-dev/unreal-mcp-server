package daemonwire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/editorpool"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// Daemon is the assembled Model-A runtime (MULTI_PROJECT_SYSTEM.md §0/§3): one
// shared discovery + editor pool + router + liveness loop, plus a per-session Deps
// resolver and the project_attach/release/list tools. It ties the tested daemon
// core to real editors (via Spawner) and to the tool surface (via the context Deps
// resolution the tools opted into for MP3).
type Daemon struct {
	Disc            *uexec.Discovery
	Pool            *editorpool.Pool
	Router          *daemon.Router
	Runtime         *daemon.Runtime
	EngineDir       string
	spawner         daemon.Spawner // *Spawner in prod; a fake in tests
	isAlive         func(int) bool // lifecycle.IsAlive in prod; a fake in tests
	restartKillWait time.Duration  // how long to confirm the old editor died (0 => 30s)
	records         *recordStore
	logger          *slog.Logger

	jobsMu    sync.Mutex
	leaseJobs map[string]*jobs.Registry // instanceID -> per-lease jobs registry

	activityMu sync.Mutex
	lastSeen   map[string]time.Time // sessionID -> last tool call (idle-sweep reclaim)
}

// NewDaemon opens the shared discovery and builds the pool/router/runtime. cfg is the
// base uexec config (multicast group etc.); each spawned editor gets an ephemeral
// per-instance command port + its own project. intentDir persists write-ahead spawn
// intents for reattach.
func NewDaemon(ctx context.Context, cfg uexec.Config, engineDir, intentDir string, bridgeMode bridge.SnippetMode, logger *slog.Logger) (*Daemon, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	disc, err := uexec.OpenDiscovery(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	pool := editorpool.New(nil)
	records := newRecordStore(intentDir, logger)
	sp := &Spawner{
		Disc: disc, Base: cfg, EngineDir: engineDir, Records: records,
		BridgeMode: bridgeMode, Logger: logger,
	}
	router := daemon.NewRouter(pool, sp, newToken)
	dm := &Daemon{
		Disc: disc, Pool: pool, Router: router, EngineDir: engineDir, spawner: sp,
		isAlive: lifecycle.IsAlive, records: records,
		logger: logger, leaseJobs: map[string]*jobs.Registry{}, lastSeen: map[string]time.Time{},
	}
	dm.Runtime = daemon.NewRuntime(router, editorpoolLiveness{}, 3*time.Second, 120*time.Second, dm.onLeaseLost)
	return dm, nil
}

// editorpoolLiveness is daemon.OSLiveness (aliased so this package doesn't re-export).
type editorpoolLiveness = daemon.OSLiveness

// Run starts the liveness loop; it returns when ctx is cancelled. Also closes the
// shared discovery on exit.
func (dm *Daemon) Run(ctx context.Context) {
	defer dm.Disc.Close()
	dm.Runtime.Run(ctx)
}

func (dm *Daemon) onLeaseLost(sessionID string) {
	dm.logger.Warn("lease lost (editor crashed); session must re-attach", "session", sessionID)
	// The holder's next tool call already resolves to ErrLeaseLost; nothing else to do
	// here without a server-push channel (future: notify the session).
}

// jobsFor returns (creating if needed) the per-lease jobs registry for an instance,
// so build/lifecycle jobs are scoped per editor and don't leak across projects.
func (dm *Daemon) jobsFor(instanceID string) *jobs.Registry {
	dm.jobsMu.Lock()
	defer dm.jobsMu.Unlock()
	r, ok := dm.leaseJobs[instanceID]
	if !ok {
		r = jobs.NewRegistry()
		dm.leaseJobs[instanceID] = r
	}
	return r
}

// RestartLease runs the §3.1 controlled restart for a session's lease: transition to
// Restarting (lease PRESERVED — the holder gets RESTART_IN_PROGRESS, not LEASE_LOST),
// close+kill the old editor so it releases the module DLL before the build, run
// buildStep with NO editor up, relaunch the editor with the SAME token (so it's
// reattach-tracked and identity-continuous), and re-pin the lease onto it. buildStep
// may be nil (a plain editor restart). On any failure the lease is torn down so the
// holder cleanly re-attaches rather than being stuck mid-restart.
func (dm *Daemon) RestartLease(ctx context.Context, sessionID string, buildStep func(context.Context) error) error {
	info, err := dm.Router.BeginRestart(sessionID)
	if err != nil {
		return err
	}
	if info.OldEditor != nil {
		_ = info.OldEditor.Close()
	}
	_ = dm.spawner.Kill(info.OldPID)
	// §3.1 step 2: CONFIRM the old editor is dead before rebuilding/relaunching. Kill
	// is async (TerminateProcess), so a still-alive editor would (a) hold the module
	// DLL / Live Coding lock and fail Build.bat, or (b) be duplicated by the same-token
	// relaunch and leaked. If it won't die, take the failure path — never relaunch.
	killWait := dm.restartKillWait
	if killWait <= 0 {
		killWait = 30 * time.Second
	}
	if info.OldPID > 0 && !dm.confirmDead(info.OldPID, killWait) {
		dm.Router.Teardown(info.ID)
		return fmt.Errorf("restart aborted: old editor pid %d did not exit before rebuild", info.OldPID)
	}
	if buildStep != nil {
		if berr := buildStep(ctx); berr != nil {
			dm.Router.Teardown(info.ID) // build infra failed -> drop lease -> re-attach
			return berr
		}
	}
	newEd, newPID, newIdentity, serr := dm.spawner.Spawn(ctx, info.Project, info.Token)
	if serr != nil {
		dm.Router.Teardown(info.ID)
		return fmt.Errorf("relaunch after restart failed: %w", serr)
	}
	if eerr := dm.Router.EndRestart(info.ID, newEd, newPID, newIdentity); eerr != nil {
		// The pool instance may already be gone (e.g. a concurrent project_release), so
		// Teardown can't kill newPID for us — force-kill it here so the freshly-spawned
		// editor can't leak.
		_ = newEd.Close()
		_ = dm.spawner.Kill(newPID)
		dm.Router.Teardown(info.ID)
		return eerr
	}
	return nil
}

// confirmDead polls until the pid is confirmed dead or the wait elapses.
func (dm *Daemon) confirmDead(pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if !dm.isAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// PruneLeaseJobs drops the per-lease jobs registry for any instance no longer in the
// pool (torn down / reaped), so leaseJobs can't grow unboundedly over a long-lived
// daemon's attach/crash/respawn cycles.
func (dm *Daemon) PruneLeaseJobs() {
	live := map[string]bool{}
	for _, inst := range dm.Pool.List() {
		live[inst.ID] = true
	}
	dm.jobsMu.Lock()
	for id := range dm.leaseJobs {
		if !live[id] {
			delete(dm.leaseJobs, id)
		}
	}
	dm.jobsMu.Unlock()
}

// DepsResolver returns the per-session Deps resolver for tools.InstallDepsMiddleware:
// it maps the request's MCP session to its editor lease's bridge/project/jobs. Returns
// (Deps{}, false) for an unattached session (its tool calls then get NO_PROJECT_ATTACHED
// via the nil bridge, prompting a project_attach).
func (dm *Daemon) DepsResolver() func(ctx context.Context, req mcp.Request) (tools.Deps, bool) {
	return func(ctx context.Context, req mcp.Request) (tools.Deps, bool) {
		sid := sessionID(req)
		if sid == "" {
			return tools.Deps{}, false
		}
		dm.touch(sid) // mark activity so the idle-sweep doesn't reclaim a live session
		ed, project, err := dm.Router.Resolve(sid)
		if err != nil {
			return tools.Deps{}, false
		}
		eh, ok := ed.(*EditorHandle)
		if !ok {
			return tools.Deps{}, false
		}
		id, _ := dm.Router.InstanceFor(sid)
		return tools.Deps{
			Bridge: eh.Bridge(), ProjectDir: project, EngineDir: dm.EngineDir, Jobs: dm.jobsFor(id),
			Restart: func(rctx context.Context, buildStep func(context.Context) error) error {
				return dm.RestartLease(rctx, sid, buildStep)
			},
		}, true
	}
}

func (dm *Daemon) touch(sid string) {
	dm.activityMu.Lock()
	dm.lastSeen[sid] = time.Now()
	dm.activityMu.Unlock()
}

// SweepIdleSessions releases the lease of any session with no tool call in the last
// ttl — a BACKSTOP for an agent that vanished without project_release (network drop,
// crashed client). Explicit project_release and the crash reaper are the primary
// reclaimers; ttl should be generous (a live agent may legitimately idle while
// thinking). The released editor is kept warm (Idle) for reuse unless mid-restart.
//
// Two guards make it safe: (1) it REFUSES to reclaim a lease whose holder has a
// running job (§0 — a long async build_compile produces no tool calls, so an
// idle-TTL alone would yank the editor mid-build); (2) it re-verifies staleness
// under the lock immediately before releasing, so a concurrent touch (the holder's
// call landing in the gap) rescues the session instead of losing its lease.
func (dm *Daemon) SweepIdleSessions(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	dm.activityMu.Lock()
	var candidates []string
	for sid, t := range dm.lastSeen {
		if t.Before(cutoff) {
			candidates = append(candidates, sid)
		}
	}
	dm.activityMu.Unlock() // do NOT pre-delete: a concurrent touch must be able to rescue

	for _, sid := range candidates {
		id, leased := dm.Router.InstanceFor(sid)
		if !leased {
			dm.forgetIfStale(sid, cutoff) // no lease; just drop the stale activity entry
			continue
		}
		// Never reclaim while the holder has async work in flight.
		if dm.jobsFor(id).HasRunning() {
			continue
		}
		// Atomically re-check staleness AND release under the same lock, so a
		// concurrent touch (the holder's call landing in the gap) either wins the
		// lock first — rescuing the session — or blocks until after the release
		// (its next call then re-attaches). This fully closes the touch/sweep TOCTOU.
		// Deadlock-safe: Router.Release takes only the pool lock, and nothing under
		// the pool lock re-enters activityMu.
		dm.activityMu.Lock()
		t, present := dm.lastSeen[sid]
		stillStale := present && t.Before(cutoff)
		if stillStale {
			delete(dm.lastSeen, sid)
			dm.Router.Release(sid)
		}
		dm.activityMu.Unlock()
		if stillStale {
			dm.logger.Info("reclaimed idle session lease", "session", sid, "idle_ttl", ttl.String())
		}
	}
}

// forgetIfStale drops a session's activity entry iff it is still stale (re-checked
// under the lock so a concurrent touch is respected).
func (dm *Daemon) forgetIfStale(sid string, cutoff time.Time) {
	dm.activityMu.Lock()
	if t, present := dm.lastSeen[sid]; present && t.Before(cutoff) {
		delete(dm.lastSeen, sid)
	}
	dm.activityMu.Unlock()
}

func sessionID(req mcp.Request) string {
	if ss, ok := req.GetSession().(*mcp.ServerSession); ok {
		return ss.ID()
	}
	return ""
}

// newToken generates a fresh, unguessable -MCPInstanceToken (§8: a spawned editor is
// daemon-owned iff it bears a token THIS daemon minted; the token authorizes kills).
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "mcp-" + hex.EncodeToString(b[:])
}
