// Package config resolves server configuration from flags > env > defaults.
package config

import (
	"flag"
	"os"
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

// Load parses flags (with env-var defaults) and returns the config. It uses the
// default flag set, so call it once from main.
func Load() Config {
	d := uexec.DefaultConfig()
	var c Config
	flag.StringVar(&c.MulticastGroup, "group", envOr("UMCP_MULTICAST_GROUP", d.MulticastGroup), "multicast discovery group endpoint")
	flag.StringVar(&c.BindAddr, "bind", envOr("UMCP_BIND_ADDR", d.BindAddress), "multicast bind/interface address")
	flag.StringVar(&c.CommandAddr, "command-addr", envOr("UMCP_COMMAND_ADDR", d.CommandAddr), "TCP reverse-connect listener addr (unique per concurrent client)")
	flag.DurationVar(&c.DiscoveryTimeout, "discovery-timeout", durOr("UMCP_DISCOVERY_TIMEOUT", d.DiscoveryTimeout), "editor discovery timeout")
	flag.DurationVar(&c.CommandTimeout, "command-timeout", durOr("UMCP_COMMAND_TIMEOUT", d.CommandTimeout), "per-command timeout (0 = block forever)")
	flag.StringVar(&c.ProjectDir, "project", envOr("UMCP_PROJECT_DIR", ""), "project dir (node selection, screenshots/logs/git roots)")
	flag.StringVar(&c.EngineDir, "engine", envOr("UMCP_ENGINE_DIR", `D:/Unreal/Engine/UE_5.7`), "engine dir (Build.bat / editor launch)")
	flag.StringVar(&c.SnippetMode, "snippet-mode", envOr("UMCP_SNIPPET_MODE", "hotload"), "companion module delivery: hotload|ondisk")
	flag.BoolVar(&c.AutoRelaunch, "auto-relaunch", boolEnv("UMCP_AUTO_RELAUNCH"), "relaunch the editor if it disappears (unattended runs; needs -project + -engine)")
	flag.StringVar(&c.LogLevel, "log-level", envOr("UMCP_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	flag.StringVar(&c.LogFormat, "log-format", envOr("UMCP_LOG_FORMAT", "json"), "log format: json|text")
	flag.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	flag.BoolVar(&c.SelfTest, "selftest", false, "connect to the editor, round-trip editor_status, and exit (0 ok, non-zero on failure)")
	flag.Parse()
	return c
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
