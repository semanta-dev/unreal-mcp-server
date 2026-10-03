package daemon

import (
	"context"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEd struct{}

func (fakeEd) Close() error { return nil }

type wireFakeSpawner struct {
	mu     sync.Mutex // Spawn runs on the router's background goroutine
	pid    int
	kills  []int
	gate   chan struct{} // when set, Spawn blocks until it is closed (a slow cold start)
	fail   error         // when set, Spawn fails with it
	during func()        // runs inside Spawn (overlap checks)
}

func (s *wireFakeSpawner) Spawn(ctx context.Context, project, token string) (supervisor.Editor, int, string, error) {
	if s.gate != nil {
		<-s.gate
	}
	if s.during != nil {
		s.during()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, 0, "", s.fail
	}
	s.pid++
	return fakeEd{}, s.pid, "id", nil
}
func (s *wireFakeSpawner) Kill(pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, pid)
	return nil
}
func (s *wireFakeSpawner) spawned() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}
func (s *wireFakeSpawner) killed() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.kills...)
}

func newTestDaemon() *Daemon {
	pool := supervisor.NewPool(nil)
	sp := &wireFakeSpawner{}
	router := NewRouter(pool, sp, func() string { return "tok" })
	return &Daemon{
		Pool: pool, Router: router, spawner: sp,
		isAlive:     func(int) bool { return false }, // fake editors are dead once killed
		logger:      slog.New(slog.DiscardHandler),
		projectJobs: map[string]*jobs.Registry{}, lastSeen: map[string]time.Time{},
		closers: map[string]func(){}, draining: map[string]drain{},
	}
}

// runningJob starts a job on the project's registry that runs until release is closed.
func runningJob(t *testing.T, dm *Daemon, project string) (release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	started := make(chan struct{})
	dm.jobsForProject(session.ProjectKey(project)).Start(context.Background(), func(context.Context, func(string)) (any, error) {
		close(started)
		<-release
		return nil, nil
	})
	<-started
	return release
}

