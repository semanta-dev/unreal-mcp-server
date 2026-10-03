package daemon

import (
	"context"
	"errors"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"testing"
	"time"
)

func TestRestartLeasePreservesLeaseAndRepins(t *testing.T) {
	dm := newTestDaemon()
	id, _ := dm.Router.Attach(context.Background(), "sessA", "/A")
	oldInst, _ := dm.Pool.Get(id)
	oldPID := oldInst.PID

	var buildRan bool
	err := dm.RestartLease(context.Background(), "sessA", session.RestartPlan{Build: func(context.Context) error {
		buildRan = true
		return nil
	}})
	if err != nil {
		t.Fatalf("RestartLease: %v", err)
	}
	if !buildRan {
		t.Fatal("buildStep must run during the restart")
	}
	// Same session, same instance, still Leased — but re-pinned to a NEW pid.
	boundID, ok := dm.Router.InstanceFor("sessA")
	if !ok || boundID != id {
		t.Fatal("the session must still hold the SAME lease after the controlled restart")
	}
	inst, _ := dm.Pool.Get(id)
	if inst.State != supervisor.Leased || inst.PID == oldPID || inst.LeasedBy != "sessA" {
		t.Fatalf("lease should be re-pinned to a new pid, still held by sessA: %+v (old pid %d)", inst, oldPID)
	}
	// The old editor's pid was killed (kill-before-rebuild so it releases the DLL).
	sp := dm.spawner.(*wireFakeSpawner)
	if len(sp.kills) != 1 || sp.kills[0] != oldPID {
		t.Fatalf("old editor pid %d must be killed before rebuild, kills=%v", oldPID, sp.kills)
	}
}

func TestRestartLeaseBuildFailureStillRelaunches(t *testing.T) {
	dm := newTestDaemon()
	id, _ := dm.Router.Attach(context.Background(), "sessA", "/A")
	err := dm.RestartLease(context.Background(), "sessA", session.RestartPlan{Build: func(context.Context) error {
		return errors.New("Build.bat could not run")
	}})
	if err == nil {
		t.Fatal("a build failure must surface as an error")
	}
	// The editor comes back and the holder keeps the lease (a failed build or revert
	// step must not leave the session without an editor).
	if inst, _ := dm.Pool.Get(id); inst.State != supervisor.Leased || inst.LeasedBy != "sessA" {
		t.Fatalf("after a failed build step the lease must be re-pinned: %+v", inst)
	}
}

func TestRestartLeaseRefusedStopAbortsWithoutKilling(t *testing.T) {
	dm := newTestDaemon()
	id, _ := dm.Router.Attach(context.Background(), "sessA", "/A")
	before, _ := dm.Pool.Get(id)
	sp := dm.spawner.(*wireFakeSpawner)
	err := dm.RestartLease(context.Background(), "sessA", session.RestartPlan{
		Stop:  func(context.Context) error { return errors.New("unsaved packages appeared") },
		Build: func(context.Context) error { t.Fatal("build must not run after a refused stop"); return nil },
	})
	if err == nil {
		t.Fatal("a refused stop must be returned")
	}
	inst, _ := dm.Pool.Get(id)
	if len(sp.kills) != 0 || inst.State != supervisor.Leased || inst.PID != before.PID {
		t.Fatalf("a refused stop must leave the editor running and the lease Leased: %+v kills=%v", inst, sp.kills)
	}
}

func TestRestartLeaseUnattachedErrors(t *testing.T) {
	dm := newTestDaemon()
	if err := dm.RestartLease(context.Background(), "ghost", session.RestartPlan{}); err != ErrNoProjectAttached {
		t.Fatalf("restart with no lease should be NO_PROJECT_ATTACHED, got %v", err)
	}
}

func TestRestartLeaseAbortsIfOldEditorWontDie(t *testing.T) {
	dm := newTestDaemon()
	dm.isAlive = func(int) bool { return true } // old editor refuses to die
	dm.restartKillWait = 20 * time.Millisecond  // don't wait the full 30s
	dm.Router.Attach(context.Background(), "sessA", "/A")
	sp := dm.spawner.(*wireFakeSpawner)
	spawnsBefore := sp.pid

	err := dm.RestartLease(context.Background(), "sessA", session.RestartPlan{Build: func(context.Context) error {
		t.Fatal("buildStep must NOT run while the old editor is still alive (DLL lock)")
		return nil
	}})
	if err == nil {
		t.Fatal("restart must abort when the old editor won't die")
	}
	if sp.pid != spawnsBefore {
		t.Fatal("must NOT relaunch a same-token duplicate while the old editor is alive")
	}
	if _, _, e := dm.Router.Resolve("sessA"); e != ErrLeaseLost {
		t.Fatalf("an aborted restart should drop the lease (LEASE_LOST), got %v", e)
	}
}
