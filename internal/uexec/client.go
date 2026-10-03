package uexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Session is the high-level remote-execution client: it runs discovery, opens a
// command channel to a chosen editor node, and runs commands with single-flight
// serialization and reconnect-once semantics (reference client parity).
type Session struct {
	cfg    Config
	self   string
	logger *slog.Logger

	mu         sync.Mutex // single-flight: serializes all commands (== Python _LOCK)
	bc         *broadcastConn
	cmd        *commandConn
	nodeID     string
	gen        uint64 // increments on each new command channel (reconnect/editor restart)
	sharedDisc bool   // true => bc is a shared Discovery this Session must not close

	// Theft detection (plan §2.8, case 4).
	now          func() time.Time
	peerReconnAt time.Time // when we last reconnected after a peer-initiated close
	stolen       bool
}

// theftWindow: a second peer close this soon after reconnecting from one, while the
// same editor still answers discovery, means another client holds the slot.
const theftWindow = 10 * time.Second

// New creates a self-contained Session (owns its own discovery). logger may be nil.
func New(cfg Config, logger *slog.Logger) *Session {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Session{cfg: cfg.withDefaults(), self: newUUID(), logger: logger, now: time.Now}
}

// NewOnDiscovery creates a PER-INSTANCE Session that SHARES a Discovery (§2): it
// reuses the shared multicast socket + node table + self-id, and owns only its own
// command channel (give cfg a distinct ephemeral CommandAddr "127.0.0.1:0" and a
// ProjectDir to pin the right editor node). Start() is a no-op (discovery already
// running); Close() tears down only this Session's command channel, never the
// shared Discovery. This is the Model-A daemon's per-lease bridge.
func NewOnDiscovery(cfg Config, disc *Discovery, logger *slog.Logger) *Session {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// A per-instance shared Session MUST use an ephemeral loopback reverse-connect
	// port — never inherit the fixed 6776 default (§1 rank-1: two shared sessions on
	// 6776 would collide, the exact hazard the split exists to prevent).
	if cfg.CommandAddr == "" {
		cfg.CommandAddr = "127.0.0.1:0"
	}
	return &Session{
		cfg: cfg.withDefaults(), self: disc.self, logger: logger,
		bc: disc.bc, sharedDisc: true, now: time.Now,
	}
}

// SelfID returns this session's node id (the protocol "source").
func (s *Session) SelfID() string { return s.self }

// Start begins node discovery. The provided context bounds the discovery loops'
// lifetime; Close also stops them.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bc != nil {
		return nil
	}
	bc, err := openBroadcast(ctx, s.cfg, s.self, s.logger)
	if err != nil {
		return err
	}
	s.bc = bc
	return nil
}

// Nodes returns a snapshot of currently discovered editor nodes.
func (s *Session) Nodes() []*Node {
	s.mu.Lock()
	bc := s.bc
	s.mu.Unlock()
	if bc == nil {
		return nil
	}
	return bc.nodes.list()
}

// WaitForNode blocks until a selectable editor node is found (project-filtered
// per cfg.ProjectDir) or the discovery timeout elapses.
func (s *Session) WaitForNode(ctx context.Context) (*Node, error) {
	s.mu.Lock()
	bc := s.bc
	s.mu.Unlock()
	if bc == nil {
		return nil, errors.New("uexec: session not started")
	}
	return bc.waitForNode(ctx, s.cfg.ProjectDir, s.cfg.DiscoveryTimeout, s.cfg.StrictNode)
}

// OpenCommand opens a command channel to a specific node id.
func (s *Session) OpenCommand(ctx context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openCommandLocked(ctx, nodeID)
}

func (s *Session) openCommandLocked(ctx context.Context, nodeID string) error {
	if s.cmd != nil {
		s.cmd.close()
		s.cmd = nil
	}
	if s.nodeID == nodeID {
		// Reconnecting to the same editor: release our previous channel first. UE
		// ignores open_connection while it still holds one from this node+endpoint
		// (OpenCommandConnection is a no-op when IsConnectedTo), and it does not
		// notice a channel we abandoned after a timeout — found live (P7): after a
		// modal dialog, every later call timed out until the server restarted.
		s.bc.broadcastCloseConnection(nodeID)
	}
	cmd := &commandConn{cfg: s.cfg, self: s.self, remote: nodeID, bc: s.bc, logger: s.logger, now: s.now}
	if err := cmd.open(ctx); err != nil {
		return err
	}
	s.cmd = cmd
	s.nodeID = nodeID
	s.gen++
	return nil
}

