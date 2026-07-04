package cockpit

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeServer is a minimal editor-side of the cockpit protocol over one net.Pipe conn.
// It reads hello, sends welcome, then dispatches client frames: rpc -> a test-supplied
// handler (default: echo an ok result), control -> recorded. Tests push events/gates.
type fakeServer struct {
	conn  net.Conn
	epoch string

	mu       sync.Mutex
	controls []*Frame
	hello    *Frame

	rpc func(*Frame) *Frame // op handler; returns the rpc_result frame (nil = no reply)
}

func (s *fakeServer) run() {
	hello, err := ReadFrame(s.conn)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.hello = hello
	s.mu.Unlock()
	_ = WriteFrame(s.conn, &Frame{Type: FrameWelcome, SessionEpoch: s.epoch, ManifestDigest: "m1", ProtocolVersion: ProtocolVersion, EngineVersion: "5.7", Project: "/p"})
	for {
		f, err := ReadFrame(s.conn)
		if err != nil {
			return
		}
		switch f.Type {
		case FrameRPC:
			var res *Frame
			if s.rpc != nil {
				res = s.rpc(f)
			} else {
				res = &Frame{Type: FrameRPCResult, OpID: f.OpID, OK: boolPtr(true), Result: json.RawMessage(`{"echo":"` + f.Op + `"}`)}
			}
			if res != nil {
				_ = WriteFrame(s.conn, res)
			}
		case FrameControl:
			s.mu.Lock()
			s.controls = append(s.controls, f)
			s.mu.Unlock()
		}
	}
}

func (s *fakeServer) push(f *Frame) { _ = WriteFrame(s.conn, f) }
func (s *fakeServer) recvControls() []*Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Frame(nil), s.controls...)
}

func newTestPair(t *testing.T, h Handlers, srv *fakeServer) *Client {
	t.Helper()
	cconn, sconn := net.Pipe()
	srv.conn = sconn
	go srv.run()
	c, err := newClient(cconn, DialConfig{Token: "tok", Timeout: 2 * time.Second}, h)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestHandshakeCapturesEpoch(t *testing.T) {
	c := newTestPair(t, Handlers{}, &fakeServer{epoch: "ep-123"})
	if c.Epoch() != "ep-123" {
		t.Fatalf("epoch = %q, want ep-123", c.Epoch())
	}
	if c.ManifestDigest() != "m1" {
		t.Fatalf("manifest digest = %q", c.ManifestDigest())
	}
}

func TestHandshakeRejectsProtocolMismatch(t *testing.T) {
	cconn, sconn := net.Pipe()
	go func() {
		ReadFrame(sconn)
		WriteFrame(sconn, &Frame{Type: FrameWelcome, ProtocolVersion: ProtocolVersion + 99})
	}()
	if _, err := newClient(cconn, DialConfig{Token: "t", Timeout: time.Second}, Handlers{}); err == nil {
		t.Fatal("expected protocol-mismatch handshake error")
	}
}

func TestRPCRoundTrip(t *testing.T) {
	c := newTestPair(t, Handlers{}, &fakeServer{epoch: "e"})
	res, err := c.RPC(context.Background(), "list_actors", json.RawMessage(`{"limit":5}`), "survey", "task1")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK == nil || !*res.OK || string(res.Result) != `{"echo":"list_actors"}` {
		t.Fatalf("bad rpc result: %+v", res)
	}
}

func TestRPCMatchesByOpID(t *testing.T) {
	// Server replies out of order; client must still route each result to its caller.
	srv := &fakeServer{epoch: "e", rpc: func(f *Frame) *Frame {
		return &Frame{Type: FrameRPCResult, OpID: f.OpID, OK: boolPtr(true), Result: json.RawMessage(`"` + f.Op + `"`)}
	}}
	c := newTestPair(t, Handlers{}, srv)
	var wg sync.WaitGroup
	for _, op := range []string{"a", "b", "c"} {
		op := op
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.RPC(context.Background(), op, nil, "", "")
			if err != nil || string(res.Result) != `"`+op+`"` {
				t.Errorf("op %s got %+v err %v", op, res, err)
			}
		}()
	}
	wg.Wait()
}

func TestRPCContextCancelSendsCancel(t *testing.T) {
	// Server never replies to rpc, so the ctx cancel path fires.
	srv := &fakeServer{epoch: "e", rpc: func(*Frame) *Frame { return nil }}
	c := newTestPair(t, Handlers{}, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := c.RPC(ctx, "slow", nil, "", ""); err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	// A cancel control for the op should have been sent.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, ctl := range srv.recvControls() {
			if ctl.Control == CtrlCancel {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no cancel control observed after ctx cancel")
}

func TestEventDeliveryAndSeqTracking(t *testing.T) {
	var mu sync.Mutex
	var events []*Frame
	srv := &fakeServer{epoch: "e"}
	c := newTestPair(t, Handlers{OnEvent: func(f *Frame) { mu.Lock(); events = append(events, f); mu.Unlock() }}, srv)
	srv.push(&Frame{Type: FrameEvent, Seq: 3, EventType: "pie.begin"})
	srv.push(&Frame{Type: FrameEvent, Seq: 7, EventType: "select.changed", Dropped: 2})
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 2 })
	if c.LastSeq() != 7 {
		t.Fatalf("LastSeq = %d, want 7", c.LastSeq())
	}
	mu.Lock()
	if events[1].Dropped != 2 {
		t.Fatalf("dropped not delivered: %d", events[1].Dropped)
	}
	mu.Unlock()
}

func TestGateDelivery(t *testing.T) {
	got := make(chan *Frame, 1)
	srv := &fakeServer{epoch: "e"}
	c := newTestPair(t, Handlers{OnGate: func(f *Frame) { got <- f }}, srv)
	srv.push(&Frame{Type: FrameGate, GateID: "g1", OpID: "op1", Classification: "destructive", ArgsHash: "h", Diff: json.RawMessage(`{"Mass":{"before":100,"after":0}}`)})
	select {
	case g := <-got:
		if g.GateID != "g1" || g.Classification != "destructive" {
			t.Fatalf("bad gate: %+v", g)
		}
		_ = c.Approve("g1")
		waitFor(t, func() bool {
			for _, ctl := range srv.recvControls() {
				if ctl.Control == CtrlApprove && ctl.GateID == "g1" {
					return true
				}
			}
			return false
		})
	case <-time.After(time.Second):
		t.Fatal("gate not delivered")
	}
}

func TestCloseFailsPendingRPC(t *testing.T) {
	srv := &fakeServer{epoch: "e", rpc: func(*Frame) *Frame { return nil }}
	c := newTestPair(t, Handlers{}, srv)
	errc := make(chan error, 1)
	go func() { _, err := c.RPC(context.Background(), "hang", nil, "", ""); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected error after close")
		}
	case <-time.After(time.Second):
		t.Fatal("RPC did not unblock on close")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
