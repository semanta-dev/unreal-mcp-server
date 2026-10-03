package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/app"
	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// fakeSpawner brings up one fake editor (wire fake + op emulator) per Spawn.
type fakeSpawner struct {
	t       *testing.T
	mu      sync.Mutex
	pid     int
	editors map[string]*bridgetest.Emulator // project key -> its editor's emulator
}

func (f *fakeSpawner) Spawn(ctx context.Context, project, token string) (supervisor.Editor, int, string, error) {
	emu := bridgetest.New()
	bridgetest.NewWorld().Install(emu)
	ed, err := uexectest.Start(emu.Options())
	if err != nil {
		return nil, 0, "", err
	}
	cfg := testUexecConfig()
	disc, err := uexec.OpenUnicastDiscovery(context.Background(), cfg, ed.Addr(), nil)
	if err != nil {
		ed.Close()
		return nil, 0, "", err
	}
	sess := uexec.NewOnDiscovery(cfg, disc, nil)
	f.t.Cleanup(func() { sess.Close(); disc.Close(); ed.Close() })
	f.mu.Lock()
	f.pid++
	pid := f.pid
	f.editors[session.ProjectKey(project)] = emu
	f.mu.Unlock()
	return supervisor.NewEditorHandle(sess, bridge.New(sess, bridge.Options{})), pid, fmt.Sprintf("id-%d", pid), nil
}

func (f *fakeSpawner) Kill(int) error { return nil }

func (f *fakeSpawner) spawned() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pid
}

type daemonEnv struct {
	dm  *daemon.Daemon
	sp  *fakeSpawner
	srv *httptest.Server
}

func startDaemon(t *testing.T, idle time.Duration, catalog func(session.Deps) []*spec.Spec) *daemonEnv {
	t.Helper()
	sp := &fakeSpawner{t: t, editors: map[string]*bridgetest.Emulator{}}
	dm := daemon.NewWithSpawner("", sp, nil)
	h := app.Handler(app.Options{
		Deps:           session.Deps{Jobs: jobs.NewRegistry(), Projects: dm},
		Resolver:       dm.DepsResolver(),
		DaemonMode:     true,
		Catalog:        catalog,
		OnSessionStart: dm.RegisterSession,
		OnSessionEnd:   dm.EndSession,
	}, idle)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &daemonEnv{dm: dm, sp: sp, srv: srv}
}

func (e *daemonEnv) connect(t *testing.T, opts *mcp.ClientOptions) *mcp.ClientSession {
	cs, _ := e.connectKillable(t, opts)
	return cs
}

// killableRT lets a test make a client vanish: once dead, every request (including
// the DELETE a closing client would send) fails before reaching the server.
type killableRT struct {
	dead atomic.Bool
	base http.RoundTripper
}

func (k *killableRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if k.dead.Load() {
		return nil, errors.New("client vanished")
	}
	return k.base.RoundTrip(r)
}

func (e *daemonEnv) connectKillable(t *testing.T, opts *mcp.ClientOptions) (*mcp.ClientSession, *killableRT) {
	t.Helper()
	rt := &killableRT{base: http.DefaultTransport}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, opts).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: e.srv.URL, MaxRetries: -1,
			HTTPClient: &http.Client{Transport: rt}}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return cs, rt
}

