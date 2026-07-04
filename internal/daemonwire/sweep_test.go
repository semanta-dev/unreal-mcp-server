package daemonwire

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/editorpool"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
)

type fakeEd struct{}

func (fakeEd) Close() error { return nil }

type fakeSpawner struct {
	pid   int
	kills []int
}

func (s *fakeSpawner) Spawn(ctx context.Context, project, token string) (daemon.Editor, int, string, error) {
	s.pid++
	return fakeEd{}, s.pid, "id", nil
}
func (s *fakeSpawner) Kill(pid int) error { s.kills = append(s.kills, pid); return nil }

func newTestDaemon() *Daemon {
	pool := editorpool.New(nil)
	sp := &fakeSpawner{}
	router := daemon.NewRouter(pool, sp, func() string { return "tok" })
	return &Daemon{
		Pool: pool, Router: router, spawner: sp,
		isAlive:   func(int) bool { return false }, // fake editors are dead once killed
		logger:    slog.New(slog.DiscardHandler),
		leaseJobs: map[string]*jobs.Registry{}, lastSeen: map[string]time.Time{},
	}
}

func TestSweepRefusesRunningJobThenReleasesIdle(t *testing.T) {
	dm := newTestDaemon()
	if _, err := dm.Router.Attach(context.Background(), "sessA", "/A"); err != nil {
		t.Fatal(err)
	}
	id, _ := dm.Router.InstanceFor("sessA")

	// A running job on this lease + a stale last-seen: the sweep MUST refuse.
	reg := dm.jobsFor(id)
	started, block := make(chan struct{}), make(chan struct{})
	reg.Start(context.Background(), func(jctx context.Context, _ func(string)) (any, error) {
		close(started)
		<-block
		return nil, nil
	})
	<-started
	dm.lastSeen["sessA"] = time.Unix(0, 0) // very stale

	dm.SweepIdleSessions(time.Minute)
	if _, ok := dm.Router.InstanceFor("sessA"); !ok {
		t.Fatal("sweep must NOT reclaim a lease whose holder has a running job")
	}

	// Let the job finish, then a stale session with no running job IS reclaimed.
	close(block)
	for i := 0; i < 200 && reg.HasRunning(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	dm.lastSeen["sessA"] = time.Unix(0, 0)
	dm.SweepIdleSessions(time.Minute)
	if _, ok := dm.Router.InstanceFor("sessA"); ok {
		t.Fatal("stale session with no running job should be reclaimed")
	}
}

func TestSweepRescuesRetouchedSession(t *testing.T) {
	dm := newTestDaemon()
	dm.Router.Attach(context.Background(), "sessA", "/A")
	// Stale, but re-touched to "now" — the re-check under lock must rescue it.
	dm.lastSeen["sessA"] = time.Now()
	dm.SweepIdleSessions(time.Minute)
	if _, ok := dm.Router.InstanceFor("sessA"); !ok {
		t.Fatal("a session active within the TTL must not be reclaimed")
	}
}

func TestPruneLeaseJobs(t *testing.T) {
	dm := newTestDaemon()
	dm.Router.Attach(context.Background(), "sessA", "/A")
	id, _ := dm.Router.InstanceFor("sessA")
	dm.jobsFor(id) // create the per-lease registry
	dm.jobsFor("ed-gone")
	dm.PruneLeaseJobs()
	dm.jobsMu.Lock()
	_, live := dm.leaseJobs[id]
	_, dead := dm.leaseJobs["ed-gone"]
	dm.jobsMu.Unlock()
	if !live {
		t.Fatal("a live instance's jobs registry must be kept")
	}
	if dead {
		t.Fatal("a removed instance's jobs registry must be pruned")
	}
}
