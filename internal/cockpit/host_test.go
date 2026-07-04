package cockpit

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHubBacklogAndDropOldest(t *testing.T) {
	h := NewHub(3)
	for i := uint64(1); i <= 5; i++ {
		h.Publish(i, "e", json.RawMessage(`{}`))
	}
	// capacity 3 → only seq 3,4,5 remain
	_, _, backlog := h.Subscribe(0, 10)
	if len(backlog) != 3 || backlog[0].Seq != 3 || backlog[2].Seq != 5 {
		t.Fatalf("backlog = %+v, want seq 3,4,5", backlog)
	}
	// resume after seq 4 → only seq 5
	_, _, b2 := h.Subscribe(4, 10)
	if len(b2) != 1 || b2[0].Seq != 5 {
		t.Fatalf("resume backlog = %+v, want [5]", b2)
	}
	if h.LastSeq() != 5 {
		t.Fatalf("LastSeq = %d", h.LastSeq())
	}
}

func TestHubFanoutAndUnsubscribe(t *testing.T) {
	h := NewHub(8)
	id, ch, _ := h.Subscribe(0, 8)
	if h.SubscriberCount() != 1 {
		t.Fatal("expected 1 subscriber")
	}
	h.Publish(1, "pie.begin", json.RawMessage(`{"x":1}`))
	select {
	case ev := <-ch:
		if ev.Seq != 1 || ev.Type != "pie.begin" {
			t.Fatalf("bad event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no fanout")
	}
	h.Unsubscribe(id)
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after unsubscribe")
	}
	if h.SubscriberCount() != 0 {
		t.Fatal("subscriber not removed")
	}
}

func newTestHost(t *testing.T, control ControlFunc) (*httptest.Server, *Hub) {
	t.Helper()
	hub := NewHub(64)
	host := NewHost(HostConfig{Token: "sekret"}, hub, control)
	srv := httptest.NewServer(host.Handler())
	t.Cleanup(srv.Close)
	return srv, hub
}

func TestHostAuthRejects(t *testing.T) {
	srv, _ := newTestHost(t, nil)
	cases := []struct {
		name   string
		path   string
		bearer string
		origin string
		host   string
		want   int
	}{
		{"no token", "/events", "", "", "", http.StatusUnauthorized},
		{"bad token", "/events", "wrong", "", "", http.StatusUnauthorized},
		{"good token", "/events", "sekret", "", "", http.StatusOK},
		{"bad origin", "/events", "sekret", "https://evil.com", "", http.StatusForbidden},
		{"loopback origin ok", "/events", "sekret", "http://127.0.0.1:9", "", http.StatusOK},
		{"bad host", "/events", "sekret", "", "evil.example.com", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+c.path, nil)
			if c.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+c.bearer)
			}
			if c.origin != "" {
				req.Header.Set("Origin", c.origin)
			}
			if c.host != "" {
				req.Host = c.host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				// an OK SSE stream stays open until ctx cancels; that's a "pass" for 200 cases
				if c.want == http.StatusOK && ctx.Err() != nil {
					return
				}
				t.Fatalf("req: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.want)
			}
		})
	}
}

func TestHostSSEBacklogAndLive(t *testing.T) {
	srv, hub := newTestHost(t, nil)
	hub.Publish(1, "a", json.RawMessage(`{"n":1}`))
	hub.Publish(2, "b", json.RawMessage(`{"n":2}`))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	req.Header.Set("Last-Event-ID", "1") // resume after seq 1 → backlog is just seq 2
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}

	events := make(chan sseEvent, 8)
	go readSSE(resp.Body, events)

	// backlog: seq 2 only (resumed after 1)
	got := expectSSE(t, events)
	if got.id != "2" || got.event != "b" {
		t.Fatalf("backlog event = %+v, want id2/b", got)
	}
	// live: publish seq 3, should arrive
	hub.Publish(3, "c", json.RawMessage(`{"n":3}`))
	got = expectSSE(t, events)
	if got.id != "3" || got.event != "c" || !strings.Contains(got.data, `"n":3`) {
		t.Fatalf("live event = %+v, want id3/c", got)
	}
}

func TestHostControlRouting(t *testing.T) {
	var mu sync.Mutex
	var got *Frame
	srv, _ := newTestHost(t, func(f *Frame) error { mu.Lock(); got = f; mu.Unlock(); return nil })

	// GET is rejected (POST-only)
	req, _ := http.NewRequest("GET", srv.URL+"/control", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /control = %d, want 405", resp.StatusCode)
	}
	resp.Body.Close()

	// POST stop routes to the control fn
	body := strings.NewReader(`{"control":"stop"}`)
	req, _ = http.NewRequest("POST", srv.URL+"/control", body)
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /control = %d, want 202", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil || got.Control != CtrlStop {
		t.Fatalf("control fn got %+v, want stop", got)
	}
}

func TestHostSPAServedWithoutToken(t *testing.T) {
	srv, _ := newTestHost(t, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("expected a CSP on the SPA shell")
	}
}

// --- SSE test reader ---

type sseEvent struct{ id, event, data string }

func readSSE(body interface{ Read([]byte) (int, error) }, out chan<- sseEvent) {
	sc := bufio.NewScanner(body)
	var cur sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.id != "" || cur.event != "" || cur.data != "" {
				out <- cur
				cur = sseEvent{}
			}
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func expectSSE(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
		return sseEvent{}
	}
}
