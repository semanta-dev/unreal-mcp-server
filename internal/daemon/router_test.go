package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
)

type fakeEditor struct{ closed bool }

func (e *fakeEditor) Close() error { e.closed = true; return nil }

type fakeSpawner struct {
	mu       sync.Mutex
	nextPID  int
	spawns   int
	kills    []int
	failNext bool
	editors  map[int]*fakeEditor // pid -> editor (to assert Close)
}

func (s *fakeSpawner) Spawn(ctx context.Context, project, token string) (supervisor.Editor, int, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return nil, 0, "", errors.New("spawn failed")
	}
	s.spawns++
	s.nextPID++
	if s.editors == nil {
		s.editors = map[int]*fakeEditor{}
	}
	ed := &fakeEditor{}
	s.editors[s.nextPID] = ed
	return ed, s.nextPID, "idn-" + project, nil
}

func (s *fakeSpawner) closed(pid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ed := s.editors[pid]
	return ed != nil && ed.closed
}
func (s *fakeSpawner) Kill(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, pid)
	return nil
}

func newTestRouter() (*Router, *fakeSpawner) {
	sp := &fakeSpawner{}
	pool := supervisor.NewPool(func() time.Time { return time.Unix(0, 0) })
	return NewRouter(pool, sp, nil), sp
}

func TestAttachSpawnsAndBinds(t *testing.T) {
	r, sp := newTestRouter()
	id, err := r.Attach(context.Background(), "sessA", "/proj/A")
	if err != nil || id == "" {
		t.Fatalf("attach: id=%q err=%v", id, err)
	}
	if sp.spawns != 1 {
		t.Fatalf("expected 1 spawn, got %d", sp.spawns)
	}
	// Idempotent: same session re-attach -> same instance, no new spawn.
	id2, _ := r.Attach(context.Background(), "sessA", "/proj/A")
	if id2 != id || sp.spawns != 1 {
		t.Fatalf("re-attach not idempotent: id2=%q spawns=%d", id2, sp.spawns)
	}
}

func TestTwoSessionsSameProjectGetTwoEditors(t *testing.T) {
	r, sp := newTestRouter()
	a, _ := r.Attach(context.Background(), "sessA", "/proj/X")
	b, _ := r.Attach(context.Background(), "sessB", "/proj/X")
	if a == b {
		t.Fatal("two sessions on one project must get DIFFERENT editors (1:1)")
	}
	if sp.spawns != 2 {
		t.Fatalf("expected 2 spawns (1:1), got %d", sp.spawns)
	}
}

func TestWarmReuseNoRespawn(t *testing.T) {
	r, sp := newTestRouter()
	id, _ := r.Attach(context.Background(), "sessA", "/proj/Y")
	r.Release("sessA") // editor goes back to warm Idle
	// A new session for the same project reuses the warm editor (no new spawn).
	id2, _ := r.Attach(context.Background(), "sessB", "/proj/Y")
	if id2 != id {
		t.Fatalf("expected warm reuse of %s, got %s", id, id2)
	}
	if sp.spawns != 1 {
		t.Fatalf("warm reuse must not respawn, spawns=%d", sp.spawns)
	}
}

func TestResolveAndLeaseLost(t *testing.T) {
	r, _ := newTestRouter()
	// Unbound -> NO_PROJECT_ATTACHED.
	if _, _, err := r.Resolve("ghost"); err != ErrNoProjectAttached {
		t.Fatalf("expected NO_PROJECT_ATTACHED, got %v", err)
	}
	id, _ := r.Attach(context.Background(), "sessA", "/proj/Z")
	ed, proj, err := r.Resolve("sessA")
	if err != nil || ed == nil || proj != "/proj/Z" {
		t.Fatalf("resolve: ed=%v proj=%q err=%v", ed, proj, err)
	}
	// Simulate a crash: the pool marks it Unhealthy.
	inst, _ := r.Pool().Get(id)
	r.Pool().CrashDetected(id, inst.PID)
	if _, _, err := r.Resolve("sessA"); err != ErrLeaseLost {
		t.Fatalf("after crash expected LEASE_LOST, got %v", err)
	}
	// The binding is cleared, so a re-resolve is NO_PROJECT_ATTACHED.
	if _, _, err := r.Resolve("sessA"); err != ErrNoProjectAttached {
		t.Fatalf("post-LEASE_LOST should be NO_PROJECT_ATTACHED, got %v", err)
	}
}

func TestSpawnFailure(t *testing.T) {
	r, sp := newTestRouter()
	sp.failNext = true
	if _, err := r.Attach(context.Background(), "sessA", "/proj/A"); err == nil {
		t.Fatal("expected spawn failure error")
	}
	if _, _, err := r.Resolve("sessA"); err != ErrNoProjectAttached {
		t.Fatalf("failed attach must leave no binding, got %v", err)
	}
}

