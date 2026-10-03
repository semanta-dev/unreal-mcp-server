package supervisor

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Reattach persistence + boot reconciliation (MULTI_PROJECT_SYSTEM.md §6). Every
// spawned editor's correlation record — {token, project, pid, identity} — is written
// to disk so that after a daemon CRASH the successor can guarantee §6's invariant: no
// daemon-owned editor outlives the daemon. Ground truth on boot is the persisted
// records ∪ the live process table (UnrealEditor processes bearing a -MCPInstanceToken
// this daemon minted, found by reading each process's command line). Enumerating the
// process table — not just trusting the persisted pid — closes the Launch→persist
// window (an editor already running but whose pid wasn't recorded yet) and is immune to
// pid recycling (a recycled pid won't bear the token).
//
// NOTE: policy is the SAFE kill-all-on-boot — re-ADOPTING a still-warm editor across a
// restart is deferred (needs the editor to advertise its instance token on discovery
// for unambiguous node matching). Also: editors RELAUNCHED by the legacy full-rebuild /
// editor_restart / project_ensure_open tools (build_tools.go) are launched WITHOUT a
// token, so they are not reattach-tracked — a known gap pending §3.1 controlled-restart
// being routed through the daemon pool.

type ReattachRecord struct {
	Token    string `json:"token"`
	Project  string `json:"project"`
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

// RecordStore is the serialized, crash-safe on-disk record set. All mutations take mu
// so a prune can't race a concurrent spawn's write; writes are atomic (temp+rename).
type RecordStore struct {
	dir    string
	mu     sync.Mutex
	logger *slog.Logger
}

func NewRecordStore(dir string, logger *slog.Logger) *RecordStore {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &RecordStore{dir: dir, logger: logger}
}

func (rs *RecordStore) path(token string) string { return filepath.Join(rs.dir, token+".json") }

// write persists (or upgrades) a record atomically: write a temp file then rename over
// the target, so a crash mid-write never leaves a torn/corrupt record.
func (rs *RecordStore) Write(rec ReattachRecord) error {
	if rs.dir == "" || rec.Token == "" {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if err := os.MkdirAll(rs.dir, 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(rec)
	tmp := rs.path(rec.Token) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, rs.path(rec.Token)) // atomic replace on the same volume
}

func (rs *RecordStore) Remove(token string) {
	if rs.dir == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	_ = os.Remove(rs.path(token))
}

func (rs *RecordStore) Read() []ReattachRecord {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.readLocked()
}

func (rs *RecordStore) readLocked() []ReattachRecord {
	if rs.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(rs.dir)
	if err != nil {
		return nil
	}
	var recs []ReattachRecord
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rs.dir, name))
		if err != nil {
			continue
		}
		var r ReattachRecord
		if json.Unmarshal(data, &r) != nil || r.Token == "" {
			rs.logger.Warn("removing corrupt reattach record", "file", name)
			_ = os.Remove(filepath.Join(rs.dir, name)) // don't let a bad file linger forever
			continue
		}
		recs = append(recs, r)
	}
	return recs
}

// pruneDead removes records whose editor is confirmed gone (pid dead / identity
// mismatch). Under the lock so it can't delete a record a concurrent spawn is
// upgrading. A pre-Launch intent (pid<=0) is kept unless its file is older than
// staleAfter (a spawn that never completed).
func (rs *RecordStore) PruneDead(isAlive func(int) bool, identity func(int) string, staleAfter time.Duration, now time.Time) {
	if rs.dir == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	entries, err := os.ReadDir(rs.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		p := filepath.Join(rs.dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r ReattachRecord
		if json.Unmarshal(data, &r) != nil || r.Token == "" {
			continue
		}
		dead := false
		if r.PID > 0 {
			dead = !isAlive(r.PID) || (r.Identity != "" && identity(r.PID) != r.Identity)
		} else if info, e2 := e.Info(); e2 == nil && now.Sub(info.ModTime()) > staleAfter {
			dead = true // pre-Launch intent that never upgraded => failed spawn
		}
		if dead {
			_ = os.Remove(p)
		}
	}
}

// InstanceTokenFlag is the editor command-line flag carrying a daemon-minted instance token.
const InstanceTokenFlag = "-MCPInstanceToken"

// KillConfirmed POSITIVELY re-verifies the pid bears MY token immediately before the
// kill (so a pid recycled since enumeration — to a token-less OR a different process —
// is never killed), then kills and polls until death is confirmed. Returns false if
// the pid no longer positively bears my token (nothing to do) or won't die (caller
// keeps the record for a retry). The false direction is always safe.
func KillConfirmed(pid int, token string, verify func(int) string, isAlive func(int) bool, kill func(int) error) bool {
	if pid <= 0 || verify(pid) != token {
		return false
	}
	_ = kill(pid)
	for i := 0; i < 20; i++ {
		if !isAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !isAlive(pid)
}
