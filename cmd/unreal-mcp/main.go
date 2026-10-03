// Command unreal-mcp is the Go MCP server that exposes a live Unreal Editor
// session as tools. It speaks MCP over stdio (or StreamableHTTP in -daemon-addr
// mode) and drives the editor via the remote-exec protocol. See docs/plans/OVERHAUL_PLAN.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/jdziat/unreal-mcp-server/internal/app"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/cockpit/attach"
	"github.com/jdziat/unreal-mcp-server/internal/config"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/supervisor"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/version"
)

func main() {
	cfg := config.Load()
	if cfg.ShowVersion {
		fmt.Println(version.String())
		return
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// STDOUT PURITY: it carries the MCP JSON-RPC frame. All logging goes to
	// stderr, and the stdlib default logger is caged to stderr too.
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	log.SetOutput(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Multi-project daemon mode (MULTI_PROJECT_SYSTEM.md): one process, StreamableHTTP,
	// a 1:1 editor lease per agent session. Distinct from the single-project stdio path
	// below — it must NOT kill sibling processes (concurrent editors are the point).
	if cfg.DaemonAddr != "" {
		if err := runDaemon(ctx, cfg, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("daemon exited with error", "err", err)
			os.Exit(1)
		}
		return
	}

	// Reclaim the reverse-connect port from any orphaned sibling server (stale from a prior
	// session) before discovery starts. Only for the default port — a custom -command-addr means
	// a deliberate concurrent setup, which we must not disturb. See supervisor.KillOrphanSiblings.
	if cfg.CommandAddr == "127.0.0.1:6776" {
		supervisor.KillOrphanSiblings(logger)
	}

	// Discovery runs in the background; failing to start it is non-fatal so the
	// server still boots and lists tools (they error at call time until an
	// editor appears).
	sess := uexec.New(cfg.Uexec(), logger)
	if err := sess.Start(ctx); err != nil {
		logger.Warn("discovery failed to start; tools will error until an editor is reachable", "err", err)
	}
	defer sess.Close()

	if cfg.AutoRelaunch {
		go supervisor.RelaunchWatcher(ctx, sess, cfg, logger)
	}

	b := bridge.New(sess, bridge.Options{
		Mode:       bridge.SnippetMode(cfg.SnippetMode),
		ProjectDir: cfg.ProjectDir,
		Logger:     logger,
	})

	if cfg.SelfTest {
		os.Exit(runSelfTest(ctx, sess, b, logger))
	}

	// The cockpit (browser control+observability surface) opens itself once the editor's
	// native MCPCore channel is reachable; the launcher surfaces its URL via a log line, a
	// Saved/PyMCP/cockpit_url.txt file, and the cockpit_url tool.
	launcher := attach.NewLauncher()
	if cfg.Cockpit == "on" {
		go launcher.Run(ctx, b, attach.LaunchConfig{Project: cfg.ProjectDir, ProjectDir: cfg.ProjectDir}, logger)
	}

	st := session.NewState("stdio")
	defer st.Teardown()
	srv := app.NewServer(app.Options{
		Logger: logger,
		Deps: session.Deps{
			Bridge:     b,
			Jobs:       jobs.NewRegistry(),
			ProjectDir: cfg.ProjectDir,
			EngineDir:  cfg.EngineDir,
			CockpitURL: launcher.URL,
		},
	}, st).MCP

	logger.Info("unreal-mcp starting",
		"version", version.String(), "snippet_mode", cfg.SnippetMode,
		"project", cfg.ProjectDir, "command_addr", cfg.CommandAddr)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

// runSelfTest validates the full path against a live editor and returns a process
// exit code (0 = healthy): discovery + node selection (logs every node and the
// chosen one), the __main__-persistence probe that decides the snippet mode, and an
// editor_status round-trip through the companion module. Doubles as an unattended
// health check. (Absorbs the retired cmd/uspike protocol spike.)
func runSelfTest(ctx context.Context, sess *uexec.Session, b *bridge.Bridge, logger *slog.Logger) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fail := func(stage string, err error) int {
		fmt.Fprintf(os.Stderr, "selftest FAIL (%s): %v\n", stage, err)
		return 1
	}

	node, err := sess.WaitForNode(ctx)
	if err != nil {
		return fail("discovery", fmt.Errorf("%w (is the editor running with remote execution enabled?)", err))
	}
	for _, n := range sess.Nodes() {
		marker := "  "
		if n.ID == node.ID {
			marker = "->"
		}
		fmt.Fprintf(os.Stderr, "  %s node %s project=%q engine=%q\n", marker, n.ID, n.ProjectRoot, n.EngineVersion)
	}

	if _, err := b.RunPython(ctx, "_umcp_selftest_probe = 1", uexec.ModeExecFile); err != nil {
		return fail("persistence probe", err)
	}
	v, err := b.Eval(ctx, "globals().get('_umcp_selftest_probe', 0)")
	if err != nil {
		return fail("persistence probe", err)
	}
	persistent := strings.TrimSpace(v) == "1"
	_, _ = b.RunPython(ctx, "globals().pop('_umcp_selftest_probe', None)", uexec.ModeExecFile)
	fmt.Fprintf(os.Stderr, "  __main__ persists across commands = %v (hotload snippet mode %s)\n",
		persistent, map[bool]string{true: "OK", false: "UNAVAILABLE — use -snippet-mode ondisk"}[persistent])

	raw, err := b.Call(ctx, "editor_status", map[string]any{})
	if err != nil {
		return fail("editor_status", err)
	}
	fmt.Fprintf(os.Stderr, "selftest OK: %s\n", string(raw))
	return 0
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
