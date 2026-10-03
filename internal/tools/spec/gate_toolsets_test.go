package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
)

// fakeGate decides each request after a scripted delay.
type fakeGate struct {
	after    time.Duration
	approve  bool
	reason   string
	mu       sync.Mutex
	severed  int
	requests int
}

func (g *fakeGate) Required() bool { return true }
func (g *fakeGate) Request(GateRequest) (<-chan Decision, func()) {
	g.mu.Lock()
	g.requests++
	g.mu.Unlock()
	ch := make(chan Decision, 1)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-time.After(g.after):
			ch <- Decision{Approved: g.approve, Reason: g.reason}
		case <-stop:
		}
	}()
	return ch, func() {
		once.Do(func() {
			close(stop)
			g.mu.Lock()
			g.severed++
			g.mu.Unlock()
		})
	}
}

type gateEnv struct {
	cs    *mcp.ClientSession
	reg   *jobs.Registry
	state *session.State
	runs  *int32
}

func newGateEnv(t *testing.T, g Gate, gateTimeout, syncWait time.Duration) *gateEnv {
	t.Helper()
	var runs int32
	var mu sync.Mutex
	s := &Spec{Name: "nuke", Timeout: time.Second, Max: time.Second, Ops: []OpSpec{{Tier: Destructive}},
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("op deadline must be armed when the op actually runs")
			}
			mu.Lock()
			runs++
			mu.Unlock()
			return &Result{Data: map[string]any{"done": true}, Summary: "done"}, nil
		}}
	reg := jobs.NewRegistry()
	st := session.NewState("sess-1")
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, m string, r mcp.Request) (mcp.Result, error) {
			return next(session.WithState(ctx, st), m, r)
		}
	})
	Register(srv, []*Spec{s}, Options{Gate: g, GateTimeout: gateTimeout, SyncApprovalWait: syncWait, Fallback: session.Deps{Jobs: reg}})
	ctx := context.Background()
	ct, stt := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, stt, nil)
	t.Cleanup(func() { ss.Close() })
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	t.Cleanup(func() { cs.Close() })
	return &gateEnv{cs: cs, reg: reg, state: st, runs: &runs}
}

func (e *gateEnv) call(t *testing.T) map[string]any {
	t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nuke"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	out["_isError"] = res.IsError
	return out
}

func TestGateApprovedWithinSyncWindowRunsOp(t *testing.T) {
	e := newGateEnv(t, &fakeGate{after: 20 * time.Millisecond, approve: true}, time.Second, 0)
	if out := e.call(t); out["done"] != true || *e.runs != 1 {
		t.Fatalf("approved op must run once: %v runs=%d", out, *e.runs)
	}
}

func TestGateDeniedIsPrecondition(t *testing.T) {
	e := newGateEnv(t, &fakeGate{after: 10 * time.Millisecond, approve: false, reason: "denied"}, time.Second, 0)
	out := e.call(t)
	er, _ := out["error"].(map[string]any)
	if out["_isError"] != true || er["code"] != string(envelope.Precondition) || *e.runs != 0 {
		t.Fatalf("denied op must not run: %v", out)
	}
}

func TestGatePendingTimesOutWithoutRunning(t *testing.T) {
	// The human never answers within the 400ms total; the sync window is 100ms.
	e := newGateEnv(t, &fakeGate{after: time.Hour, approve: true}, 400*time.Millisecond, 100*time.Millisecond)
	out := e.call(t)
	if out["state"] != "pending_approval" || out["executed"] != false {
		t.Fatalf("expected pending_approval, got %v", out)
	}
	j, _ := e.reg.Get(out["job_id"].(string))
	if snap := j.Wait(); snap.Status != jobs.Failed || !strings.Contains(snap.Err, "approval") || *e.runs != 0 {
		t.Fatalf("timed-out approval must fail the job without running: %+v runs=%d", snap, *e.runs)
	}
}

func TestGatePendingThenApprovedRunsAsJob(t *testing.T) {
	// Approval arrives at 300ms: after the 100ms sync window, before the 2s total.
	e := newGateEnv(t, &fakeGate{after: 300 * time.Millisecond, approve: true}, 2*time.Second, 100*time.Millisecond)
	out := e.call(t)
	if out["state"] != "pending_approval" {
		t.Fatalf("expected pending_approval, got %v", out)
	}
	j, _ := e.reg.Get(out["job_id"].(string))
	snap := j.Wait()
	if snap.Status != jobs.Succeeded || *e.runs != 1 {
		t.Fatalf("approved pending job must run the op once: %+v runs=%d", snap, *e.runs)
	}
}

