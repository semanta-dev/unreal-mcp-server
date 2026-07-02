// Command unreal-mcp is the Go MCP server that exposes a live Unreal Editor
// session as tools (replacement for the Python server.py). It speaks MCP over
// stdio and drives the editor via the remote-exec protocol. See GO_REWRITE_PLAN.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/config"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/version"
)

func main() {
	cfg := config.Load()
	if cfg.ShowVersion {
		fmt.Println(version.String())
		return
	}

	// STDOUT PURITY: it carries the MCP JSON-RPC frame. All logging goes to
	// stderr, and the stdlib default logger is caged to stderr too.
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	log.SetOutput(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Discovery runs in the background; failing to start it is non-fatal so the
	// server still boots and lists tools (they error at call time until an
	// editor appears).
	sess := uexec.New(cfg.Uexec(), logger)
	if err := sess.Start(ctx); err != nil {
		logger.Warn("discovery failed to start; tools will error until an editor is reachable", "err", err)
	}
	defer sess.Close()

	if cfg.AutoRelaunch {
		go relaunchWatcher(ctx, sess, cfg, logger)
	}

	b := bridge.New(sess, bridge.Options{
		Mode:       bridge.SnippetMode(cfg.SnippetMode),
		ProjectDir: cfg.ProjectDir,
		Logger:     logger,
	})

	if cfg.SelfTest {
		os.Exit(runSelfTest(ctx, b, logger))
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: version.Version}, &mcp.ServerOptions{Logger: logger})
	tools.InstallMiddleware(srv, logger)
	tools.RegisterAll(srv, tools.Deps{
		Bridge:     b,
		Jobs:       jobs.NewRegistry(),
		ProjectDir: cfg.ProjectDir,
		EngineDir:  cfg.EngineDir,
	})

	logger.Info("unreal-mcp starting",
		"version", version.String(), "snippet_mode", cfg.SnippetMode,
		"project", cfg.ProjectDir, "command_addr", cfg.CommandAddr)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

// runSelfTest connects to a live editor, round-trips editor_status, and returns
// a process exit code (0 = healthy). Doubles as an unattended health check.
func runSelfTest(ctx context.Context, b *bridge.Bridge, logger *slog.Logger) int {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := b.Call(ctx, "editor_status", map[string]any{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "selftest FAIL: %v\n", err)
		return 1
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
