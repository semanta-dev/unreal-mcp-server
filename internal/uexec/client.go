package uexec

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
}

// New creates a self-contained Session (owns its own discovery). logger may be nil.
func New(cfg Config, logger *slog.Logger) *Session {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Session{cfg: cfg.withDefaults(), self: newUUID(), logger: logger}
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
		bc: disc.bc, sharedDisc: true,
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
	cmd := &commandConn{cfg: s.cfg, self: s.self, remote: nodeID, bc: s.bc, logger: s.logger}
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
// Single-flight (holds the session lock for the whole call). Reconnects once on
// a lost connection; does NOT auto-retry timeouts (a side-effecting command must
// not be silently re-run) or missing-editor errors.
func (s *Session) RunCommand(ctx context.Context, code string, mode ExecMode) (CommandResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bc == nil {
		return CommandResult{}, errors.New("uexec: session not started")
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
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
		// Only a lost connection is safe to transparently retry once.
		if errors.Is(err, ErrConnectionLost) && attempt == 0 {
			s.logger.Warn("command connection lost; reconnecting once", "err", err)
			continue
		}
		return res, err
	}
	return CommandResult{}, lastErr
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
