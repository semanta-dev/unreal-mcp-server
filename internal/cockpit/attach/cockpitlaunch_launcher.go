// cockpitlaunch (merged into package attach) wires the cockpit into the running server: once the editor's
// native MCPCore channel is reachable, it bootstraps a cockpit Session, selects the native
// backend, and surfaces the browser URL three ways (server log, a file the user can open,
// and the cockpit_url MCP tool). It re-bootstraps on disconnect. Without this, the
// Session/Host components exist but nothing ever opens the cockpit.
package attach

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/cockpit"
)

// LaunchConfig parametrizes the launcher.
type LaunchConfig struct {
	Project     string // project key (for the epoch store)
	ProjectDir  string // where to drop the cockpit_url.txt pointer
	GateTimeout time.Duration
	RetryEvery  time.Duration // backoff between bootstrap attempts (default 3s)
}

// Launcher holds the current cockpit session + URL and (re)opens it as editors come and go.
type Launcher struct {
	mu    sync.Mutex
	url   string
	ready bool
	sess  *cockpit.Session
}

// NewLauncher builds an idle launcher; call Run in a goroutine.
func NewLauncher() *Launcher { return &Launcher{} }

// URL returns the current cockpit URL and whether it is live. Safe for the cockpit_url tool.
func (l *Launcher) URL() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url, l.ready
}

func (l *Launcher) set(sess *cockpit.Session, url string, ready bool) {
	l.mu.Lock()
	l.sess, l.url, l.ready = sess, url, ready
	l.mu.Unlock()
}

// Run bootstraps the cockpit whenever the editor's native channel is reachable, surfaces
// the URL, waits for the session to drop, and repeats. Returns when ctx is cancelled.
func (l *Launcher) Run(ctx context.Context, b *bridge.Bridge, cfg LaunchConfig, logger *slog.Logger) {
	if cfg.RetryEvery <= 0 {
		cfg.RetryEvery = 3 * time.Second
	}
	epochs := NewMemEpochStore()
	retry := cfg.RetryEvery
	var absentGen uint64 // channel generation on which MCPCore was found absent (0 = none)
	for ctx.Err() == nil {
		// MCPCore absence is a property of the running editor: don't re-probe the
		// game thread every few seconds — only after the command channel changes
		// (reconnect / editor restart), or at the backoff cap.
		if !shouldProbe(absentGen, b.Generation()) {
			if sleep(ctx, maxRetry) {
				return
			}
			absentGen = 0 // re-probe once per cap interval as a backstop
			continue
		}
		sess, err := Bootstrap(ctx, b, BootstrapConfig{
			Project:      cfg.Project,
			CockpitToken: randToken(),
			DialTimeout:  5 * time.Second,
			Epochs:       epochs,
		})
		if err != nil || sess == nil {
			if err != nil { // editor unreachable yet → back off
				logger.Debug("cockpit bootstrap retrying", "err", err, "in", retry.String())
			} else { // MCPCore not loaded → uexec fallback; remember for this channel
				absentGen = b.Generation()
			}
			if sleep(ctx, retry) {
				return
			}
			retry = nextBackoff(retry)
			continue
		}
		retry = cfg.RetryEvery
		url := sess.CockpitURL()
		l.set(sess, url, true)
		writeURLFile(cfg.ProjectDir, url, logger)
		logger.Info("==== MCP COCKPIT READY — open this in a browser ====", "url", url)

		select {
		case <-ctx.Done():
			sess.Close()
			return
		case <-sess.Done(): // editor dropped → clear + re-bootstrap (resuming the event seq)
			epochs.Advance(cfg.Project, sess.EditorEpoch(), sess.Client().LastSeq())
			l.set(nil, "", false)
			logger.Info("cockpit session ended; will re-open when the editor is reachable")
		}
	}
}

// maxRetry caps the bootstrap backoff.
const maxRetry = 60 * time.Second

// nextBackoff doubles the retry interval up to maxRetry.
func nextBackoff(d time.Duration) time.Duration { return min(2*d, maxRetry) }

// shouldProbe reports whether to probe for MCPCore: always, unless it was found
// absent on the current command-channel generation.
func shouldProbe(absentGen, gen uint64) bool { return absentGen == 0 || absentGen != gen }

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeURLFile drops the URL where a human can find it even without the log.
func writeURLFile(projectDir, url string, logger *slog.Logger) {
	if projectDir == "" {
		return
	}
	dir := filepath.Join(projectDir, "Saved", "PyMCP")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	p := filepath.Join(dir, "cockpit_url.txt")
	if err := os.WriteFile(p, []byte(url+"\n"), 0o644); err != nil {
		logger.Debug("could not write cockpit_url.txt", "err", err)
	}
}

func sleep(ctx context.Context, d time.Duration) (cancelled bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