func TestGatePendingThenDenied(t *testing.T) {
	e := newGateEnv(t, &fakeGate{after: 300 * time.Millisecond, approve: false, reason: "denied"}, 2*time.Second, 100*time.Millisecond)
	out := e.call(t)
	j, _ := e.reg.Get(out["job_id"].(string))
	if snap := j.Wait(); snap.Status != jobs.Failed || *e.runs != 0 {
		t.Fatalf("denied pending job must fail without running: %+v", snap)
	}
}

func TestGateTeardownCancelsPendingJobAndSevers(t *testing.T) {
	g := &fakeGate{after: time.Hour, approve: true}
	e := newGateEnv(t, g, 5*time.Second, 100*time.Millisecond)
	if out := e.call(t); out["state"] != "pending_approval" {
		t.Fatalf("expected pending_approval, got %v", out)
	}
	e.state.Teardown()
	time.Sleep(50 * time.Millisecond)
	g.mu.Lock()
	severed := g.severed
	g.mu.Unlock()
	if severed == 0 || *e.runs != 0 {
		t.Fatalf("teardown must sever the parked request and never run the op (severed=%d runs=%d)", severed, *e.runs)
	}
}

func TestAsyncWaitStreamsProgress(t *testing.T) {
	reg := jobs.NewRegistry()
	s := &Spec{Name: "build", Async: true, Ops: []OpSpec{{Tier: Mutating}},
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			j := reg.Start(context.Background(), func(ctx context.Context, progress func(string)) (any, error) {
				progress("compiling")
				time.Sleep(20 * time.Millisecond)
				progress("linking")
				return "ok", nil
			})
			return &Result{Job: j}, nil
		}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	Register(srv, []*Spec{s}, Options{})
	var mu sync.Mutex
	var msgs []string
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, st, nil)
	defer ss.Close()
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			msgs = append(msgs, r.Params.Message)
			mu.Unlock()
		}}).Connect(ctx, ct, nil)
	defer cs.Close()

	params := &mcp.CallToolParams{Name: "build", Arguments: map[string]any{"wait_s": 5}}
	params.SetProgressToken("tok-1")
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"state":"succeeded"`) {
		t.Fatalf("wait_s should return the finished job: %s", b)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("no progress notifications received")
	}
	// Without wait_s the job comes back immediately as running or done.
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "build"})
	b, _ = json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"job_id"`) {
		t.Fatalf("async op must return a job: %s", b)
	}
}

func TestToolsetsEnableDisableEmitListChanged(t *testing.T) {
	mk := func(name string, ts Toolset) *Spec {
		return &Spec{Name: name, Toolset: ts, Max: time.Second, Ops: []OpSpec{{Tier: ReadOnly}},
			Handler: func(context.Context, *Call) (*Result, error) { return nil, nil }}
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	tsm := NewToolsets(srv, []*Spec{mk("a", Core), mk("audit_x", Design)}, Options{})
	srv.AddReceivingMiddleware(envelope.SafetyNet(tsm.Disabled))

	changed := make(chan struct{}, 8)
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, st, nil)
	defer ss.Close()
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} }}).Connect(ctx, ct, nil)
	defer cs.Close()

	names := func() map[string]bool {
		res, _ := cs.ListTools(ctx, nil)
		m := map[string]bool{}
		for _, tl := range res.Tools {
			m[tl.Name] = true
		}
		return m
	}
	if n := names(); !n["a"] || n["audit_x"] {
		t.Fatalf("only core should be listed: %v", n)
	}
	// Disabled tool → PRECONDITION naming the toolset.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "audit_x"})
	if err != nil || !res.IsError {
		t.Fatalf("disabled tool should be an enveloped error: %v %+v", err, res)
	}
	if _, err := tsm.Enable(Design); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("no tools/list_changed after enable")
	}
	if n := names(); !n["audit_x"] {
		t.Fatalf("design tool not listed after enable: %v", n)
	}
	if _, err := tsm.Disable(Core); err == nil {
		t.Fatal("core must not be disableable")
	}
	if _, err := tsm.Disable(Design); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("no tools/list_changed after disable")
	}
	if _, err := tsm.Enable("nonsense"); err == nil {
		t.Fatal("unknown toolset must error")
	}
}

