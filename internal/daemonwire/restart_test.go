package daemonwire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/editorpool"
)

func TestRestartLeasePreservesLeaseAndRepins(t *testing.T) {
	dm := newTestDaemon()
	id, _ := dm.Router.Attach(context.Background(), "sessA", "/A")
	oldInst, _ := dm.Pool.Get(id)
	oldPID := oldInst.PID

	var buildRan bool
	err := dm.RestartLease(context.Background(), "sessA", func(context.Context) error {
		buildRan = true
		return nil
	})
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
	if inst.State != editorpool.Leased || inst.PID == oldPID || inst.LeasedBy != "sessA" {
		t.Fatalf("lease should be re-pinned to a new pid, still held by sessA: %+v (old pid %d)", inst, oldPID)
	}
	// The old editor's pid was killed (kill-before-rebuild so it releases the DLL).
	sp := dm.spawner.(*fakeSpawner)
	if len(sp.kills) != 1 || sp.kills[0] != oldPID {
		t.Fatalf("old editor pid %d must be killed before rebuild, kills=%v", oldPID, sp.kills)
	}
}

func TestRestartLeaseBuildFailureDropsLease(t *testing.T) {
	dm := newTestDaemon()
	dm.Router.Attach(context.Background(), "sessA", "/A")
	err := dm.RestartLease(context.Background(), "sessA", func(context.Context) error {
		return errors.New("Build.bat could not run")
	})
	if err == nil {
		t.Fatal("a build infra failure should surface as an error")
	}
	// The lease is torn down so the holder cleanly re-attaches (not stuck mid-restart).
	if _, _, e := dm.Router.Resolve("sessA"); e != daemon.ErrLeaseLost {
		t.Fatalf("after a failed restart the holder should get LEASE_LOST, got %v", e)
	}
}

func TestRestartLeaseUnattachedErrors(t *testing.T) {
	dm := newTestDaemon()
	if err := dm.RestartLease(context.Background(), "ghost", nil); err != daemon.ErrNoProjectAttached {
		t.Fatalf("restart with no lease should be NO_PROJECT_ATTACHED, got %v", err)
	}
}

func TestRestartLeaseAbortsIfOldEditorWontDie(t *testing.T) {
	dm := newTestDaemon()
	dm.isAlive = func(int) bool { return true } // old editor refuses to die
	dm.restartKillWait = 20 * time.Millisecond  // don't wait the full 30s
	dm.Router.Attach(context.Background(), "sessA", "/A")
	sp := dm.spawner.(*fakeSpawner)
	spawnsBefore := sp.pid

	err := dm.RestartLease(context.Background(), "sessA", func(context.Context) error {
		t.Fatal("buildStep must NOT run while the old editor is still alive (DLL lock)")
		return nil
	})
	if err == nil {
		t.Fatal("restart must abort when the old editor won't die")
	}
	if sp.pid != spawnsBefore {
		t.Fatal("must NOT relaunch a same-token duplicate while the old editor is alive")
	}
	if _, _, e := dm.Router.Resolve("sessA"); e != daemon.ErrLeaseLost {
		t.Fatalf("an aborted restart should drop the lease (LEASE_LOST), got %v", e)
	}
}
