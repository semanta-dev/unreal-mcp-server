package cockpit

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Host is the Go-hosted human cockpit server (EDITOR_PLUGIN_PLAN.md §3.2): an
// http.Server on a second loopback port that serves the SPA shell and streams the
// editor's observation feed to the browser over SSE. It is authenticated (a per-session
// token on every API route) and hardened against CSRF / DNS-rebinding (Host + Origin
// allowlist), because it is a local control surface that can stop the agent.
//
// The event stream uses the fetch()+ReadableStream contract, not EventSource: the token
// rides an Authorization: Bearer header (never a URL), and resume is driven by the
// Last-Event-ID header mapping to the Hub's seq ring (§3.2, fix #8).
type Host struct {
	hub     *Hub
	token   string
	origins map[string]bool // allowed Origin values (scheme://host[:port]); empty = allow same-loopback only
	spa     []byte
	control ControlFunc
}

// ControlFunc sends a control frame to the editor (stop/approve/deny/replay/…). The Host
// calls it from POST /control after auth. It returns an error the Host surfaces as 502.
type ControlFunc func(*Frame) error

// HostConfig parametrizes the cockpit server.
type HostConfig struct {
	Token          string   // per-session bearer token (required)
	AllowedOrigins []string // exact Origin values permitted for API routes (loopback SPA origin)
	SPA            []byte   // the index.html shell; a minimal default is used if nil
}

// NewHost builds a cockpit host. control may be nil (a read-only cockpit).
func NewHost(cfg HostConfig, hub *Hub, control ControlFunc) *Host {
	origins := map[string]bool{}
	for _, o := range cfg.AllowedOrigins {
		origins[o] = true
	}
	spa := cfg.SPA
	if spa == nil {
		spa = defaultSPA
	}
	return &Host{hub: hub, token: cfg.Token, origins: origins, spa: spa, control: control}
}

// Handler returns the cockpit's http.Handler with the full auth chain applied.
func (h *Host) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleSPA)
	mux.HandleFunc("/events", h.authAPI(h.handleEvents))
	mux.HandleFunc("/control", h.authAPI(h.handleControl))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return h.hostGuard(mux)
}