func TestReleaseKillsExpectedDead(t *testing.T) {
	r, sp := newTestRouter()
	id, _ := r.Attach(context.Background(), "sessA", "/proj/A")
	inst, _ := r.Pool().Get(id)
	pid := inst.PID
	// Holder session closes while its editor is mid-restart (expected-dead-but-alive).
	r.Pool().RestartBegin(id, "sessA")
	r.Release("sessA")
	if len(sp.kills) != 1 || sp.kills[0] != pid {
		t.Fatalf("Release of an expected-dead instance must kill-before-teardown, kills=%v", sp.kills)
	}
	if _, ok := r.Pool().Get(id); ok {
		t.Fatal("expected-dead instance released should be removed (no leak)")
	}
}

func TestReleaseKeepsHealthyWarm(t *testing.T) {
	r, sp := newTestRouter()
	id, _ := r.Attach(context.Background(), "sessA", "/proj/A")
	r.Release("sessA") // healthy -> keep warm, no kill
	if len(sp.kills) != 0 {
		t.Fatalf("healthy Release must not kill, kills=%v", sp.kills)
	}
	if inst, ok := r.Pool().Get(id); !ok || inst.State != supervisor.Idle {
		t.Fatalf("healthy released editor should be warm Idle: %+v", inst)
	}
}

func TestResolveRestartInProgress(t *testing.T) {
	r, _ := newTestRouter()
	id, _ := r.Attach(context.Background(), "sessA", "/proj/A")
	r.Pool().RestartBegin(id, "sessA")
	// A holder call mid-restart must soft-fail, NOT get the bridge (which would race
	// the watchdog), and the binding must be kept for the retry.
	if _, _, err := r.Resolve("sessA"); err != ErrRestartInProgress {
		t.Fatalf("Resolve during restart should be RESTART_IN_PROGRESS, got %v", err)
	}
	// After the restart completes, Resolve works again (binding intact).
	r.Pool().RepinSucceeded(id)
	if _, _, err := r.Resolve("sessA"); err != nil {
		t.Fatalf("Resolve after restart should succeed: %v", err)
	}
}

func TestControlledRestartPreservesLeaseAndRepins(t *testing.T) {
	r, _ := newTestRouter()
	id, _ := r.Attach(context.Background(), "sessA", "/A")
	inst, _ := r.Pool().Get(id)
	oldPID, oldEd := inst.PID, r.editors[id]

	info, err := r.BeginRestart("sessA")
	if err != nil || info.ID != id || info.OldPID != oldPID || info.OldEditor != oldEd {
		t.Fatalf("BeginRestart bad: info=%+v err=%v", info, err)
	}
	if info.Token == "" || info.Project != "/A" {
		t.Fatalf("BeginRestart must return token+project to relaunch: %+v", info)
	}
	// The lease is PRESERVED during the restart — a holder call soft-fails, no LEASE_LOST.
	if _, _, e := r.Resolve("sessA"); e != ErrRestartInProgress {
		t.Fatalf("during restart Resolve should be RESTART_IN_PROGRESS, got %v", e)
	}
	// Re-pin onto the new editor + pid.
	newEd := &fakeEditor{}
	if err := r.EndRestart(id, newEd, 999, "new-identity"); err != nil {
		t.Fatal(err)
	}
	ed, _, e := r.Resolve("sessA")
	if e != nil || ed != newEd {
		t.Fatalf("after restart, Resolve should return the NEW editor: ed=%v err=%v", ed, e)
	}
	ni, _ := r.Pool().Get(id)
	if ni.State != supervisor.Leased || ni.PID != 999 || ni.LeasedBy != "sessA" {
		t.Fatalf("lease must be re-pinned to the new pid, still held by sessA: %+v", ni)
	}
}

func TestTeardownKillsExpectedDead(t *testing.T) {
	r, sp := newTestRouter()
	id, _ := r.Attach(context.Background(), "sessA", "/proj/A")
	inst, _ := r.Pool().Get(id)
	pid := inst.PID
	// Put it in an expected-dead-but-maybe-alive state.
	r.Pool().RestartBegin(id, "sessA")
	r.Teardown(id)
	if len(sp.kills) != 1 || sp.kills[0] != pid {
		t.Fatalf("expected kill-before-teardown of pid %d, got %v", pid, sp.kills)
	}
	if _, ok := r.Pool().Get(id); ok {
		t.Fatal("torn-down instance should be removed")
	}
}
