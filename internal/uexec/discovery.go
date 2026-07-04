package uexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// packetConn is the subset of net.PacketConn the discovery loops use. It is an
// interface so tests can drive discovery over a plain unicast loopback socket
// (avoiding flaky CI multicast) while production uses the real multicast conn.
type packetConn interface {
	ReadFrom(p []byte) (int, net.Addr, error)
	WriteTo(p []byte, addr net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// broadcastConn owns the UDP socket, runs discovery (ping/pong) loops, and sends
// open/close_connection messages for the command channel.
type broadcastConn struct {
	self   string
	pc     packetConn
	group  net.Addr // where pings/open_connection are sent (multicast group in prod)
	nodes  *nodeTable
	logger *slog.Logger

	pingInterval time.Duration
	nodeTimeout  time.Duration

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// openBroadcast builds the real multicast discovery connection and starts the loops.
func openBroadcast(ctx context.Context, cfg Config, self string, logger *slog.Logger) (*broadcastConn, error) {
	pc, group, lo, err := newMulticastConn(ctx, cfg)
	if err != nil {
		return nil, err
	}
	loName := "default"
	if lo != nil {
		loName = lo.Name
	}
	logger.Info("discovery listening", "group", cfg.MulticastGroup, "bind", cfg.BindAddress, "interface", loName, "ttl", cfg.MulticastTTL)
	if lo == nil {
		logger.Warn("no loopback interface found; multicast may not reach a same-host editor (see Risk #1)")
	}
	bc := newBroadcastConn(self, pc, group, cfg.PingInterval, cfg.NodeTimeout, logger)
	bc.start(ctx)
	return bc, nil
}

// newBroadcastConn constructs (without starting) a broadcastConn over an
// arbitrary packetConn. Used by openBroadcast and by tests.
func newBroadcastConn(self string, pc packetConn, group net.Addr, pingInterval, nodeTimeout time.Duration, logger *slog.Logger) *broadcastConn {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &broadcastConn{
		self:         self,
		pc:           pc,
		group:        group,
		nodes:        newNodeTable(),
		logger:       logger,
		pingInterval: pingInterval,
		nodeTimeout:  nodeTimeout,
	}
}

func (b *broadcastConn) start(ctx context.Context) {
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(2)
	go b.recvLoop(ctx)
	go b.pingLoop(ctx)
}

func (b *broadcastConn) recvLoop(ctx context.Context) {
	defer b.wg.Done()
	buf := make([]byte, 65536)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = b.pc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := b.pc.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // idle tick; re-check ctx
			}
			if ctx.Err() != nil {
				return // closed on shutdown
			}
			// transient read error (e.g. malformed datagram at OS level): keep going
			continue
		}
		msg, derr := decodeMessage(buf[:n])
		if derr != nil {
			continue // not a valid ue_py message; ignore
		}
		if !msg.passesFilter(b.self) {
			continue
		}
		if msg.Type == TypePong {
			b.nodes.upsert(msg.Source, msg.Data, time.Now())
		}
	}
}

func (b *broadcastConn) pingLoop(ctx context.Context) {
	defer b.wg.Done()
	t := time.NewTicker(b.pingInterval)
	defer t.Stop()
	b.sendPing() // ping immediately so discovery is fast
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.sendPing()
			b.nodes.sweep(time.Now(), b.nodeTimeout)
		}
	}
}

func (b *broadcastConn) sendPing() {
	msg := newMessage(TypePing, b.self, "", nil)
	raw, err := msg.encode()
	if err != nil {
		return
	}
	_, _ = b.pc.WriteTo(raw, b.group)
}

func (b *broadcastConn) broadcastOpenConnection(remoteNodeID, ip string, port int) error {
	data, err := marshalNoEscape(openConnData{CommandIP: ip, CommandPort: port})
	if err != nil {
		return err
	}
	raw, err := newMessage(TypeOpenConnection, b.self, remoteNodeID, data).encode()
	if err != nil {
		return err
	}
	_, err = b.pc.WriteTo(raw, b.group)
	return err
}

func (b *broadcastConn) broadcastCloseConnection(remoteNodeID string) {
	raw, err := newMessage(TypeCloseConnection, b.self, remoteNodeID, nil).encode()
	if err != nil {
		return
	}
	_, _ = b.pc.WriteTo(raw, b.group)
}

// waitForNode blocks until a node is selectable or timeout elapses. With
// projectDir set it prefers the matching node; on timeout with nodes present but
// no match, it falls back to the first node with an ambiguity warning (parity).
func (b *broadcastConn) waitForNode(ctx context.Context, projectDir string, timeout time.Duration, strict bool) (*Node, error) {
	// Strict selection is only meaningful with a project to match — pickNode's
	// no-filter path returns an arbitrary nodes[0], which under a lease would
	// cross-bind. Refuse rather than bind blind (defends a leased caller that set
	// StrictNode but forgot ProjectDir; the Spawner always sets both).
	if strict && projectDir == "" {
		return nil, fmt.Errorf("%w: strict node selection requires a project", ErrEditorNotFound)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if n, reason := pickNode(b.nodes.list(), projectDir); n != nil {
			b.logger.Info("selected editor node", "node_id", n.ID, "reason", reason,
				"project_root", n.ProjectRoot, "project_name", n.ProjectName, "engine", n.EngineVersion)
			return n, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			nodes := b.nodes.list()
			// Under a lease (strict), NEVER fall back to an arbitrary node — a
			// non-matching node is a DIFFERENT tenant's editor on the shared
			// discovery. Return not-found so the caller keeps polling for its own.
			if !strict && len(nodes) > 0 {
				n := nodes[0]
				if projectDir != "" {
					b.logger.Warn("no editor node matched project; falling back to first discovered node",
						"project_dir", projectDir, "node_id", n.ID, "project_root", n.ProjectRoot)
				}
				return n, nil
			}
			return nil, fmt.Errorf("%w within %s", ErrEditorNotFound, timeout)
		case <-tick.C:
		}
	}
}

func (b *broadcastConn) close() {
	b.closeOnce.Do(func() {
		if b.cancel != nil {
			b.cancel()
		}
		_ = b.pc.Close()
		b.wg.Wait()
	})
}
