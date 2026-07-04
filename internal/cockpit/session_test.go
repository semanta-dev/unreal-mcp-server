package cockpit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// tcpFakeEditor is a real TCP-listening editor stand-in speaking the cockpit protocol,
// so OpenSession's Client can dial it exactly as it would a live MCPCore listener.
type tcpFakeEditor struct {
	ln    net.Listener
	epoch string

	mu       sync.Mutex
	conn     net.Conn
	controls []*Frame
	hello    *Frame
}

func newTCPFakeEditor(t *testing.T) *tcpFakeEditor {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fe := &tcpFakeEditor{ln: ln, epoch: "ep-e2e"}
	go fe.serve()
	t.Cleanup(func() { ln.Close() })
	return fe
}

func (fe *tcpFakeEditor) addr() string { return fe.ln.Addr().String() }

func (fe *tcpFakeEditor) serve() {
	conn, err := fe.ln.Accept()
	if err != nil {
		return
	}
	fe.mu.Lock()
	fe.conn = conn
	fe.mu.Unlock()
	hello, err := ReadFrame(conn)
	if err != nil {
		return
	}
	fe.mu.Lock()
	fe.hello = hello
	fe.mu.Unlock()
	_ = WriteFrame(conn, &Frame{Type: FrameWelcome, SessionEpoch: fe.epoch, ManifestDigest: "d", ProtocolVersion: ProtocolVersion})
	for {
		f, err := ReadFrame(conn)
		if err != nil {
			return
		}
		if f.Type == FrameControl {
			fe.mu.Lock()
			fe.controls = append(fe.controls, f)
			fe.mu.Unlock()
		}
	}
}

func (fe *tcpFakeEditor) push(f *Frame) error {
	fe.mu.Lock()
	c := fe.conn
	fe.mu.Unlock()
	if c == nil {
		return errors.New("no editor connection yet")
	}
	return WriteFrame(c, f)
}

func (fe *tcpFakeEditor) recvControls() []*Frame {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return append([]*Frame(nil), fe.controls...)
}

