// Package daemonwire is the production wiring that connects the daemon CORE
// (internal/daemon, internal/supervisor, internal/uexec) to real OS editors
// (internal/lifecycle) and per-instance command bridges (internal/bridge). It
// implements Spawner (editor bring-up + kill) and the Editor that
// wraps a per-instance uexec.Session + bridge.Bridge. Kept separate from the daemon
// package so that package stays unit-testable with fakes.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// EditorHandle is the production Editor: a per-instance command bridge over a
// uexec.Session on the shared discovery. Bridge() exposes the *bridge.Bridge the tool
// resolver routes to; Close tears down only this session's command channel (never the
// shared discovery).
type EditorHandle struct {
	sess *uexec.Session
	br   *bridge.Bridge
}

func (e *EditorHandle) Close() error           { return e.sess.Close() }
func (e *EditorHandle) Bridge() *bridge.Bridge { return e.br }

var _ Editor = (*EditorHandle)(nil)

// Spawner brings up editors for the daemon. It writes a write-ahead intent (§6),
// launches the editor with its correlation token + an ephemeral reverse-connect
// port, opens a per-instance session on the SHARED discovery, and waits until the
// editor is accepting before returning a ready bridge.
type ProcessSpawner struct {
	Disc       *uexec.Discovery
	Base       uexec.Config // multicast group etc. (CommandAddr/ProjectDir set per-instance)
	EngineDir  string
	Records    *RecordStore // shared crash-safe reattach store
	BridgeMode bridge.SnippetMode
	Logger     *slog.Logger
	// AcceptTimeout bounds the accepting-wait (cold start 10-60s + load). 0 => 300s.
	AcceptTimeout time.Duration
}

// Spawn launches an editor for project and returns a ready bridge + its pid +
// process identity. Satisfies Spawner.
func (s *ProcessSpawner) Spawn(ctx context.Context, project, token string) (Editor, int, string, error) {
	// Write-ahead intent (§6): persist token+project BEFORE Launch so a crash during
	// bring-up still leaves a reconcilable record (enumeration finds it by token).
	_ = s.Records.Write(ReattachRecord{Token: token, Project: project})
	uproj := lifecycle.FindUproject(project)
	if uproj == "" {
		s.Records.Remove(token)
		return nil, 0, "", fmt.Errorf("no .uproject under %s", project)
	}
	// The token is baked into the launch args so process-enumeration reattach can
	// identify this editor as daemon-owned before it advertises on discovery.
	pid, err := lifecycle.Launch(s.EngineDir, uproj, InstanceTokenFlag+"="+token)
	if err != nil {
		s.Records.Remove(token)
		return nil, 0, "", fmt.Errorf("launch editor: %w", err)
	}
	identity := lifecycle.ProcessIdentity(pid)
	// Upgrade the intent to a full record now that pid + identity are known.
	_ = s.Records.Write(ReattachRecord{Token: token, Project: project, PID: pid, Identity: identity})

	cfg := s.Base
	cfg.ProjectDir = project
	cfg.CommandAddr = "127.0.0.1:0" // ephemeral per-instance reverse-connect port (§1)
	cfg.StrictNode = true           // under lease: never bind a foreign tenant's editor (§3)
	sess := uexec.NewOnDiscovery(cfg, s.Disc, s.Logger)
	br := bridge.New(sess, bridge.Options{ProjectDir: project, Mode: s.BridgeMode, Logger: s.Logger})

	if err := s.waitAccepting(ctx, sess, br, pid); err != nil {
		_ = sess.Close()
		_ = lifecycle.Kill(pid) // kill-before-giving-up so we don't leak a wedged editor
		return nil, 0, "", err
	}
	// Editor is up: intent is upgraded to a full record by the caller (pool.Register).
	return &EditorHandle{sess: sess, br: br}, pid, identity, nil
}

// waitAccepting polls discovery + command-channel readiness WHILE the launched pid
// is alive, up to a cold-start-sized deadline. Terminal only on confirmed process
// death or the deadline (§3.1 re-pin: PID-death, not a fixed clock, is the primary
// signal).
func (s *ProcessSpawner) waitAccepting(ctx context.Context, sess *uexec.Session, br *bridge.Bridge, pid int) error {
	deadline := s.AcceptTimeout
	if deadline <= 0 {
		deadline = 300 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		if !lifecycle.IsAlive(pid) {
			return fmt.Errorf("editor pid %d died during startup", pid)
		}
		node, err := sess.WaitForNode(dctx)
		if err == nil && node != nil {
			if oerr := sess.OpenCommand(dctx, node.ID); oerr == nil {
				// Probe the command channel is actually accepting.
				if _, perr := br.Call(dctx, "editor_status", map[string]any{}); perr == nil {
					return nil
				}
			}
		}
		select {
		case <-dctx.Done():
			return fmt.Errorf("editor not accepting within %s (pid %d alive=%v)", deadline, pid, lifecycle.IsAlive(pid))
		case <-time.After(3 * time.Second):
		}
	}
}

// Kill force-kills a pid (kill-before-teardown). Satisfies Spawner.
func (s *ProcessSpawner) Kill(pid int) error { return lifecycle.Kill(pid) }

var _ Spawner = (*ProcessSpawner)(nil)
