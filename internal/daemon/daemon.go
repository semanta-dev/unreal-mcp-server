package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// Daemon is the assembled Model-A runtime (MULTI_PROJECT_SYSTEM.md §0/§3): one
// shared discovery + editor pool + router + liveness loop, plus a per-session Deps
// resolver and the project_attach/release/list tools. It ties the tested daemon
// core to real editors (via Spawner) and to the tool surface (via the context Deps
// resolution the tools opted into for MP3).
type Daemon struct {
	Disc            *uexec.Discovery
	Pool            *supervisor.Pool
	Router          *Router
	Runtime         *Runtime
	EngineDir       string
	spawner         supervisor.Spawner // *Spawner in prod; a fake in tests
	isAlive         func(int) bool     // lifecycle.IsAlive in prod; a fake in tests
	restartKillWait time.Duration      // how long to confirm the old editor died (0 => 30s)
	records         *supervisor.RecordStore
	logger          *slog.Logger

	// OnEditorReady, when set, runs once per editor instance after a session first
	// leases it (e.g. the cockpit launcher); its ctx ends when the instance leaves
	// the pool.
	OnEditorReady func(ctx context.Context, project string, b *bridge.Bridge)
	readyMu       sync.Mutex
	readyStarted  map[*bridge.Bridge]bool // editor bridges whose OnEditorReady is running

	jobsMu      sync.Mutex
	projectJobs map[string]*jobs.Registry // canonical project key -> that project's jobs

	sessMu    sync.Mutex
	closers   map[string]func() // sessionID -> close the live MCP session
	draining  map[string]drain  // session ID -> lease held past that session for a running project job
	drainPoll time.Duration     // 0 = default (see pollEvery)

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
	pool := supervisor.NewPool(nil)
	records := supervisor.NewRecordStore(intentDir, logger)
	sp := &supervisor.ProcessSpawner{
		Disc: disc, Base: cfg, EngineDir: engineDir, Records: records,
		BridgeMode: bridgeMode, Logger: logger,
	}
	router := NewRouter(pool, sp, newToken)
	dm := &Daemon{
		Disc: disc, Pool: pool, Router: router, EngineDir: engineDir, spawner: sp,
		isAlive: lifecycle.IsAlive, records: records,
		logger: logger, projectJobs: map[string]*jobs.Registry{}, lastSeen: map[string]time.Time{},
		closers: map[string]func(){}, draining: map[string]drain{},
	}
	dm.Runtime = NewRuntime(router, poolLiveness{}, 3*time.Second, 120*time.Second, dm.onLeaseLost)
	return dm, nil
}

// NewWithSpawner builds a Daemon over a caller-supplied Spawner with no shared
// discovery (custom deployments and end-to-end tests drive editors themselves).
func NewWithSpawner(engineDir string, sp supervisor.Spawner, logger *slog.Logger) *Daemon {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	pool := supervisor.NewPool(nil)
	router := NewRouter(pool, sp, newToken)
	dm := &Daemon{
		Pool: pool, Router: router, EngineDir: engineDir, spawner: sp, isAlive: lifecycle.IsAlive,
		logger: logger, projectJobs: map[string]*jobs.Registry{}, lastSeen: map[string]time.Time{},
		closers: map[string]func(){}, draining: map[string]drain{},
	}
	dm.Runtime = NewRuntime(router, poolLiveness{}, 3*time.Second, 120*time.Second, dm.onLeaseLost)
	return dm
}

// poolLiveness is supervisor.OSLiveness (aliased so this package doesn't re-export).
type poolLiveness = supervisor.OSLiveness

// Run starts the liveness loop; it returns when ctx is cancelled. Also closes the
// shared discovery on exit.
func (dm *Daemon) Run(ctx context.Context) {
	if dm.Disc != nil {
		defer dm.Disc.Close()
	}
	dm.Runtime.Run(ctx)
}

func (dm *Daemon) onLeaseLost(sessionID string) {
	dm.logger.Warn("lease lost (editor crashed); session must re-attach", "session", sessionID)
	// The holder's next tool call already resolves to ErrLeaseLost; nothing else to do
	// here without a server-push channel (future: notify the session).
}

// jobsForProject returns (creating if needed) a project's jobs registry. Jobs are
// owned by the PROJECT, not the session or lease, so a later session attaching the
// same project (after a disconnect or an editor restart) still sees them.
func (dm *Daemon) jobsForProject(key string) *jobs.Registry {
	dm.jobsMu.Lock()
	defer dm.jobsMu.Unlock()
	r, ok := dm.projectJobs[key]
	if !ok {
		r = jobs.NewRegistry()
		dm.projectJobs[key] = r
	}
	return r
}