// TestSessionEndToEnd proves the full A0+A1 stack: an editor observation flows
// editor → Client → Hub → browser SSE, and a browser STOP flows browser → Host →
// Client → editor.
func TestSessionEndToEnd(t *testing.T) {
	fe := newTCPFakeEditor(t)
	sess, err := OpenSession(context.Background(), SessionConfig{
		EditorAddr:   fe.addr(),
		CockpitToken: "browser-tok",
		DialTimeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	if sess.EditorEpoch() != "ep-e2e" {
		t.Fatalf("epoch = %q", sess.EditorEpoch())
	}

	// A browser connects to the cockpit SSE feed.
	req, _ := http.NewRequest("GET", "http://"+sess.HTTPAddr()+"/events", nil)
	req.Header.Set("Authorization", "Bearer browser-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status %d", resp.StatusCode)
	}
	events := make(chan sseEvent, 32)
	go readSSE(resp.Body, events)

	// Editor pushes an observation → it must reach the browser.
	if err := fe.push(&Frame{Type: FrameEvent, Seq: 5, EventType: "pie.begin", Payload: json.RawMessage(`{"world":"PIE"}`)}); err != nil {
		t.Fatal(err)
	}
	if !waitEvent(t, events, func(ev sseEvent) bool {
		return ev.event == "event" && strings.Contains(ev.data, "pie.begin") && strings.Contains(ev.data, `"editor_seq":5`)
	}) {
		t.Fatal("editor event never reached the browser SSE")
	}

	// Editor pushes a gate → it must reach the browser as a gate feed item.
	if err := fe.push(&Frame{Type: FrameGate, GateID: "g1", OpID: "op1", Classification: "destructive", ArgsHash: "h", Diff: json.RawMessage(`{"Mass":{"before":100,"after":0}}`)}); err != nil {
		t.Fatal(err)
	}
	if !waitEvent(t, events, func(ev sseEvent) bool {
		return ev.event == "gate" && strings.Contains(ev.data, `"gate_id":"g1"`) && strings.Contains(ev.data, "before")
	}) {
		t.Fatal("gate never reached the browser SSE")
	}

	// Browser clicks STOP → the editor must receive a stop control.
	body := strings.NewReader(`{"control":"stop"}`)
	sreq, _ := http.NewRequest("POST", "http://"+sess.HTTPAddr()+"/control", body)
	sreq.Header.Set("Authorization", "Bearer browser-tok")
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	sresp.Body.Close()
	if sresp.StatusCode != http.StatusAccepted {
		t.Fatalf("STOP POST = %d", sresp.StatusCode)
	}
	waitFor(t, func() bool {
		for _, c := range fe.recvControls() {
			if c.Control == CtrlStop {
				return true
			}
		}
		return false
	})
}

// TestSessionGateFlow proves the human-in-the-loop wiring: an editor gate reaches the
// browser + the registry, an approve is authorized + forwarded to the editor, and a
// severed gate's approve is rejected 409 (never executed).
func TestSessionGateFlow(t *testing.T) {
	fe := newTCPFakeEditor(t)
	sess, err := OpenSession(context.Background(), SessionConfig{EditorAddr: fe.addr(), CockpitToken: "tok", DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	// browser SSE
	req, _ := http.NewRequest("GET", "http://"+sess.HTTPAddr()+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan sseEvent, 32)
	go readSSE(resp.Body, events)

	// editor parks a destructive op → gate frame
	fe.push(&Frame{Type: FrameGate, GateID: "g1", OpID: "op-x", Op: "delete_actor", Classification: "destructive", ArgsHash: "h", Diff: json.RawMessage(`{"n":{"before":1,"after":0}}`)})
	if !waitEvent(t, events, func(ev sseEvent) bool { return ev.event == "gate" && strings.Contains(ev.data, `"gate_id":"g1"`) }) {
		t.Fatal("gate never reached the browser")
	}
	waitFor(t, func() bool { return len(sess.Gates().Pending()) == 1 })

	// browser approves → 202 + the editor receives the approve control
	if code := postControl(t, sess, `{"control":"approve","gate_id":"g1"}`); code != http.StatusAccepted {
		t.Fatalf("approve = %d, want 202", code)
	}
	waitFor(t, func() bool {
		for _, c := range fe.recvControls() {
			if c.Control == CtrlApprove && c.GateID == "g1" {
				return true
			}
		}
		return false
	})

	// a second gate, then the agent departs (sever) → a late approve is rejected 409 and
	// is NOT forwarded to the editor.
	fe.push(&Frame{Type: FrameGate, GateID: "g2", OpID: "op-y", Op: "console", Classification: "exec", ArgsHash: "h2"})
	waitFor(t, func() bool { _, ok := sess.Gates().Get("g2"); return ok })
	sess.Gates().Sever("op-y") // same effect as the OnRPCCancel hook when the agent's ctx cancels
	if code := postControl(t, sess, `{"control":"approve","gate_id":"g2"}`); code != http.StatusConflict {
		t.Fatalf("severed-gate approve = %d, want 409", code)
	}
	// the editor must NOT have received an approve for g2
	for _, c := range fe.recvControls() {
		if c.Control == CtrlApprove && c.GateID == "g2" {
			t.Fatal("a severed gate's approve was forwarded to the editor — must not run")
		}
	}
}

func postControl(t *testing.T, sess *Session, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+sess.HTTPAddr()+"/control", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestSessionCockpitURL(t *testing.T) {
	fe := newTCPFakeEditor(t)
	sess, err := OpenSession(context.Background(), SessionConfig{EditorAddr: fe.addr(), CockpitToken: "tok123", DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	url := sess.CockpitURL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/#tok123") {
		t.Fatalf("cockpit URL = %q", url)
	}
}

func waitEvent(t *testing.T, ch <-chan sseEvent, match func(sseEvent) bool) bool {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if match(ev) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
