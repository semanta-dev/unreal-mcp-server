// Package config resolves server configuration from flags > env > defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// Config is the resolved server configuration.
type Config struct {
	MulticastGroup   string
	BindAddr         string
	CommandAddr      string
	DiscoveryTimeout time.Duration
	CommandTimeout   time.Duration
	ProjectDir       string
	EngineDir        string
	SnippetMode      string
	AutoRelaunch     bool
	LogLevel         string
	LogFormat        string
	ShowVersion      bool
	SelfTest         bool
	DaemonAddr       string        // if set, run the multi-project daemon (StreamableHTTP) on this addr
	SessionIdle      time.Duration // daemon: end a session after this long without activity
	Cockpit          string        // "off" | "on": attach the MCPCore cockpit (one Go peer per editor)
	Toolsets         string        // comma-separated toolsets enabled at session start (besides core)
}

func boolEnv(key string) bool {
	v := os.Getenv(key)
	return v == "1" || v == "true" || v == "yes"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func durOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Load parses the process flags (with env-var defaults) and returns the config.
// Call it once from main.
func Load() Config {
	c, _ := LoadFrom(flag.CommandLine, os.Args[1:])
	return c
}

// LoadFrom parses args on fs (with env-var defaults) and returns the config.
func LoadFrom(fs *flag.FlagSet, args []string) (Config, error) {
	d := uexec.DefaultConfig()
	var c Config
	fs.StringVar(&c.MulticastGroup, "group", envOr("UMCP_MULTICAST_GROUP", d.MulticastGroup), "multicast discovery group endpoint")
	fs.StringVar(&c.BindAddr, "bind", envOr("UMCP_BIND_ADDR", d.BindAddress), "multicast bind/interface address")
	fs.StringVar(&c.CommandAddr, "command-addr", envOr("UMCP_COMMAND_ADDR", d.CommandAddr), "TCP reverse-connect listener addr (unique per concurrent client)")
	fs.DurationVar(&c.DiscoveryTimeout, "discovery-timeout", durOr("UMCP_DISCOVERY_TIMEOUT", d.DiscoveryTimeout), "editor discovery timeout")
	fs.DurationVar(&c.CommandTimeout, "command-timeout", durOr("UMCP_COMMAND_TIMEOUT", d.CommandTimeout), "per-command timeout (0 = block forever)")
	fs.StringVar(&c.ProjectDir, "project", envOr("UMCP_PROJECT_DIR", ""), "project dir (node selection, screenshots/logs/git roots)")
	fs.StringVar(&c.EngineDir, "engine", envOr("UMCP_ENGINE_DIR", ""), "engine dir (Build.bat / editor launch); default: UE_ENGINE_DIR, else the Epic launcher install matching the project")
	fs.StringVar(&c.SnippetMode, "snippet-mode", envOr("UMCP_SNIPPET_MODE", "hotload"), "companion module delivery: hotload|ondisk")
	fs.BoolVar(&c.AutoRelaunch, "auto-relaunch", boolEnv("UMCP_AUTO_RELAUNCH"), "relaunch the editor if it disappears (unattended runs; needs -project + -engine)")
	fs.StringVar(&c.LogLevel, "log-level", envOr("UMCP_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&c.LogFormat, "log-format", envOr("UMCP_LOG_FORMAT", "json"), "log format: json|text")
	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&c.SelfTest, "selftest", false, "connect to the editor, round-trip editor_status, and exit (0 ok, non-zero on failure)")
	fs.StringVar(&c.DaemonAddr, "daemon-addr", envOr("UMCP_DAEMON_ADDR", ""), "run the multi-project daemon (StreamableHTTP, one editor lease per session) on this addr, e.g. 127.0.0.1:6111")
	fs.StringVar(&c.Cockpit, "cockpit", envOr("UMCP_COCKPIT", "off"), "attach the MCPCore cockpit (native channel + browser page): off|on. The plugin accepts ONE Go peer per editor")
	fs.StringVar(&c.Toolsets, "toolsets", envOr("UMCP_TOOLSETS", ""), "comma-separated toolsets enabled at session start, e.g. design,ui (adds to the project's .umcp.json toolsets)")
	fs.DurationVar(&c.SessionIdle, "session-idle", 30*time.Minute, "daemon: end an MCP session after this long without activity (its lease drains while a project job runs)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.EngineDir == "" {
		c.EngineDir = DiscoverEngine(c.ProjectDir)
	}
	return c, nil
}

// Validate rejects inconsistent configuration at startup instead of failing later
// inside a tool call.
func (c Config) Validate() error {
	var errs []string
	switch c.SnippetMode {
	case "hotload", "ondisk":
	default:
		errs = append(errs, fmt.Sprintf("-snippet-mode must be hotload or ondisk (got %q)", c.SnippetMode))
	}
	if c.SnippetMode == "ondisk" && c.ProjectDir == "" {
		errs = append(errs, "-snippet-mode ondisk requires -project")
	}
	if c.AutoRelaunch && (c.ProjectDir == "" || c.EngineDir == "") {
		errs = append(errs, "-auto-relaunch requires -project and an engine dir (-engine, UMCP_ENGINE_DIR, UE_ENGINE_DIR or an Epic launcher install)")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("-log-level must be debug|info|warn|error (got %q)", c.LogLevel))
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Sprintf("-log-format must be json|text (got %q)", c.LogFormat))
	}
	if c.CommandTimeout < 0 || c.DiscoveryTimeout <= 0 {
		errs = append(errs, "-command-timeout must be >= 0 and -discovery-timeout > 0")
	}
	switch c.Cockpit {
	case "off", "on":
	default:
		errs = append(errs, fmt.Sprintf("-cockpit must be off|on (got %q)", c.Cockpit))
	}
	if c.DaemonAddr != "" && c.SessionIdle <= 0 {
		errs = append(errs, "-session-idle must be > 0 in daemon mode")
	}
	if len(errs) > 0 {
		return errors.New("invalid configuration:\n  " + strings.Join(errs, "\n  "))
	}
	return nil
}

// Uexec builds the protocol client config.
func (c Config) Uexec() uexec.Config {
	u := uexec.DefaultConfig()
	u.MulticastGroup = c.MulticastGroup
	u.BindAddress = c.BindAddr
	u.CommandAddr = c.CommandAddr
	u.DiscoveryTimeout = c.DiscoveryTimeout
	u.CommandTimeout = c.CommandTimeout
	u.ProjectDir = c.ProjectDir
	return u
}