func waitNoJobs(dm *Daemon, project string) {
	r := dm.jobsForProject(session.ProjectKey(project))
	for i := 0; i < 200 && r.HasRunning(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSweepDrainsLeaseWithRunningJobThenReleases(t *testing.T) {
	dm := newTestDaemon()
	if _, err := dm.Attach(context.Background(), "sessA", "/A"); err != nil {
		t.Fatal(err)
	}
	release := runningJob(t, dm, "/A")
	dm.lastSeen["sessA"] = time.Unix(0, 0) // very stale

	dm.SweepIdleSessions(time.Minute)
	if !dm.Draining("/A") {
		t.Fatal("ending a session with a running project job must drain the lease")
	}
	if _, ok := dm.Router.InstanceFor("sessA"); !ok {
		t.Fatal("a draining lease must stay held")
	}
	// Another project cannot take the draining editor.
	if inst, err := dm.Pool.Lease("/A", "intruder"); err == nil {
		t.Fatalf("draining editor was leasable: %+v", inst)
	}
	close(release)
	waitNoJobs(dm, "/A")
	dm.TickDrains()
	if dm.Draining("/A") {
		t.Fatal("drain must end once the job finished")
	}
	if _, ok := dm.Router.InstanceFor("sessA"); ok {
		t.Fatal("drained lease must be released")
	}
}

func TestSameProjectAdoptsDrainingLease(t *testing.T) {
	dm := newTestDaemon()
	dir := t.TempDir()
	id, err := dm.Attach(context.Background(), "old", dir)
	if err != nil {
		t.Fatal(err)
	}
	release := runningJob(t, dm, dir)
	defer close(release)
	dm.EndSession("old")
	if !dm.Draining(dir) {
		t.Fatal("expected draining")
	}
	// The new session spells the path differently (case-only on Windows, trailing
	// separator elsewhere) — it must still adopt the same instance, not spawn.
	alt := dir + string(filepath.Separator)
	if runtime.GOOS == "windows" {
		alt = strings.ToUpper(dir)
	}
	got, err := dm.Attach(context.Background(), "new", alt)
	if err != nil || got != id {
		t.Fatalf("adoption failed: got %q err %v, want %q", got, err, id)
	}
	if dm.Draining(dir) {
		t.Fatal("adoption must clear the drain")
	}
	if _, ok := dm.Router.InstanceFor("old"); ok {
		t.Fatal("old session must no longer hold the lease")
	}
	if sp := dm.spawner.(*wireFakeSpawner); sp.pid != 1 {
		t.Fatalf("adoption must not spawn (spawned %d)", sp.pid)
	}
}

func TestEndSessionWithoutJobsReleasesWarm(t *testing.T) {
	dm := newTestDaemon()
	dm.Attach(context.Background(), "sessA", "/A")
	dm.EndSession("sessA")
	if _, ok := dm.Router.InstanceFor("sessA"); ok {
		t.Fatal("lease must be released")
	}
	if inst, err := dm.Pool.Lease("/A", "next"); err != nil {
		t.Fatalf("released editor should be warm for reuse: %v", err)
	} else if inst.State != supervisor.Leased {
		t.Fatalf("state = %s", inst.State)
	}
}

func TestSweepClosesRegisteredSession(t *testing.T) {
	dm := newTestDaemon()
	dm.Attach(context.Background(), "sessA", "/A")
	closed := false
	dm.RegisterSession("sessA", func() { closed = true; dm.EndSession("sessA") })
	dm.lastSeen["sessA"] = time.Unix(0, 0)
	dm.SweepIdleSessions(time.Minute)
	if !closed {
		t.Fatal("sweeper must close the MCP session so teardown runs the normal path")
	}
}

func TestSweepRescuesRetouchedSession(t *testing.T) {
	dm := newTestDaemon()
	dm.Router.Attach(context.Background(), "sessA", "/A")
	dm.lastSeen["sessA"] = time.Now()
	dm.SweepIdleSessions(time.Minute)
	if _, ok := dm.Router.InstanceFor("sessA"); !ok {
		t.Fatal("a session active within the TTL must not be reclaimed")
	}
}

func TestPruneProjectJobs(t *testing.T) {
	dm := newTestDaemon()
	dm.Attach(context.Background(), "sessA", "/A")
	dm.jobsForProject(session.ProjectKey("/A"))
	dm.jobsForProject(session.ProjectKey("/gone"))
	dm.PruneProjectJobs()
	dm.jobsMu.Lock()
	_, live := dm.projectJobs[session.ProjectKey("/A")]
	_, dead := dm.projectJobs[session.ProjectKey("/gone")]
	dm.jobsMu.Unlock()
	if !live || dead {
		t.Fatalf("live=%v dead=%v; want live kept, gone pruned", live, dead)
	}
}

func TestTwoDrainsOnOneProjectBothRelease(t *testing.T) {
	dm := newTestDaemon()
	dm.Attach(context.Background(), "s1", "/P")
	dm.Attach(context.Background(), "s2", "/P") // second editor for the same project
	release := runningJob(t, dm, "/P")
	dm.EndSession("s1")
	dm.EndSession("s2")
	close(release)
	waitNoJobs(dm, "/P")
	dm.TickDrains()
	for _, sid := range []string{"s1", "s2"} {
		if _, ok := dm.Router.InstanceFor(sid); ok {
			t.Fatalf("%s's drained lease was never released (overwritten drain entry)", sid)
		}
	}
}

func TestDrainReleasesPromptlyWhenJobFinishes(t *testing.T) {
	dm := newTestDaemon()
	dm.drainPoll = 10 * time.Millisecond
	dm.Attach(context.Background(), "s1", "/Q")
	release := runningJob(t, dm, "/Q")
	dm.EndSession("s1")
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := dm.Router.InstanceFor("s1"); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("drain was not released promptly after the job finished (no sweeper tick)")
}
