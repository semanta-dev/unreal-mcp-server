package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
)

// Session lifecycle (plan §2.6). Every way a session can end — client DELETE, the
// SDK's idle SessionTimeout, the idle sweeper, an explicit project_release — funnels
// into EndSession, which owns the single lease rule: a lease whose PROJECT still has
// a running job is not released but DRAINS (held, so no other project can take the
// editor) until the job finishes; a new session attaching the same project adopts it.

var _ session.ProjectManager = (*Daemon)(nil)

// drain is a lease held past its session's end because a project job is running.
type drain struct {
	project  string // canonical project key
	instance string
}

// RegisterSession records how to close a live MCP session (the app supplies its
// ServerSession.Close) so the idle sweeper ends sessions through the same teardown
// path as a client DELETE.
func (dm *Daemon) RegisterSession(sid string, closeFn func()) {
	dm.sessMu.Lock()
	dm.closers[sid] = closeFn
	dm.sessMu.Unlock()
	dm.touch(sid)
}

// Attach implements session.ProjectManager.
func (dm *Daemon) Attach(ctx context.Context, sid, project string) (string, error) {
	if sid == "" {
		return "", fmt.Errorf("no MCP session on the request")
	}
	if project == "" {
		return "", fmt.Errorf("project (the project directory) is required")
	}
	key := session.ProjectKey(project)
	dm.sessMu.Lock()
	from, draining := "", false
	for oldSid, d := range dm.draining {
		if d.project == key {
			from, draining = oldSid, true
			delete(dm.draining, oldSid)
			break
		}
	}
	dm.sessMu.Unlock()
	if draining {
		if id, err := dm.Router.Transfer(from, sid); err == nil {
			dm.logger.Info("adopted draining lease", "project", key, "from", from, "to", sid, "instance", id)
			dm.touch(sid)
			return id, nil
		}
		// The drained lease vanished (crash/reap) — fall through to a normal attach.
	}
	id, err := dm.Router.Attach(ctx, sid, project)
	if err == nil {
		dm.touch(sid)
	}
	return id, err
}

// Release implements session.ProjectManager (explicit project_release).
func (dm *Daemon) Release(sid string) { dm.EndSession(sid) }

// List implements session.ProjectManager.
func (dm *Daemon) List(sid string) []session.ProjectInstance {
	me, _ := dm.Router.InstanceFor(sid)
	var out []session.ProjectInstance
	for _, inst := range dm.Pool.List() {
		out = append(out, session.ProjectInstance{
			Instance: inst.ID, Project: inst.Project, State: string(inst.State),
			Leased: inst.LeasedBy != "", Mine: inst.ID == me,
		})
	}
	return out
}

// EndSession ends a session's lease binding under the single drain rule.
func (dm *Daemon) EndSession(sid string) {
	dm.sessMu.Lock()
	delete(dm.closers, sid)
	dm.sessMu.Unlock()
	dm.activityMu.Lock()
	delete(dm.lastSeen, sid)
	dm.activityMu.Unlock()

	id, leased := dm.Router.InstanceFor(sid)
	if !leased {
		return
	}
	inst, ok := dm.Pool.Get(id)
	if !ok {
		dm.Router.Release(sid)
		return
	}
	key := session.ProjectKey(inst.Project)
	if dm.jobsForProject(key).HasRunning() {
		dm.sessMu.Lock()
		dm.draining[sid] = drain{project: key, instance: id}
		dm.sessMu.Unlock()
		dm.logger.Info("session ended with a running job; lease draining", "session", sid, "project", key)
		go dm.awaitDrain(sid, key)
		return
	}
	dm.Router.Release(sid)
}

// awaitDrain releases a draining lease as soon as its project's jobs finish (unless
// it was adopted first). TickDrains on the sweeper loop is the backstop.
func (dm *Daemon) awaitDrain(sid, key string) {
	for {
		time.Sleep(dm.pollEvery())
		dm.sessMu.Lock()
		_, still := dm.draining[sid]
		dm.sessMu.Unlock()
		if !still {
			return // adopted, or released by TickDrains
		}
		if !dm.jobsForProject(key).HasRunning() {
			dm.TickDrains()
			return
		}
	}
}

// pollEvery is how often a draining lease checks for its project's jobs to finish.
func (dm *Daemon) pollEvery() time.Duration {
	if dm.drainPoll > 0 {
		return dm.drainPoll
	}
	return 200 * time.Millisecond
}

// TickDrains releases draining leases whose project jobs have all finished.
func (dm *Daemon) TickDrains() {
	dm.sessMu.Lock()
	var done []string
	for sid, d := range dm.draining {
		if !dm.jobsForProject(d.project).HasRunning() {
			done = append(done, sid)
			delete(dm.draining, sid)
		}
	}
	dm.sessMu.Unlock()
	for _, sid := range done {
		dm.Router.Release(sid)
		dm.logger.Info("drained lease released", "session", sid)
	}
}

// Draining reports whether a project currently has a draining lease (tests/cockpit).
func (dm *Daemon) Draining(project string) bool {
	key := session.ProjectKey(project)
	dm.sessMu.Lock()
	defer dm.sessMu.Unlock()
	for _, d := range dm.draining {
		if d.project == key {
			return true
		}
	}
	return false
}

// ProjectJobs returns the project's jobs registry (the one its sessions' tools use).
func (dm *Daemon) ProjectJobs(project string) *jobs.Registry {
	return dm.jobsForProject(session.ProjectKey(project))
}
