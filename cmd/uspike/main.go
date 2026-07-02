// Command uspike is the Phase-1 protocol go/no-go harness (GO_REWRITE_PLAN.md §8.2,
// §13). It exercises the ENTIRE remote-exec wire protocol against a live Unreal
// Editor before any MCP code exists:
//
//   - UDP multicast discovery on Windows loopback (Risk #1)
//   - project-aware node selection (logs the chosen node_id)
//   - TCP reverse-connect command channel
//   - engine version via EvaluateStatement
//   - the __main__-persistence probe that decides the default snippet mode
//
// Exits 0 only if all checks pass. Run it with the editor open:
//
//	uspike -project C:/Users/jorda/code/games/aesir-wave-defense
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/version"
)

func main() {
	cfg := uexec.DefaultConfig()
	var logLevel string
	flag.StringVar(&cfg.ProjectDir, "project", os.Getenv("UMCP_PROJECT_DIR"), "project dir for node selection")
	flag.StringVar(&cfg.MulticastGroup, "group", cfg.MulticastGroup, "multicast group endpoint")
	flag.StringVar(&cfg.BindAddress, "bind", cfg.BindAddress, "multicast bind address")
	flag.StringVar(&cfg.CommandAddr, "command-addr", cfg.CommandAddr, "TCP reverse-connect listener addr")
	flag.DurationVar(&cfg.DiscoveryTimeout, "discovery-timeout", cfg.DiscoveryTimeout, "node discovery timeout")
	flag.DurationVar(&cfg.CommandTimeout, "command-timeout", 30*time.Second, "per-command timeout")
	flag.StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")
	flag.Parse()

	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(logLevel))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	fmt.Fprintln(os.Stderr, version.String(), "— protocol spike (P1 gate)")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, cfg, logger); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAIL: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg uexec.Config, logger *slog.Logger) error {
	sess := uexec.New(cfg, logger)
	if err := sess.Start(ctx); err != nil {
		return fmt.Errorf("start discovery: %w", err)
	}
	defer sess.Close()

	fmt.Fprintln(os.Stderr, "\n[1/4] discovering editor nodes (UDP multicast on loopback)...")
	node, err := sess.WaitForNode(ctx)
	if err != nil {
		return fmt.Errorf("%w\n\nIs the Unreal Editor running with the project open, and is remote execution\nenabled (DefaultEngine.ini [/Script/PythonScriptPlugin.PythonScriptPluginSettings] bRemoteExecution=True)?", err)
	}
	for _, n := range sess.Nodes() {
		marker := "  "
		if n.ID == node.ID {
			marker = "->"
		}
		fmt.Fprintf(os.Stderr, "   %s node %s  project=%q engine=%q user=%q\n", marker, n.ID, n.ProjectRoot, n.EngineVersion, n.User)
	}

	fmt.Fprintln(os.Stderr, "\n[2/4] opening TCP reverse-connect command channel...")
	if err := sess.OpenCommand(ctx, node.ID); err != nil {
		return fmt.Errorf("open command channel: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\n[3/4] querying engine version (EvaluateStatement)...")
	ev, err := sess.RunCommand(ctx, "unreal.SystemLibrary.get_engine_version()", uexec.ModeEval)
	if err != nil {
		return fmt.Errorf("engine version command: %w", err)
	}
	if !ev.Success {
		return fmt.Errorf("engine version command returned success=false: %s", ev.Result)
	}
	fmt.Fprintf(os.Stderr, "   engine_version = %s\n", strings.TrimSpace(ev.Result))

	fmt.Fprintln(os.Stderr, "\n[4/4] __main__-persistence probe (decides default snippet mode)...")
	if _, err := sess.RunCommand(ctx, "_uspike_probe = 1", uexec.ModeExecFile); err != nil {
		return fmt.Errorf("persistence probe (set): %w", err)
	}
	pr, err := sess.RunCommand(ctx, "globals().get('_uspike_probe', 0)", uexec.ModeEval)
	if err != nil {
		return fmt.Errorf("persistence probe (read): %w", err)
	}
	persistent := strings.TrimSpace(pr.Result) == "1"
	mode := "ondisk"
	if persistent {
		mode = "hotload"
	}
	fmt.Fprintf(os.Stderr, "   __main__ persists across commands = %v  ->  default snippet mode = %s\n", persistent, mode)

	fmt.Fprintln(os.Stderr, "\nPASS: full remote-exec wire protocol validated against the live editor.")
	fmt.Fprintf(os.Stderr, "  node_id      = %s\n  engine       = %s\n  snippet_mode = %s\n",
		node.ID, strings.TrimSpace(ev.Result), mode)
	if !persistent {
		fmt.Fprintln(os.Stderr, "  NOTE: __main__ did not persist -> Phase 2 install.go should default to UMCP_SNIPPET_MODE=ondisk.")
	}
	return nil
}