// hostGuard rejects any request whose Host header is not a loopback address — the
// primary DNS-rebinding defense (a rebound name resolves to 127.0.0.1 but arrives with
// a non-loopback Host header).
func (h *Host) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if !isLoopbackHost(host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authAPI guards an API route: constant-time bearer-token match + Origin allowlist. A
// missing/simple request with no bearer is rejected, so a cross-site form or img cannot
// drive /control, and a rebound page cannot read /events.
func (h *Host) authAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.checkToken(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !h.originAllowed(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (h *Host) checkToken(r *http.Request) bool {
	if h.token == "" {
		return false // never run token-less
	}
	got := ""
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		got = strings.TrimPrefix(a, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func (h *Host) originAllowed(origin string) bool {
	if h.origins[origin] {
		return true
	}
	// Default: allow a loopback origin (the SPA served from this host).
	if u, err := url.Parse(origin); err == nil {
		host := u.Hostname()
		return isLoopbackHost(host)
	}
	return false
}

func (h *Host) handleSPA(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Lock the shell down: it inlines everything, so a strict CSP costs nothing and blocks
	// any injected external fetch.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	_, _ = w.Write(h.spa)
}

// handleEvents streams the observation feed as SSE, resuming from Last-Event-ID.
func (h *Host) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	fromSeq := parseLastEventID(r)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	id, ch, backlog := h.hub.Subscribe(fromSeq, 512)
	defer h.hub.Unsubscribe(id)

	for _, ev := range backlog {
		if !writeSSE(w, ev) {
			return
		}
	}
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if !writeSSE(w, ev) {
				return
			}
			flusher.Flush()
		}
	}
}

// handleControl accepts a JSON control action and forwards it to the editor. POST-only
// with a required bearer makes it a non-simple request, so it cannot be driven by a
// cross-site form.
func (h *Host) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.control == nil {
		http.Error(w, "control unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Control string `json:"control"`
		OpID    string `json:"op_id"`
		GateID  string `json:"gate_id"`
		FromSeq uint64 `json:"from_seq"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Control == "" {
		http.Error(w, "missing control", http.StatusBadRequest)
		return
	}
	f := &Frame{Type: FrameControl, Control: ControlKind(req.Control), OpID: req.OpID, GateID: req.GateID, FromSeq: req.FromSeq}
	if err := h.control(f); err != nil {
		if errors.Is(err, ErrGateNotApprovable) {
			// A late click on a severed/expired/decided gate — the op did NOT run.
			http.Error(w, "gate not approvable (severed/expired/decided)", http.StatusConflict)
			return
		}
		http.Error(w, "editor unreachable", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// --- helpers ---

func parseLastEventID(r *http.Request) uint64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("lastEventId")
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	return n
}

func writeSSE(w http.ResponseWriter, ev HubEvent) bool {
	// SSE data must be single-line. ev.Data is a json.RawMessage that preserves its
	// source whitespace verbatim, and the editor's JSON writer can pretty-print (newlines
	// + tabs) — which would split the frame. Compact defensively (a no-op on already-
	// compact input) to strip all inter-token whitespace, killing the whole class.
	data := ev.Data
	if len(data) > 0 {
		var buf bytes.Buffer
		if json.Compact(&buf, ev.Data) == nil {
			data = buf.Bytes()
		}
	} else {
		data = []byte("{}")
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, sseField(ev.Type), data); err != nil {
		return false
	}
	return true
}

// sseField strips CR/LF from a field value so it can't break SSE framing.
func sseField(s string) string {
	return strings.NewReplacer("\r", "", "\n", " ").Replace(s)
}

func isLoopbackHost(host string) bool {
	// Normalize: strip brackets from an IPv6 literal without a port (SplitHostPort fails
	// on "[::1]"), lower-case, and drop a trailing FQDN dot.
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// defaultSPA is a small but REAL cockpit client: it POSTs STOP with the bearer (and
// echoes the ack latency, so the <100ms STOP gate is demonstrable), reconnects on stream
// end, and captures each frame's id to resend as Last-Event-ID so a reconnect resumes
// the gap rather than replaying the whole ring. A richer SPA can be injected via
// HostConfig.SPA; this one is enough to exercise the A1 exit gate. (No backticks: it is a
// Go raw string literal.)
var defaultSPA = []byte(`<!doctype html><meta charset=utf-8><title>MCP Cockpit</title>
<style>
 body{font:14px system-ui;margin:0;background:#111;color:#ddd}
 #hdr{padding:8px 12px;background:#1a1a1a;border-bottom:1px solid #333;display:flex;gap:12px;align-items:center}
 #st{color:#8c8}
 #stop{background:#c33;color:#fff;border:0;padding:6px 16px;border-radius:4px;cursor:pointer;font-weight:700}
 #stop:active{background:#911}
 #feed{padding:8px 12px;font-family:ui-monospace,monospace;white-space:pre-wrap}
 .row{padding:2px 0;border-bottom:1px solid #1e1e1e}
 .t{color:#6af}
</style>
<body>
<div id=hdr><b>MCP Cockpit</b><button id=stop>STOP</button><span id=st>connecting…</span></div>
<div id=feed></div>
<script>
var tok = location.hash.slice(1);
var st = document.getElementById('st'), feed = document.getElementById('feed');
var lastId = '';
document.getElementById('stop').onclick = function(){
  var t0 = performance.now();
  fetch('/control', {method:'POST', headers:{'Authorization':'Bearer '+tok, 'Content-Type':'application/json'}, body:'{"control":"stop"}'})
    .then(function(r){ st.textContent = r.ok ? ('STOP acked in '+Math.round(performance.now()-t0)+'ms') : ('STOP failed '+r.status); })
    .catch(function(){ st.textContent='STOP send failed'; });
};
function addRow(ev, data){
  var el=document.createElement('div'); el.className='row';
  var s=document.createElement('span'); s.className='t'; s.textContent='['+ev+'] ';
  el.appendChild(s); el.appendChild(document.createTextNode(data||'')); feed.prepend(el);
}
function parseFrame(chunk){ var o={}; chunk.split('\n').forEach(function(l){ var i=l.indexOf(': '); if(i>=0) o[l.slice(0,i)]=l.slice(i+2); }); return o; }
async function stream(){
  var hdrs={'Authorization':'Bearer '+tok}; if(lastId) hdrs['Last-Event-ID']=lastId;
  var res; try { res=await fetch('/events',{headers:hdrs}); } catch(e){ return true; }
  if(!res.ok){ st.textContent='auth failed ('+res.status+')'; return false; }
  st.textContent='live';
  var rd=res.body.getReader(), dec=new TextDecoder(), buf='';
  for(;;){ var r; try { r=await rd.read(); } catch(e){ break; } if(r.done) break;
    buf+=dec.decode(r.value,{stream:true}); var i;
    while((i=buf.indexOf('\n\n'))>=0){ var f=parseFrame(buf.slice(0,i)); buf=buf.slice(i+2);
      if(f.id) lastId=f.id; if(f.event) addRow(f.event, f.data); }
  }
  return true;
}
async function loop(){ for(;;){ var again=await stream(); if(!again) return; st.textContent='reconnecting…'; await new Promise(function(r){ setTimeout(r,1000); }); } }
loop();
</script>`)