// Generation returns a counter that increments each time a new command channel
// is opened (initial connect, reconnect, or editor restart). A change signals
// that editor-side state (e.g. a hot-loaded module) may need re-verifying.
func (s *Session) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// RunCommand runs one command, opening/reconnecting the channel as needed.
// Single-flight (holds the session lock for the whole call). Connection loss is
// handled by the ordered decision table of plan §2.8:
//
//  0. before sending, probe the channel (idempotent commands skip this on a channel
//     used in the last 100 ms); a dead channel is replaced first
//     (nothing was sent, so no outcome uncertainty);
//  1. our own timeout/cancel taints the channel; the next call reconnects;
//  2. a failed write reached nothing executable — reconnect and re-send once;
//  3. a read-side loss after a successful write is ErrOutcomeUnknown — re-sent once
//     only under RetryIdempotent (see WithRetryPolicy);
//  4. a peer close repeated within theftWindow after reconnecting from one, while
//     the same editor still answers discovery, is theft: ErrChannelStolen, and the
//     session stops reconnecting until Reclaim.
func (s *Session) RunCommand(ctx context.Context, code string, mode ExecMode) (CommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bc == nil {
		return CommandResult{}, errors.New("uexec: session not started")
	}
	if s.stolen {
		// The slot was taken from THIS editor instance. If that node has left discovery
		// (the editor quit, crashed or was restarted), the theft no longer applies.
		if s.nodeListedLocked() {
			return CommandResult{}, ErrChannelStolen
		}
		s.logger.Info("stolen editor node is gone; resuming normal reconnects", "node_id", s.nodeID)
		s.stolen, s.peerReconnAt = false, time.Time{}
	}
	policy := retryPolicy(ctx)

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		// Case 0: replace a channel that died while idle before sending anything. A
		// desync (unsolicited bytes) is reconnected but is not a peer close.
		if s.cmd != nil && !s.cmd.tainted {
			switch s.cmd.probe(policy == RetryIdempotent) {
			case probeDead:
				s.cmd.taint()
				if err := s.notePeerCloseLocked(); err != nil {
					return CommandResult{}, err
				}
			case probeDesync:
				s.cmd.taint()
			}
		}
		if s.cmd == nil || s.cmd.tainted {
			if err := s.reconnectLocked(ctx); err != nil {
				if errors.Is(err, ErrEditorNotFound) {
					return CommandResult{}, err // not retried
				}
				lastErr = err
				continue
			}
		}
		res, err := s.cmd.runCommand(ctx, code, mode, s.cfg.CommandTimeout)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if s.cmd != nil && s.cmd.tainted {
			s.cmd = nil // force reconnect on any subsequent call
		}
		switch {
		case errors.Is(err, ErrOutcomeUnknown): // case 3 (+ case 4 check)
			if terr := s.notePeerCloseLocked(); terr != nil {
				return CommandResult{}, fmt.Errorf("%w: %w", terr, ErrOutcomeUnknown)
			}
			if policy == RetryIdempotent && attempt == 0 {
				s.logger.Warn("command connection lost after send; re-sending an idempotent command", "err", err)
				continue
			}
			return res, err
		case errors.Is(err, ErrConnectionLost) && attempt == 0: // case 2: the write failed
			s.logger.Warn("command write failed; reconnecting and re-sending", "err", err)
			continue
		}
		return res, err // case 1 (timeout/cancel) and everything else
	}
	return CommandResult{}, lastErr
}

// notePeerCloseLocked records a peer-initiated close. If it follows a reconnect that
// was itself caused by a peer close within theftWindow, and the same editor node is
// still answering discovery, the slot has been taken by another client: mark the
// session stolen (case 4).
func (s *Session) notePeerCloseLocked() error {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if !s.peerReconnAt.IsZero() && now.Sub(s.peerReconnAt) <= theftWindow && s.freshPongLocked(freshPongWait) {
		s.stolen = true
		s.logger.Warn("command channel stolen by another client; not reconnecting until reclaimed", "node_id", s.nodeID)
		return ErrChannelStolen
	}
	s.peerReconnAt = now // the reconnect that follows is "after a peer close"
	return nil
}

// freshPongWait bounds the theft check's discovery round-trip.
const freshPongWait = time.Second

// freshPongLocked pings and reports whether the SAME editor node answers within
// wait — a pong received after now, not a cached table entry (a node that just
// crashed stays listed until NodeTimeout).
func (s *Session) freshPongLocked(wait time.Duration) bool {
	if s.bc == nil || s.nodeID == "" {
		return false
	}
	since := time.Now()
	s.bc.sendPing()
	deadline := since.Add(wait)
	for time.Now().Before(deadline) {
		for _, n := range s.bc.nodes.list() {
			if n.ID == s.nodeID && n.LastPong.After(since) {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// nodeListedLocked reports whether the session's node is still in discovery.
func (s *Session) nodeListedLocked() bool {
	if s.bc == nil || s.nodeID == "" {
		return false
	}
	for _, n := range s.bc.nodes.list() {
		if n.ID == s.nodeID {
			return true
		}
	}
	return false
}

// Stolen reports whether the session gave up its channel to another client.
func (s *Session) Stolen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stolen
}

// Reclaim clears the stolen state so the next command reconnects (taking the slot
// back from whoever holds it). Explicit and logged by design — never automatic.
func (s *Session) Reclaim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stolen {
		s.logger.Warn("reclaiming the editor command channel", "node_id", s.nodeID)
	}
	s.stolen = false
	s.peerReconnAt = time.Time{}
}

func (s *Session) reconnectLocked(ctx context.Context) error {
	// Strict under a lease: a reconnect must NOT re-pin to a wrong-project node either.
	node, err := s.bc.waitForNode(ctx, s.cfg.ProjectDir, s.cfg.DiscoveryTimeout, s.cfg.StrictNode)
	if err != nil {
		return err
	}
	return s.openCommandLocked(ctx, node.ID)
}

// Close tears down the command channel and discovery loops.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		if s.bc != nil && s.nodeID != "" {
			s.bc.broadcastCloseConnection(s.nodeID)
		}
		s.cmd.close()
		s.cmd = nil
	}
	// A Session sharing a Discovery must NOT close the shared multicast socket/node
	// table — the daemon owns the Discovery's lifetime.
	if s.bc != nil && !s.sharedDisc {
		s.bc.close()
	}
	s.bc = nil
	return nil
}
