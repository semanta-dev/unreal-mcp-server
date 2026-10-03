package uexec

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// countingEditor executes commands, counting each by its text.
type countingEditor struct {
	mu   sync.Mutex
	runs map[string]int
}

func (c *countingEditor) handler(req uexectest.CommandRequest) uexectest.CommandResponse {
	cmd := trimGuard(req.Command)
	c.mu.Lock()
	c.runs[cmd]++
	c.mu.Unlock()
	return uexectest.CommandResponse{Success: true, Result: cmd}
}

func (c *countingEditor) count(cmd string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[cmd]
}

// dropOnce drops the reply of the first command equal to target.
func dropOnce(target string) func(uexectest.CommandRequest) bool {
	var done atomic.Bool
	return func(req uexectest.CommandRequest) bool {
		return trimGuard(req.Command) == target && done.CompareAndSwap(false, true)
	}
}

func TestMutatingCommandRunsExactlyOnceOnLostReply(t *testing.T) {
	ce := &countingEditor{runs: map[string]int{}}
	e, _ := uexectest.Start(uexectest.Options{OnCommand: ce.handler, DropReplyIf: dropOnce("SPAWN")})
	defer e.Close()
	s := dialFake(t, e, testCfg())

	_, err := s.RunCommand(context.Background(), "SPAWN", ModeExecFile) // default RetryNone
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("want ErrOutcomeUnknown (wrapping ErrConnectionLost), got %v", err)
	}
	if n := ce.count("SPAWN"); n != 1 {
		t.Fatalf("the mutating command executed %d times, want exactly 1", n)
	}
	// The session recovers: the next command reconnects and succeeds.
	if res, err := s.RunCommand(context.Background(), "NEXT", ModeEval); err != nil || res.Result != "NEXT" {
		t.Fatalf("recovery failed: %v %+v", err, res)
	}
}

func TestIdempotentCommandIsResentOnLostReply(t *testing.T) {
	ce := &countingEditor{runs: map[string]int{}}
	e, _ := uexectest.Start(uexectest.Options{OnCommand: ce.handler, DropReplyIf: dropOnce("READ")})
	defer e.Close()
	s := dialFake(t, e, testCfg())

	ctx := WithRetryPolicy(context.Background(), RetryIdempotent)
	res, err := s.RunCommand(ctx, "READ", ModeEval)
	if err != nil || res.Result != "READ" {
		t.Fatalf("idempotent command should be re-sent transparently: %v %+v", err, res)
	}
	if n := ce.count("READ"); n != 2 {
		t.Fatalf("idempotent command ran %d times, want 2 (original + re-send)", n)
	}
}

// probePair returns a commandConn over a real loopback TCP connection and the
// editor-side end.
func probePair(t *testing.T) (*commandConn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	c := &commandConn{conn: client, logger: slog.New(slog.DiscardHandler)}
	c.br = newBufReader(client)
	c.dec = newDecoder(c.br)
	c.lastIO = time.Now().Add(-time.Second)
	return c, server
}

func TestProbeDetectsPeerClose(t *testing.T) {
	c, server := probePair(t)
	if !c.alive(false) {
		t.Fatal("an open idle channel must be alive")
	}
	server.Close()
	time.Sleep(20 * time.Millisecond) // let the FIN arrive
	if c.alive(false) {
		t.Fatal("probe missed the peer close")
	}
}

func TestProbeConsumesWhitespaceAndFlagsDesync(t *testing.T) {
	c, server := probePair(t)
	server.Write([]byte("\n \r\n"))
	time.Sleep(20 * time.Millisecond)
	if !c.alive(false) {
		t.Fatal("trailing whitespace is not a desync")
	}
	server.Write([]byte(`{"x":1}`))
	time.Sleep(20 * time.Millisecond)
	if c.alive(false) {
		t.Fatal("unsolicited data on an idle channel must be reported as desync")
	}
	// The probe never consumed the protocol bytes it peeked.
	var m map[string]int
	if err := c.dec.Decode(&m); err != nil || m["x"] != 1 {
		t.Fatalf("probe consumed protocol bytes: %v %v", m, err)
	}
}

func TestRecentChannelSkipsProbeOnlyForIdempotent(t *testing.T) {
	c, server := probePair(t)
	c.lastIO = time.Now()
	server.Close()
	time.Sleep(20 * time.Millisecond)
	if !c.alive(true) {
		t.Fatal("idempotent commands skip the probe on a recently used channel")
	}
	if c.alive(false) {
		t.Fatal("non-idempotent commands must always probe")
	}
}

// theft: two clients against one single-slot editor.
func twoClients(t *testing.T) (*uexectest.Editor, *Session, *Session, *time.Time) {
	t.Helper()
	ce := &countingEditor{runs: map[string]int{}}
	e, _ := uexectest.Start(uexectest.Options{OnCommand: ce.handler, SingleSlot: true})
	t.Cleanup(e.Close)
	clock := time.Now()
	a := dialFake(t, e, testCfg())
	b := dialFake(t, e, testCfg())
	a.now = func() time.Time { return clock }
	b.now = func() time.Time { return clock }
	return e, a, b, &clock
}

func TestTheftDetectedOnReSteal(t *testing.T) {
	e, a, b, _ := twoClients(t)
	ctx := context.Background()
	mustRun := func(s *Session, cmd string) {
		t.Helper()
		if res, err := s.RunCommand(ctx, cmd, ModeExecFile); err != nil || res.Result != cmd {
			t.Fatalf("%s: %v %+v", cmd, err, res)
		}
	}
	mustRun(a, "A1") // a holds the slot
	mustRun(b, "B1") // b steals it
	mustRun(a, "A2") // a sees the peer close, reconnects (steals back) — first close: not theft
	mustRun(b, "B2") // b likewise
	// a's channel was closed again within the window, editor still answering: theft.
	_, err := a.RunCommand(ctx, "A3", ModeExecFile)
	if !errors.Is(err, ErrChannelStolen) {
		t.Fatalf("want ErrChannelStolen, got %v", err)
	}
	if !a.Stolen() {
		t.Fatal("session should report stolen")
	}
	// No further connection attempts until an explicit reclaim.
	before := e.Connections()
	for i := 0; i < 3; i++ {
		if _, err := a.RunCommand(ctx, "A4", ModeExecFile); !errors.Is(err, ErrChannelStolen) {
			t.Fatalf("stolen session must fail fast, got %v", err)
		}
	}
	if e.Connections() != before {
		t.Fatalf("stolen session reconnected (%d -> %d)", before, e.Connections())
	}
	a.Reclaim()
	mustRun(a, "A5")
	if e.Connections() != before+1 {
		t.Fatalf("reclaim should take the slot back with one new connection")
	}
}

func TestPeerClosesOutsideTheftWindowAreNotTheft(t *testing.T) {
	_, a, b, clock := twoClients(t)
	ctx := context.Background()
	run := func(s *Session, cmd string) error { _, err := s.RunCommand(ctx, cmd, ModeExecFile); return err }
	_ = run(a, "A1")
	_ = run(b, "B1")
	_ = run(a, "A2") // first peer close for a
	_ = run(b, "B2")
	*clock = clock.Add(theftWindow + time.Second) // the next close is long after a's reconnect
	if err := run(a, "A3"); err != nil {
		t.Fatalf("a peer close outside the theft window must reconnect normally, got %v", err)
	}
	if a.Stolen() {
		t.Fatal("not theft")
	}
}
