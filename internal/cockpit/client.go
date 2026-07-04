package cockpit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Errors surfaced to callers.
var (
	ErrClosed           = errors.New("cockpit: client closed")
	ErrProtocolMismatch = errors.New("cockpit: protocol version mismatch")
	ErrHandshake        = errors.New("cockpit: handshake failed")
)

// Handlers receive pushed frames off the client's read loop. They MUST NOT block (the
// read loop is single-threaded); hand off to a buffered channel if work is needed.
type Handlers struct {
	OnEvent    func(*Frame) // event frames (observations); Frame.Dropped>0 signals a ring drop
	OnProgress func(*Frame) // progress frames for a long op
	OnGate     func(*Frame) // an op parked pending human approval
}

// Client is one framed socket to one editor's MCPCore listener. It handshakes, then
// runs a single read loop that resolves rpc results by op_id and delivers
// event/progress/gate frames to Handlers. Writes are serialized so frames are never
// interleaved on the wire. LastSeq tracks the highest observation seq for replay-from.
type Client struct {
	conn     net.Conn
	handlers Handlers

	epoch          string
	manifestDigest string
	engineVersion  string
	project        string

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan *Frame // op_id -> result waiter
	lastSeq uint64                 // highest event/progress seq observed

	nonce string // per-client op_id prefix (unique across sessions)
	opSeq atomic.Uint64

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  atomic.Value // error
}

// DialConfig parametrizes the handshake.
type DialConfig struct {
	Token       string
	LastSeenSeq uint64 // resume point: ask the editor to replay observations after this
	KnownEpoch  string // last-known session_epoch for this project (epoch compare, §2.5)
	Timeout     time.Duration
}

// Dial connects to addr (the editor's ephemeral 127.0.0.1:port from cockpit_info),
// performs the hello/welcome handshake, and starts the read loop.
func Dial(ctx context.Context, addr string, cfg DialConfig, h Handlers) (*Client, error) {
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cockpit dial %s: %w", addr, err)
	}
	c, err := newClient(conn, cfg, h)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// newClient runs the handshake over an already-established conn (net.Pipe in tests)
// and starts the read loop.
func newClient(conn net.Conn, cfg DialConfig, h Handlers) (*Client, error) {
	nonceBytes := make([]byte, 6)
	_, _ = rand.Read(nonceBytes)
	c := &Client{
		conn:     conn,
		handlers: h,
		pending:  map[string]chan *Frame{},
		nonce:    hex.EncodeToString(nonceBytes),
		closed:   make(chan struct{}),
	}
	// Handshake: hello -> welcome. Bounded by the dial timeout if set.
	if cfg.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	}
	if err := c.writeFrame(&Frame{
		Type: FrameHello, Token: cfg.Token, LastSeenSeq: cfg.LastSeenSeq,
		KnownEpoch: cfg.KnownEpoch, ProtocolVersion: ProtocolVersion,
	}); err != nil {
		return nil, fmt.Errorf("%w: hello: %v", ErrHandshake, err)
	}
	welcome, err := ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("%w: welcome: %v", ErrHandshake, err)
	}
	if welcome.Type != FrameWelcome {
		return nil, fmt.Errorf("%w: expected welcome, got %q", ErrHandshake, welcome.Type)
	}
	if welcome.ProtocolVersion != 0 && welcome.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("%w: editor v%d, go v%d", ErrProtocolMismatch, welcome.ProtocolVersion, ProtocolVersion)
	}
	c.epoch = welcome.SessionEpoch
	c.manifestDigest = welcome.ManifestDigest
	c.engineVersion = welcome.EngineVersion
	c.project = welcome.Project
	c.lastSeq = cfg.LastSeenSeq
	_ = conn.SetDeadline(time.Time{}) // clear the handshake deadline; the read loop blocks
	go c.readLoop()
	return c, nil
}

func (c *Client) writeFrame(f *Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteFrame(c.conn, f)
}