// vanish makes the client disappear without a DELETE: its transport dies and the
// server drops every open connection (incl. the standalone SSE stream).
func (e *daemonEnv) vanish(rt *killableRT) {
	rt.dead.Store(true)
	e.srv.CloseClientConnections()
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	if res.IsError {
		out["_error"] = text(res)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func leasedBy(dm *daemon.Daemon, project string) string {
	for _, inst := range dm.Pool.List() {
		if session.ProjectKey(inst.Project) == session.ProjectKey(project) {
			return inst.LeasedBy
		}
	}
	return ""
}

func TestDaemonTwoSessionsTwoProjectsNoCrossTalk(t *testing.T) {
	e := startDaemon(t, time.Minute, nil)
	a, b := e.connect(t, nil), e.connect(t, nil)
	defer a.Close()
	defer b.Close()
	pa, pb := t.TempDir(), t.TempDir()
	if out := callTool(t, a, "project", map[string]any{"op": "attach", "project": pa}); out["attached"] != true {
		t.Fatalf("attach a: %v", out)
	}
	if out := callTool(t, b, "project", map[string]any{"op": "attach", "project": pb}); out["attached"] != true {
		t.Fatalf("attach b: %v", out)
	}
	callTool(t, a, "actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "OnlyInA"})
	if out := callTool(t, b, "actor_query", map[string]any{"op": "get", "actor": "OnlyInA"}); out["_error"] == nil {
		t.Fatalf("session b saw session a's actor: %v", out)
	}
	if out := callTool(t, a, "actor_query", map[string]any{"op": "get", "actor": "OnlyInA"}); out["_error"] != nil {
		t.Fatalf("session a lost its actor: %v", out)
	}
}

func TestDaemonVanishedClientIsTornDown(t *testing.T) {
	e := startDaemon(t, 300*time.Millisecond, nil)
	cs, rt := e.connectKillable(t, nil)
	proj := t.TempDir()
	callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	if leasedBy(e.dm, proj) == "" {
		t.Fatal("expected a lease")
	}
	start := time.Now()
	e.vanish(rt) // no DELETE ever reaches the server
	waitFor(t, "lease release after idle expiry", func() bool { return leasedBy(e.dm, proj) == "" })
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("released after %s — before the 300ms idle timeout, so something other than expiry ended it", d)
	}
}

func TestDaemonSweeperEndsSessionWithHeldStream(t *testing.T) {
	// SDK idle timeout effectively off; the sweeper is the backstop.
	e := startDaemon(t, time.Hour, nil)
	cs := e.connect(t, nil) // keeps its standalone SSE stream open
	defer cs.Close()
	proj := t.TempDir()
	callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	e.dm.SweepIdleSessions(0) // everything is idle relative to a zero TTL
	waitFor(t, "sweeper-driven release", func() bool { return leasedBy(e.dm, proj) == "" })
}

func TestDaemonVanishMidJobDrainsThenAdopts(t *testing.T) {
	e := startDaemon(t, 200*time.Millisecond, nil)
	cs, rt := e.connectKillable(t, nil)
	proj := t.TempDir()
	out := callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	instance := out["instance"]

	// A long project job (as build_compile would start) is running.
	release := make(chan struct{})
	reg := e.dm.ProjectJobs(proj)
	j := reg.Start(context.Background(), func(ctx context.Context, p func(string)) (any, error) { <-release; return "built", nil })

	e.vanish(rt) // vanish mid-job
	waitFor(t, "drain", func() bool { return e.dm.Draining(proj) })
	if leasedBy(e.dm, proj) == "" {
		t.Fatal("a draining lease must stay held")
	}

	// A new session for the same project (different spelling) adopts the lease.
	cs2 := e.connect(t, nil)
	defer cs2.Close()
	alt := proj + string(filepath.Separator)
	if runtime.GOOS == "windows" {
		alt = upper(proj)
	}
	out = callTool(t, cs2, "project", map[string]any{"op": "attach", "project": alt})
	if out["instance"] != instance {
		t.Fatalf("expected adoption of %v, got %v", instance, out)
	}
	if e.sp.spawned() != 1 {
		t.Fatalf("adoption must not spawn a second editor (spawned %d)", e.sp.spawned())
	}
	// The new session sees the project's job.
	if st := callTool(t, cs2, "job", map[string]any{"op": "status", "job_id": j.ID}); st["_error"] != nil {
		t.Fatalf("adopting session cannot see the project job: %v", st)
	}
	close(release)
}

func TestDaemonAttachAppliesProjectToolsets(t *testing.T) {
	extra := &spec.Spec{Name: "design_probe_tool", Toolset: spec.Design, Max: time.Second,
		Ops: []spec.OpSpec{{Tier: spec.ReadOnly}},
		Handler: func(context.Context, *spec.Call) (*spec.Result, error) {
			return &spec.Result{Data: map[string]any{"ok": true}}, nil
		}}
	catalog := func(d session.Deps) []*spec.Spec { return append(tools.Specs(d), extra) }
	e := startDaemon(t, time.Minute, catalog)

	changed := make(chan struct{}, 8)
	cs := e.connect(t, &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
		select {
		case changed <- struct{}{}:
		default:
		}
	}})
	defer cs.Close()

	// Before attach: the design tool is a known-but-disabled tool.
	if out := callTool(t, cs, "design_probe_tool", nil); out["_error"] == nil {
		t.Fatalf("design tool should be disabled before attach: %v", out)
	}
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, session.ProjectFileName), []byte(`{"toolsets":["design"]}`), 0o644)
	out := callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj})
	if out["attached"] != true {
		t.Fatalf("attach: %v", out)
	}
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("no tools/list_changed after attach enabled the project's toolsets")
	}
	if out := callTool(t, cs, "design_probe_tool", nil); out["ok"] != true {
		t.Fatalf("design tool should work after attach: %v", out)
	}
}

func TestDaemonSessionChurnDoesNotLeak(t *testing.T) {
	e := startDaemon(t, time.Minute, nil)
	warm := e.connect(t, nil) // warm up lazily-started SDK/HTTP goroutines
	warm.Close()
	time.Sleep(100 * time.Millisecond)
	base := runtime.NumGoroutine()
	for i := 0; i < 200; i++ {
		cs := e.connect(t, nil)
		if _, err := cs.ListTools(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		cs.Close()
	}
	waitFor(t, "goroutines back to baseline", func() bool { return runtime.NumGoroutine() <= base+10 })
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// Found live (P7): while a lease restarted, the resolver dropped the whole binding, so
// job op=wait could not find the restart's own job and editor calls claimed no project
// was attached. Now only the editor is unavailable (retryable EDITOR_BUSY).
func TestDaemonRestartKeepsProjectAndJobs(t *testing.T) {
	e := startDaemon(t, time.Minute, tools.Specs)
	cs := e.connect(t, nil)
	defer cs.Close()
	proj := t.TempDir()
	if out := callTool(t, cs, "project", map[string]any{"op": "attach", "project": proj}); out["attached"] != true {
		t.Fatalf("attach: %v", out)
	}
	var id string
	for _, inst := range e.dm.Pool.List() {
		if session.ProjectKey(inst.Project) == session.ProjectKey(proj) {
			id = inst.ID
		}
	}
	if err := e.dm.Pool.RestartBegin(id, leasedBy(e.dm, proj)); err != nil {
		t.Fatal(err)
	}
	out := callTool(t, cs, "editor", map[string]any{"op": "status"})
	if s, _ := out["_error"].(string); !strings.Contains(s, "EDITOR_BUSY") || !strings.Contains(s, "restarting") {
		t.Fatalf("an editor call mid-restart should be a retryable EDITOR_BUSY: %v", out)
	}
	if out := callTool(t, cs, "job", map[string]any{"op": "list"}); out["_error"] != nil {
		t.Fatalf("the project's jobs must stay reachable mid-restart: %v", out)
	}
}
