package cockpitbridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/cockpit"
)

// Selectable is the slice of *bridge.Bridge that Bootstrap needs: probe an op over uexec
// and select the native backend. Kept as an interface so Bootstrap is unit-testable
// without a live editor.
type Selectable interface {
	Call(ctx context.Context, op string, args any) (json.RawMessage, error)
	SetNative(bridge.NativeDispatcher)
}

// EpochStore remembers the plugin-minted session_epoch (and the resume seq) per project,
// so a reconnect against an UNCHANGED epoch replays only the gap, while a CHANGED epoch
// signals an editor/MCPCore reset (§2.5 step 3). A nil store means no resume (start fresh).
type EpochStore interface {
	// LastSeq returns the observation-seq resume point for (project, epoch), or 0 if the
	// epoch changed or is unknown (→ full re-sync).
	LastSeq(project, epoch string) uint64
	// Record notes the current epoch for the project (called after a successful attach).
	Record(project, epoch string)
}

// BootstrapConfig parametrizes Bootstrap.
type BootstrapConfig struct {
	Project      string        // project key for the epoch store
	CockpitToken string        // browser-facing bearer for the cockpit Session
	DialTimeout  time.Duration // editor dial/handshake timeout
	Epochs       EpochStore    // optional resume store
}

// Bootstrap implements the §2.5 backend-selection state machine (steps 2–8): it probes
// cockpit_info over the existing uexec channel, and
//   - if MCPCore is absent (the not_present sentinel) → returns (nil, nil): the session
//     stays on the uexec/Python fallback (one-way, per §5.4);
//   - if present → epoch-compares, dials the framed channel, opens a cockpit Session, and
//     selects the native backend on the Bridge so ops route over the socket.
//
// A uexec transport error (the probe itself failing) is returned so the caller can retry.
func Bootstrap(ctx context.Context, b Selectable, cfg BootstrapConfig) (*cockpit.Session, error) {
	raw, err := b.Call(ctx, "cockpit_info", nil)
	if err != nil {
		return nil, err // uexec broken — surface, don't silently claim not-present
	}
	info := cockpit.ParseCockpitInfo(raw)
	if !info.Present {
		return nil, nil // native module absent → stay on uexec
	}

	var lastSeq uint64
	if cfg.Epochs != nil {
		lastSeq = cfg.Epochs.LastSeq(cfg.Project, info.SessionEpoch)
	}
	sess, err := cockpit.OpenSession(ctx, cockpit.SessionConfig{
		EditorAddr:   info.EditorAddr(),
		EditorToken:  info.Token,
		KnownEpoch:   info.SessionEpoch,
		LastSeenSeq:  lastSeq,
		CockpitToken: cfg.CockpitToken,
		DialTimeout:  cfg.DialTimeout,
	})
	if err != nil {
		return nil, err
	}
	// Route op dispatch through the framed socket; uexec remains the install/fallback.
	b.SetNative(New(sess.Client()))
	if cfg.Epochs != nil {
		cfg.Epochs.Record(cfg.Project, info.SessionEpoch)
	}
	return sess, nil
}

// MemEpochStore is a simple in-memory EpochStore (per-daemon). Resume seq tracking is
// left to the caller updating LastSeq via the Session; here it only tracks the epoch so a
// change is detectable.
type MemEpochStore struct {
	epochs map[string]string
}

// NewMemEpochStore builds an empty in-memory epoch store.
func NewMemEpochStore() *MemEpochStore { return &MemEpochStore{epochs: map[string]string{}} }

// LastSeq returns 0 (no persisted seq) but is where resume would read once wired.
func (m *MemEpochStore) LastSeq(project, epoch string) uint64 {
	if m.epochs[project] == epoch {
		return 0 // same epoch, no persisted gap yet
	}
	return 0
}

// EpochChanged reports whether the recorded epoch differs from the given one.
func (m *MemEpochStore) EpochChanged(project, epoch string) bool {
	prev, ok := m.epochs[project]
	return ok && prev != epoch
}

// Record stores the current epoch for the project.
func (m *MemEpochStore) Record(project, epoch string) { m.epochs[project] = epoch }
