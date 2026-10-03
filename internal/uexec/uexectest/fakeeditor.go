// Package uexectest is an in-repo protocol double for the Unreal editor's
// PythonScriptPlugin remote-execution server. It lets the uexec protocol port be
// tested end-to-end (discovery + reverse-connect + command/result) over loopback
// with NO live editor, and deliberately uses its own independent message
// encoding so the two implementations must agree on the wire.
//
// For unit tests it binds a unicast loopback UDP socket (the discoverer is
// pointed at Addr()); for the integration build tag it can join the real
// multicast group. Behaviors (slow, large, exact-8192, malformed, refuse
// connect-back) are scriptable via Options to exercise the framing and
// timeout/taint/recovery paths.
package uexectest

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion = 1
	protocolMagic   = "ue_py"
)

type message struct {
	Version int             `json:"version"`
	Magic   string          `json:"magic"`
	Type    string          `json:"type"`
	Source  string          `json:"source"`
	Dest    string          `json:"dest,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// CommandRequest is a decoded command message handed to a handler.
type CommandRequest struct {
	Command    string
	ExecMode   string
	Unattended bool
}

// CommandResponse is what a handler returns; it becomes the command_result data.
type CommandResponse struct {
	Success bool          `json:"success"`
	Result  string        `json:"result"`
	Output  []OutputEntry `json:"output"`
}

// OutputEntry mirrors the protocol's output entries.
type OutputEntry struct {
	Type   string `json:"type"`
	Output string `json:"output"`
}

// Options configures a fake editor. All fields are optional.
type Options struct {
	NodeID    string                               // defaults to a random id
	Pong      map[string]any                       // pong metadata (engine_version, project_root, ...)
	OnCommand func(CommandRequest) CommandResponse // defaults to an echo handler

	// RawResponse, if set, overrides the normal command_result with arbitrary
	// bytes (to inject wrong magic/version/source or garbage). Takes precedence.
	RawResponse func(src string) []byte

	RefuseConnectBack   bool          // never dial back on open_connection (busy_accept)
	ReplyDelay          time.Duration // sleep before replying (timeout tests)
	SplitWritesAt       int           // write the reply in chunks of this size (segmentation)
	PadResultToMultiple int           // pad Result so the serialized reply is a multiple of this
	CloseAfterReplies   int           // close the command channel after replying to N commands (drop-recovery tests)
	CloseChannels       int           // with CloseAfterReplies: only the first N channels are closed (0 = every channel)

	// DropReplyIf, when it returns true for a command, runs OnCommand (the command
	// EXECUTES) and then closes the channel without replying — the exactly-once case.
	DropReplyIf func(CommandRequest) bool
	// SingleSlot models UE's single command connection: a new open_connection
	// closes every existing command channel (a different client steals the slot; the
	// same client reconnecting replaces its own).
	SingleSlot bool
	// StickyConnection models UE 5.7's FPythonScriptRemoteExecution::OpenCommandConnection:
	// an open_connection from the node+endpoint the editor believes it is already
	// connected to is ignored until that node sends close_connection — even when the
	// client has dropped the TCP side (e.g. after a timeout).
	StickyConnection bool
}

// Editor is a running fake editor. Close it when done.
type Editor struct {
	opts Options
	udp  net.PacketConn
	addr *net.UDPAddr

	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup

	mu       sync.Mutex
	conns    map[net.Conn]bool // live command channels (SingleSlot)
	accepted int               // command channels ever connected back
	believed map[string]string // StickyConnection: node id -> endpoint the editor thinks is connected
}

// Connections reports how many command channels the editor has accepted so far
// (theft tests assert a victim stops reconnecting).
func (e *Editor) Connections() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.accepted
}

// Start binds a unicast loopback UDP socket and serves the discovery + command
// protocol. Point a discoverer's send address at Addr().
func Start(opts Options) (*Editor, error) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	e := &Editor{
		opts:   fillDefaults(opts),
		udp:    pc,
		addr:   pc.LocalAddr().(*net.UDPAddr),
		closed: make(chan struct{}),
		conns:  map[net.Conn]bool{},
	}
	e.wg.Add(1)
	go e.serveUDP()
	return e, nil
}

func fillDefaults(o Options) Options {
	if o.NodeID == "" {
		o.NodeID = randID()
	}
	if o.Pong == nil {
		o.Pong = map[string]any{
			"engine_version": "5.7.0-fake+fakeeditor",
			"engine_root":    "D:/Unreal/Engine/UE_5.7",
			"project_name":   "FakeProject",
			"project_root":   "C:/fake/FakeProject",
			"user":           "tester",
			"machine":        "ci",
		}
	}
	if o.OnCommand == nil {
		o.OnCommand = func(req CommandRequest) CommandResponse {
			return CommandResponse{Success: true, Result: req.Command,
				Output: []OutputEntry{{Type: "Info", Output: req.Command}}}
		}
	}
	return o
}

// Addr is the fake editor's UDP address; a discoverer sends pings/open_connection here.
func (e *Editor) Addr() net.Addr { return e.addr }

// NodeID is the fake editor's node id (the pong source).
func (e *Editor) NodeID() string { return e.opts.NodeID }

func (e *Editor) serveUDP() {
	defer e.wg.Done()
	buf := make([]byte, 65536)
	for {
		select {
		case <-e.closed:
			return
		default:
		}
		_ = e.udp.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, src, err := e.udp.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			select {
			case <-e.closed:
				return
			default:
				continue
			}
		}
		var m message
		if json.Unmarshal(buf[:n], &m) != nil || m.Magic != protocolMagic || m.Version != protocolVersion {
			continue
		}
		switch m.Type {
		case "ping":
			e.sendPong(src)
		case "open_connection":
			if !e.opts.RefuseConnectBack {
				var d struct {
					CommandIP   string `json:"command_ip"`
					CommandPort int    `json:"command_port"`
				}
				_ = json.Unmarshal(m.Data, &d)
				ep := fmt.Sprintf("%s:%d", d.CommandIP, d.CommandPort)
				e.mu.Lock()
				sticky := e.opts.StickyConnection && e.believed[m.Source] == ep
				if e.opts.StickyConnection {
					if e.believed == nil {
						e.believed = map[string]string{}
					}
					e.believed[m.Source] = ep
				}
				e.mu.Unlock()
				if !sticky {
					e.wg.Add(1)
					go e.handleCommandChannel(m.Source, d.CommandIP, d.CommandPort)
				}
			}
		case "close_connection":
			e.mu.Lock()
			delete(e.believed, m.Source)
			e.mu.Unlock()
		}
	}
}

func (e *Editor) sendPong(dst net.Addr) {
	data, _ := json.Marshal(e.opts.Pong)
	raw, _ := json.Marshal(message{
		Version: protocolVersion, Magic: protocolMagic, Type: "pong",
		Source: e.opts.NodeID, Data: data,
	})
	_, _ = e.udp.WriteTo(raw, dst)
}

func (e *Editor) handleCommandChannel(remoteSource, ip string, port int) {
	defer e.wg.Done()
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort(ip, fmt.Sprintf("%d", port)), 5*time.Second)
	if err != nil {
		return
	}
	defer conn.Close()
	e.mu.Lock()
	if e.opts.SingleSlot {
		for old := range e.conns {
			_ = old.Close() // the slot moves to the newest client
		}
		e.conns = map[net.Conn]bool{}
	}
	e.conns[conn] = true
	e.accepted++
	channel := e.accepted
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.conns, conn)
		e.mu.Unlock()
	}()

	// Unblock the blocking Decode below when the editor is closed, so Close()'s
	// wg.Wait() doesn't hang if the client hasn't closed this channel yet.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-e.closed:
			_ = conn.Close()
		case <-stop:
		}
	}()

	dec := json.NewDecoder(bufio.NewReaderSize(conn, 65536))
	replies := 0
	for {
		var m message
		if err := dec.Decode(&m); err != nil {
			return // EOF when the client closes/taints the connection
		}
		if m.Type != "command" {
			continue
		}
		if e.opts.ReplyDelay > 0 {
			select {
			case <-time.After(e.opts.ReplyDelay):
			case <-e.closed:
				return
			}
		}
		out, drop := e.buildReply(m)
		if drop {
			return // executed, never replied: the client must not know the outcome
		}
		if err := e.writeReply(conn, out); err != nil {
			return
		}
		replies++
		if e.opts.CloseAfterReplies > 0 && replies >= e.opts.CloseAfterReplies &&
			(e.opts.CloseChannels == 0 || channel <= e.opts.CloseChannels) {
			return // drop the channel (deferred conn.Close) — the client must reconnect
		}
	}
}

func (e *Editor) buildReply(cmd message) ([]byte, bool) {
	if e.opts.RawResponse != nil {
		return e.opts.RawResponse(cmd.Source), false
	}
	var cd struct {
		Command    string `json:"command"`
		ExecMode   string `json:"exec_mode"`
		Unattended bool   `json:"unattended"`
	}
	_ = json.Unmarshal(cmd.Data, &cd)
	req := CommandRequest{Command: cd.Command, ExecMode: cd.ExecMode, Unattended: cd.Unattended}
	resp := e.opts.OnCommand(req)
	drop := e.opts.DropReplyIf != nil && e.opts.DropReplyIf(req)

	if e.opts.PadResultToMultiple > 0 {
		resp.Result = padResult(resp, cmd.Source, e.opts.NodeID, e.opts.PadResultToMultiple)
	}
	data, _ := json.Marshal(resp)
	raw, _ := json.Marshal(message{
		Version: protocolVersion, Magic: protocolMagic, Type: "command_result",
		Source: e.opts.NodeID, Dest: cmd.Source, Data: data,
	})
	return raw, drop
}

// padResult returns a Result padded with 'x' so the fully-serialized command_result
// message length is a multiple of `mult` (exercises the exact-8192 framing case).
func padResult(resp CommandResponse, dest, nodeID string, mult int) string {
	measure := func(result string) int {
		r := resp
		r.Result = result
		data, _ := json.Marshal(r)
		raw, _ := json.Marshal(message{
			Version: protocolVersion, Magic: protocolMagic, Type: "command_result",
			Source: nodeID, Dest: dest, Data: data,
		})
		return len(raw)
	}
	base := measure(resp.Result)
	target := ((base / mult) + 1) * mult
	pad := target - base
	if pad < 0 {
		pad = 0
	}
	return resp.Result + strings.Repeat("x", pad)
}

func (e *Editor) writeReply(conn net.Conn, raw []byte) error {
	if e.opts.SplitWritesAt > 0 {
		for off := 0; off < len(raw); off += e.opts.SplitWritesAt {
			end := off + e.opts.SplitWritesAt
			if end > len(raw) {
				end = len(raw)
			}
			if _, err := conn.Write(raw[off:end]); err != nil {
				return err
			}
			time.Sleep(2 * time.Millisecond) // force distinct TCP segments
		}
		return nil
	}
	_, err := conn.Write(raw)
	return err
}

// Close stops the fake editor and waits for its goroutines.
func (e *Editor) Close() {
	e.closeOnce.Do(func() {
		close(e.closed)
		_ = e.udp.Close()
	})
	e.wg.Wait()
}

func randID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
