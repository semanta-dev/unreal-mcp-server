package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
)

func shortCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func waitPool(t *testing.T, dm *Daemon, what string, ok func([]supervisor.Instance) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok(dm.Pool.List()) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v", what, dm.Pool.List())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Found live (P7): a cold start longer than the attach call's deadline was cancelled
// and its editor killed. Now the call returns ErrEditorStarting and attaching again
// resumes the same start.
func TestAttachOutlivesTheCallDeadline(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	sp.gate = make(chan struct{})
	if _, err := dm.Router.Attach(shortCtx(t), "s", "/A"); !errors.Is(err, session.ErrEditorStarting) {
		t.Fatalf("want ErrEditorStarting, got %v", err)
	}
	close(sp.gate) // the editor finishes starting
	id, err := dm.Router.Attach(context.Background(), "s", "/A")
	if err != nil || id == "" {
		t.Fatalf("re-attach should bind the same start: id=%q err=%v", id, err)
	}
	if sp.spawned() != 1 || len(sp.killed()) != 0 {
		t.Fatalf("exactly one editor, never killed: spawned=%d kills=%v", sp.spawned(), sp.killed())
	}
}

// Gate finding (P7): EndSession returned early for a session with no binding, so a
// start whose session ended was later leased to the dead session forever.
func TestEndedSessionsColdStartStaysWarm(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	sp.gate = make(chan struct{})
	_, _ = dm.Router.Attach(shortCtx(t), "gone", "/A")
	dm.EndSession("gone") // the client disconnected while its editor was still loading
	close(sp.gate)
	waitPool(t, dm, "a warm unleased editor", func(l []supervisor.Instance) bool {
		return len(l) == 1 && l[0].State == supervisor.Idle && l[0].LeasedBy == ""
	})
	if id, err := dm.Router.Attach(context.Background(), "next", "/A"); err != nil || id == "" || sp.spawned() != 1 {
		t.Fatalf("the next session should lease the warm editor: id=%q err=%v spawned=%d", id, err, sp.spawned())
	}
}

// An ended session's in-flight start is adopted by the next session of the project
// instead of starting a second editor.
func TestNewSessionAdoptsAnAbandonedColdStart(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	sp.gate = make(chan struct{})
	_, _ = dm.Router.Attach(shortCtx(t), "gone", "/A")
	dm.EndSession("gone")
	if _, err := dm.Router.Attach(shortCtx(t), "next", "/A"); !errors.Is(err, session.ErrEditorStarting) {
		t.Fatalf("the adopter waits on the same start: %v", err)
	}
	close(sp.gate)
	id, err := dm.Router.Attach(context.Background(), "next", "/A")
	if err != nil || id == "" || sp.spawned() != 1 {
		t.Fatalf("adopted start should lease to the new session: id=%q err=%v spawned=%d", id, err, sp.spawned())
	}
	if inst, _ := dm.Pool.Get(id); inst.LeasedBy != "next" {
		t.Fatalf("lease holder = %q, want next", inst.LeasedBy)
	}
}

// Gate finding (P7): a start that failed while nobody waited kept the session stuck
// on "still starting" for every other project.
func TestFinishedStartDoesNotBlockAnotherProject(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	sp.gate = make(chan struct{})
	sp.fail = errors.New("boom")
	_, _ = dm.Router.Attach(shortCtx(t), "s", "/A")
	close(sp.gate)
	time.Sleep(20 * time.Millisecond) // the start fails with nobody waiting
	sp.mu.Lock()
	sp.fail = nil
	sp.mu.Unlock()
	if id, err := dm.Router.Attach(context.Background(), "s", "/B"); err != nil || id == "" {
		t.Fatalf("attaching another project must not be blocked by a finished start: id=%q err=%v", id, err)
	}
}

// Gate finding (P7): concurrent cold starts of one project are serialized, so the
// second spawner sees the first editor's node before it launches.
func TestColdStartsOfOneProjectAreSerialized(t *testing.T) {
	dm := newTestDaemon()
	sp := dm.spawner.(*wireFakeSpawner)
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	sp.during = func() {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for _, sid := range []string{"a", "b"} {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if _, err := dm.Router.Attach(context.Background(), sid, "/A"); err != nil {
				t.Errorf("%s: %v", sid, err)
			}
		}(sid)
	}
	wg.Wait()
	if maxInFlight != 1 || sp.spawned() != 2 {
		t.Fatalf("two editors, launched one at a time: max in flight=%d spawned=%d", maxInFlight, sp.spawned())
	}
}