func TestGateApprovedRunSurvivesSessionTeardown(t *testing.T) {
	var runs int32
	started, finish := make(chan struct{}), make(chan struct{})
	s := &Spec{Name: "slow_nuke", Timeout: 5 * time.Second, Max: 5 * time.Second, Ops: []OpSpec{{Tier: Destructive}},
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			close(started)
			select {
			case <-finish:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			runs++
			return &Result{Data: map[string]any{"done": true}}, nil
		}}
	reg := jobs.NewRegistry()
	st := session.NewState("sess-x")
	g := &fakeGate{after: 200 * time.Millisecond, approve: true}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, m string, r mcp.Request) (mcp.Result, error) {
			return next(session.WithState(ctx, st), m, r)
		}
	})
	Register(srv, []*Spec{s}, Options{Gate: g, GateTimeout: 5 * time.Second, SyncApprovalWait: 50 * time.Millisecond,
		Fallback: session.Deps{Jobs: reg}})
	ctx := context.Background()
	ct, stt := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, stt, nil)
	defer ss.Close()
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	defer cs.Close()

	res, _ := cs.CallTool(ctx, &mcp.CallToolParams{Name: "slow_nuke"})
	b, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	j, ok := reg.Get(fmt.Sprint(out["job_id"]))
	if !ok {
		t.Fatalf("expected a pending job: %s", b)
	}
	<-started     // approved and running
	st.Teardown() // the session ends mid-run
	close(finish) // the op completes
	if snap := j.Wait(); snap.Status != jobs.Succeeded || runs != 1 {
		t.Fatalf("an approved, running op must not be cancelled by teardown: %+v runs=%d", snap, runs)
	}
}

func TestGateApprovedAsyncSendsNoLateProgress(t *testing.T) {
	inner := jobs.NewRegistry()
	s := &Spec{Name: "slow_build", Async: true, Ops: []OpSpec{{Tier: Exec}},
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			return &Result{Job: inner.Start(context.Background(), func(ctx context.Context, p func(string)) (any, error) {
				p("compiling")
				time.Sleep(30 * time.Millisecond)
				p("linking")
				return "built", nil
			})}, nil
		}}
	reg := jobs.NewRegistry()
	g := &fakeGate{after: 150 * time.Millisecond, approve: true}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	Register(srv, []*Spec{s}, Options{Gate: g, GateTimeout: 5 * time.Second, SyncApprovalWait: 50 * time.Millisecond,
		Fallback: session.Deps{Jobs: reg}})
	var mu sync.Mutex
	progress := 0
	ctx := context.Background()
	ct, stt := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, stt, nil)
	defer ss.Close()
	cs, _ := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *mcp.ProgressNotificationClientRequest) { mu.Lock(); progress++; mu.Unlock() },
	}).Connect(ctx, ct, nil)
	defer cs.Close()

	params := &mcp.CallToolParams{Name: "slow_build", Arguments: map[string]any{"wait_s": 5}}
	params.SetProgressToken("tok")
	res, _ := cs.CallTool(ctx, params)
	b, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out["state"] != "pending_approval" {
		t.Fatalf("expected pending_approval: %s", b)
	}
	j, _ := reg.Get(fmt.Sprint(out["job_id"]))
	snap := j.Wait()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if progress != 0 {
		t.Fatalf("%d progress notifications were sent on the original token after its response", progress)
	}
	view, _ := snap.Result.(map[string]any)
	if snap.Status != jobs.Succeeded || view["state"] != "succeeded" || view["result"] != "built" {
		t.Fatalf("the pending job must carry the async op's final result, got %+v", snap)
	}
}

func TestToolsetsApplyKeepsBaseAndIsAtomic(t *testing.T) {
	mk := func(name string, ts Toolset) *Spec {
		return &Spec{Name: name, Toolset: ts, Max: time.Second, Ops: []OpSpec{{Tier: ReadOnly}},
			Handler: func(context.Context, *Call) (*Result, error) { return nil, nil }}
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	tsm := NewToolsets(srv, []*Spec{mk("c", Core), mk("d", Daemon), mk("u", UI), mk("x", Design)}, Options{}, Daemon)
	if err := tsm.Apply([]Toolset{UI}); err != nil {
		t.Fatal(err)
	}
	got := tsm.Enabled()
	if len(got) != 3 || got[0] != Core || got[1] != Daemon || got[2] != UI {
		t.Fatalf("Apply must keep startup toolsets: %v", got)
	}
	if err := tsm.Apply([]Toolset{Design, "bogus"}); err == nil {
		t.Fatal("unknown toolset must be rejected")
	}
	if got := tsm.Enabled(); len(got) != 3 || got[2] != UI {
		t.Fatalf("a rejected Apply must change nothing: %v", got)
	}
}