// RPC invokes op on the editor and blocks until its rpc_result, ctx cancellation, or
// the client closing. It mints a unique op_id and matches the result by it.
func (c *Client) RPC(ctx context.Context, op string, args json.RawMessage, intent, taskID string) (*Frame, error) {
	opID := c.nonce + "-" + strconv.FormatUint(c.opSeq.Add(1), 36)
	ch := make(chan *Frame, 1)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, c.err()
	default:
	}
	c.pending[opID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, opID)
		c.mu.Unlock()
	}()

	if err := c.writeFrame(&Frame{Type: FrameRPC, OpID: opID, Op: op, Args: args, Intent: intent, TaskID: taskID}); err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		// The op keeps running editor-side; a caller cancel doesn't cancel the op.
		// Send a cancel control so the editor can drop it if still queued (§2.4).
		_ = c.Control(&Frame{Type: FrameControl, Control: CtrlCancel, OpID: opID})
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.err()
	}
}

// Control sends an out-of-band control frame (cancel/stop/pause/approve/replay/…). It
// is drained by the editor's rx thread, so a stop/cancel is received off the game
// thread even mid-op (§2.4).
func (c *Client) Control(f *Frame) error {
	f.Type = FrameControl
	return c.writeFrame(f)
}

// Convenience control senders.
func (c *Client) Stop() error              { return c.Control(&Frame{Control: CtrlStop}) }
func (c *Client) Cancel(opID string) error { return c.Control(&Frame{Control: CtrlCancel, OpID: opID}) }
func (c *Client) Approve(gateID string) error {
	return c.Control(&Frame{Control: CtrlApprove, GateID: gateID})
}
func (c *Client) Deny(gateID string) error {
	return c.Control(&Frame{Control: CtrlDeny, GateID: gateID})
}
func (c *Client) FocusTarget(gateID string) error {
	return c.Control(&Frame{Control: CtrlFocusTarget, GateID: gateID})
}
func (c *Client) ReplayFrom(seq uint64) error {
	return c.Control(&Frame{Control: CtrlReplayFrom, FromSeq: seq})
}

func (c *Client) readLoop() {
	for {
		f, err := ReadFrame(c.conn)
		if err != nil {
			c.fail(err)
			return
		}
		switch f.Type {
		case FrameRPCResult:
			// Delete-on-delivery makes a duplicate rpc_result for the same op_id find no
			// waiter (ch==nil), and the non-blocking send means even a live-but-departed
			// waiter can never wedge the single read loop.
			c.mu.Lock()
			ch := c.pending[f.OpID]
			delete(c.pending, f.OpID)
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- f: // buffered(1)
				default:
				}
			}
		case FrameEvent:
			c.bumpSeq(f.Seq)
			if c.handlers.OnEvent != nil {
				c.handlers.OnEvent(f)
			}
		case FrameProgress:
			c.bumpSeq(f.Seq)
			if c.handlers.OnProgress != nil {
				c.handlers.OnProgress(f)
			}
		case FrameGate:
			if c.handlers.OnGate != nil {
				c.handlers.OnGate(f)
			}
		default:
			// Unknown/handshake frames after welcome are ignored (forward-compat).
		}
	}
}

func (c *Client) bumpSeq(seq uint64) {
	if seq == 0 {
		return
	}
	c.mu.Lock()
	if seq > c.lastSeq {
		c.lastSeq = seq
	}
	c.mu.Unlock()
}

// LastSeq is the highest observation seq seen — the resume point for a reconnect.
func (c *Client) LastSeq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSeq
}

// Epoch is the plugin-minted session_epoch from the handshake (§2.5).
func (c *Client) Epoch() string          { return c.epoch }
func (c *Client) ManifestDigest() string { return c.manifestDigest }

// Done is closed when the client's read loop stops.
func (c *Client) Done() <-chan struct{} { return c.closed }

// Close tears the socket down and fails all pending RPCs.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Client) fail(err error) {
	c.closeOnce.Do(func() {
		c.closeErr.Store(err)
		_ = c.conn.Close()
		close(c.closed)
		// Fail any in-flight RPCs with the close error.
		c.mu.Lock()
		pend := c.pending
		c.pending = map[string]chan *Frame{}
		c.mu.Unlock()
		for _, ch := range pend {
			// non-blocking: RPC waiters also select on c.closed, so this is best-effort
			select {
			case ch <- &Frame{Type: FrameRPCResult, OK: boolPtr(false), Code: "EDITOR_DISCONNECTED", Error: err.Error()}:
			default:
			}
		}
	})
}

func (c *Client) err() error {
	if e, ok := c.closeErr.Load().(error); ok && e != nil {
		return e
	}
	return ErrClosed
}

func boolPtr(b bool) *bool { return &b }
