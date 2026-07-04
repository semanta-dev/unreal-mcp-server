package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/editorpool"
)

type dClock struct{ t time.Time }

func (c *dClock) now() time.Time      { return c.t }
func (c *dClock) adv(d time.Duration) { c.t = c.t.Add(d) }

type dLiveness struct {
	alive map[int]bool
	ident map[int]string
}

func newDLv() *dLiveness                     { return &dLiveness{alive: map[int]bool{}, ident: map[int]string{}} }
func (l *dLiveness) IsAlive(pid int) bool    { return l.alive[pid] }
func (l *dLiveness) Identity(pid int) string { return l.ident[pid] }
func (l *dLiveness) set(pid int, alive bool, id string) {
	l.alive[pid] = alive
	l.ident[pid] = id
}

func TestRuntimeTickReapsCrashKeepsAlive(t *testing.T) {
	c := &dClock{t: time.Unix(1000, 0)}
	pool := editorpool.New(c.now)
	sp := &fakeSpawner{}
	router := NewRouter(pool, sp, nil)

	idA, _ := router.Attach(context.Background(), "sessA", "/A")
	idB, _ := router.Attach(context.Background(), "sessB", "/B")
	ia, _ := pool.Get(idA)
	ib, _ := pool.Get(idB)

	lv := newDLv()
	lv.set(ia.PID, true, ia.Identity) // A alive + identity matches
	lv.set(ib.PID, false, "")         // B crashed

	var lost []string
	rt := NewRuntime(router, lv, time.Second, 3*time.Second, func(s string) { lost = append(lost, s) })

	c.adv(1 * time.Minute) // well past reapTTL; without a renew, both would look stale
	reaped := rt.Tick()

	// Exactly B is reaped as a crash; A was renewed (alive) so it survives.
	if len(reaped) != 1 || reaped[0].ID != idB || reaped[0].State != editorpool.Leased || reaped[0].LeasedBy != "sessB" {
		t.Fatalf("expected B crash-reaped, got %+v", reaped)
	}
	if len(lost) != 1 || lost[0] != "sessB" {
		t.Fatalf("LEASE_LOST should be delivered to sessB, got %v", lost)
	}
	// A survived: still resolvable.
	if _, _, err := router.Resolve("sessA"); err != nil {
		t.Fatalf("alive A should survive the reaper: %v", err)
	}
	// B's binding + bridge were reconciled away.
	if _, _, err := router.Resolve("sessB"); err != ErrNoProjectAttached {
		t.Fatalf("crashed B binding should be cleared, got %v", err)
	}
	// B's bridge was Closed on reconcile (no dangling bridge).
	if !sp.closed(ib.PID) {
		t.Fatal("B's bridge should have been Closed on reconcile")
	}
}