// RestartLease runs the §3.1 controlled restart for a session's lease: transition to
// Restarting (lease PRESERVED — the holder gets RESTART_IN_PROGRESS, not LEASE_LOST),
// stop the old editor (plan.Stop gracefully, else kill) and confirm it is dead so it
// releases the module DLL, run plan.Build with NO editor up, relaunch the editor with
// the SAME token (reattach-tracked, identity-continuous), re-pin the lease and reopen
// plan.Map. A refused Stop aborts with the editor untouched and the lease Leased; a
// failed Build still relaunches (its error is returned after); only an editor that
// will not die or cannot be relaunched tears the lease down.
func (dm *Daemon) RestartLease(ctx context.Context, sessionID string, plan session.RestartPlan) error {
	info, err := dm.Router.BeginRestart(sessionID)
	if err != nil {
		return err
	}
	if plan.Stop != nil {
		if serr := plan.Stop(ctx); serr != nil {
			if aerr := dm.Router.AbortRestart(info.ID); aerr != nil {
				dm.Router.Teardown(info.ID)
			}
			return serr
		}
	}
	if info.OldEditor != nil {
		_ = info.OldEditor.Close()
	}
	_ = dm.spawner.Kill(info.OldPID)
	// CONFIRM the old editor is dead before rebuilding/relaunching: a live one would
	// hold the module DLL / Live Coding lock, or be duplicated by the same-token
	// relaunch. If it won't die, take the failure path — never relaunch.
	killWait := dm.restartKillWait
	if killWait <= 0 {
		killWait = 30 * time.Second
	}
	if info.OldPID > 0 && !dm.confirmDead(info.OldPID, killWait) {
		dm.Router.Teardown(info.ID)
		return fmt.Errorf("restart aborted: old editor pid %d did not exit before rebuild", info.OldPID)
	}
	var buildErr error
	if plan.Build != nil {
		buildErr = plan.Build(ctx)
	}
	newEd, newPID, newIdentity, serr := dm.spawner.Spawn(ctx, info.Project, info.Token)
	if serr != nil {
		dm.Router.Teardown(info.ID)
		return errors.Join(buildErr, fmt.Errorf("relaunch after restart failed: %w", serr))
	}
	if eerr := dm.Router.EndRestart(info.ID, newEd, newPID, newIdentity); eerr != nil {
		// The pool instance may already be gone (e.g. a concurrent project_release), so
		// Teardown can't kill newPID for us — force-kill it here so the freshly-spawned
		// editor can't leak.
		_ = newEd.Close()
		_ = dm.spawner.Kill(newPID)
		dm.Router.Teardown(info.ID)
		return errors.Join(buildErr, eerr)
	}
	if plan.Map != "" {
		if eh, ok := newEd.(*supervisor.EditorHandle); ok {
			if _, oerr := eh.Bridge().Call(ctx, "open_level", map[string]any{"level_path": plan.Map}); oerr != nil {
				dm.logger.Warn("reopening the map after a restart failed", "map", plan.Map, "err", oerr)
			}
		}
	}
	return buildErr
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

// PruneProjectJobs drops a project's jobs registry once no editor for the project
// remains in the pool and none of its jobs is running, so the map stays bounded over
// a long-lived daemon's attach/crash/respawn cycles.
func (dm *Daemon) PruneProjectJobs() {
	live := map[string]bool{}
	for _, inst := range dm.Pool.List() {
		live[session.ProjectKey(inst.Project)] = true
	}
	dm.jobsMu.Lock()
	for key, r := range dm.projectJobs {
		if !live[key] && !r.HasRunning() {
			delete(dm.projectJobs, key)
		}
	}
	dm.jobsMu.Unlock()
}

// DepsResolver returns the per-session Deps resolver for session.InstallMiddleware:
// it maps the request's MCP session to its editor lease's bridge/project/jobs. Returns
// (Deps{}, false) for an unattached session (its tool calls then get NO_PROJECT_ATTACHED
// via the nil bridge, prompting a project_attach).
func (dm *Daemon) DepsResolver() func(ctx context.Context, req mcp.Request) (session.Deps, bool) {
	return func(ctx context.Context, req mcp.Request) (session.Deps, bool) {
		sid := sessionID(req)
		if sid == "" {
			return session.Deps{}, false
		}
		dm.touch(sid) // mark activity so the idle-sweep doesn't reclaim a live session
		ed, project, err := dm.Router.Resolve(sid)
		if err != nil {
			return session.Deps{}, false
		}
		eh, ok := ed.(*supervisor.EditorHandle)
		if !ok {
			return session.Deps{}, false
		}
		return session.Deps{
			Bridge: eh.Bridge(), ProjectDir: project, EngineDir: dm.EngineDir,
			Jobs: dm.jobsForProject(session.ProjectKey(project)), Projects: dm,
			Restart: func(rctx context.Context, plan session.RestartPlan) error {
				return dm.RestartLease(rctx, sid, plan)
			},
		}, true
	}
}

func (dm *Daemon) touch(sid string) {
	dm.activityMu.Lock()
	dm.lastSeen[sid] = time.Now()
	dm.activityMu.Unlock()
}

// SweepIdleSessions ends any session with no tool call in the last ttl — the
// PRIMARY liveness backstop (the SDK's idle SessionTimeout is ref-counted and never
// fires while a half-open SSE stream is held). It closes the MCP session when the app
// registered a closer, so teardown runs the same path as a client DELETE (cancelling
// pending approvals, then EndSession's drain rule); otherwise it calls EndSession
// directly. Staleness is re-checked under the lock so a concurrent call rescues the
// session.
func (dm *Daemon) SweepIdleSessions(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	dm.activityMu.Lock()
	var candidates []string
	for sid, t := range dm.lastSeen {
		if !t.After(cutoff) {
			candidates = append(candidates, sid)
		}
	}
	dm.activityMu.Unlock()

	for _, sid := range candidates {
		dm.activityMu.Lock()
		t, present := dm.lastSeen[sid]
		stillStale := present && !t.After(cutoff)
		dm.activityMu.Unlock()
		if !stillStale {
			continue
		}
		dm.sessMu.Lock()
		closeFn := dm.closers[sid]
		dm.sessMu.Unlock()
		dm.logger.Info("ending idle session", "session", sid, "idle_ttl", ttl.String())
		if closeFn != nil {
			closeFn() // teardown → EndSession
		} else {
			dm.EndSession(sid)
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
