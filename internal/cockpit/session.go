package cockpit

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Session ties one editor's framed transport (Client) to the human cockpit (Hub + Host).
// It is the integration that makes A0 (transport) and A1 (cockpit) run together
// (EDITOR_PLUGIN_PLAN.md §2.3): the editor's pushed observations flow Client → Hub →
// browser SSE, and browser control (STOP/approve/deny/replay) flows Host → Client →
// editor. It owns a second loopback HTTP server for the browser.
//
// Two seq spaces meet here and are kept distinct on purpose: the editor↔Client space
// (editor event seq, drives editor-side replay-from) and the Hub↔browser FEED space (a
// monotonic feed seq the Session assigns, drives SSE Last-Event-ID resume). The editor's
// own seq is preserved inside each feed item's payload.
type Session struct {
	client   *Client
	hub      *Hub
	host     *Host
	server   *http.Server
	httpAddr string
	token    string
	feedSeq  atomic.Uint64
}

// SessionConfig parametrizes OpenSession. EditorAddr/EditorToken come from the
// cockpit_info probe over the existing uexec channel (§2.5 step 2).
type SessionConfig struct {
	EditorAddr    string        // 127.0.0.1:cockpit_port
	EditorToken   string        // token echoed to the editor on hello
	KnownEpoch    string        // last-known session_epoch (epoch compare)
	LastSeenSeq   uint64        // editor-seq resume point
	CockpitToken  string        // browser-facing bearer (required)
	CockpitListen string        // loopback listen addr; default 127.0.0.1:0 (ephemeral)
	HubCapacity   int           // feed ring depth; default 4096
	DialTimeout   time.Duration // editor dial/handshake timeout
	SPA           []byte        // optional custom cockpit SPA
}

// OpenSession dials the editor, starts the browser cockpit server, and wires the two
// together. On success the caller shows CockpitURL() to the human.
func OpenSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if cfg.HubCapacity == 0 {
		cfg.HubCapacity = 4096
	}
	if cfg.CockpitListen == "" {
		cfg.CockpitListen = "127.0.0.1:0"
	}
	s := &Session{hub: NewHub(cfg.HubCapacity), token: cfg.CockpitToken}

	// Editor → Hub: every pushed observation becomes a feed item (with a fresh feed seq;
	// the editor's own seq is carried inside the payload).
	handlers := Handlers{
		OnEvent:    func(f *Frame) { s.publish("event", eventEnvelope(f)) },
		OnProgress: func(f *Frame) { s.publish("progress", progressEnvelope(f)) },
		OnGate:     func(f *Frame) { s.publish("gate", gateEnvelope(f)) },
	}
	client, err := Dial(ctx, cfg.EditorAddr, DialConfig{
		Token: cfg.EditorToken, LastSeenSeq: cfg.LastSeenSeq, KnownEpoch: cfg.KnownEpoch, Timeout: cfg.DialTimeout,
	}, handlers)
	if err != nil {
		return nil, err
	}
	s.client = client

	// Browser control → editor. The Host has already authenticated the request.
	s.host = NewHost(HostConfig{Token: cfg.CockpitToken, SPA: cfg.SPA}, s.hub, func(f *Frame) error {
		return s.client.Control(f)
	})

	ln, err := net.Listen("tcp", cfg.CockpitListen)
	if err != nil {
		s.client.Close()
		return nil, err
	}
	s.httpAddr = ln.Addr().String()
	s.server = &http.Server{Handler: s.host.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.server.Serve(ln) }()

	// Publish an initial banner so a just-connected browser sees the session is live.
	s.publish("session", mustJSON(map[string]any{
		"epoch": client.Epoch(), "manifest_digest": client.ManifestDigest(),
	}))
	return s, nil
}

func (s *Session) publish(etype string, data json.RawMessage) {
	s.hub.Publish(s.feedSeq.Add(1), etype, data)
}

// CockpitURL is the browser entry point; the token rides the URL fragment (never sent to
// the server) and the SPA promotes it to an Authorization: Bearer header (§3.2).
func (s *Session) CockpitURL() string {
	return "http://" + s.httpAddr + "/#" + s.token
}

// HTTPAddr is the loopback address the cockpit server listens on.
func (s *Session) HTTPAddr() string { return s.httpAddr }

// EditorEpoch is the plugin-minted session_epoch of the connected editor.
func (s *Session) EditorEpoch() string { return s.client.Epoch() }

// Client exposes the underlying transport (for the daemon to dispatch rpc through the
// native backend when selected).
func (s *Session) Client() *Client { return s.client }

// Done is closed when the editor transport drops.
func (s *Session) Done() <-chan struct{} { return s.client.Done() }

// Close tears down the cockpit server and the editor transport.
func (s *Session) Close() error {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// --- feed-item envelopes (what the browser sees per SSE data payload) ---

func eventEnvelope(f *Frame) json.RawMessage {
	return mustJSON(map[string]any{
		"editor_seq": f.Seq,
		"type":       f.EventType,
		"payload":    rawOrNull(f.Payload),
		"dropped":    f.Dropped,
	})
}

func progressEnvelope(f *Frame) json.RawMessage {
	return mustJSON(map[string]any{
		"editor_seq": f.Seq,
		"op_id":      f.OpID,
		"payload":    rawOrNull(f.Payload),
	})
}

func gateEnvelope(f *Frame) json.RawMessage {
	return mustJSON(map[string]any{
		"gate_id":        f.GateID,
		"op_id":          f.OpID,
		"classification": f.Classification,
		"args_hash":      f.ArgsHash,
		"diff":           rawOrNull(f.Diff),
	})
}

func rawOrNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
