package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func valid() Config {
	return Config{SnippetMode: "hotload", LogLevel: "info", LogFormat: "json", DiscoveryTimeout: time.Second, SessionIdle: time.Minute, Cockpit: "off"}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]func(*Config){
		"snippet-mode":    func(c *Config) { c.SnippetMode = "magic" },
		"ondisk":          func(c *Config) { c.SnippetMode = "ondisk" },
		"auto-relaunch":   func(c *Config) { c.AutoRelaunch = true },
		"log-level":       func(c *Config) { c.LogLevel = "loud" },
		"log-format":      func(c *Config) { c.LogFormat = "xml" },
		"command-timeout": func(c *Config) { c.CommandTimeout = -1 },
		"session-idle":    func(c *Config) { c.DaemonAddr = "127.0.0.1:1"; c.SessionIdle = 0 },
		"cockpit":         func(c *Config) { c.Cockpit = "maybe" },
	}
	for want, mut := range cases {
		c := valid()
		mut(&c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want an error mentioning it, got %v", want, err)
		}
	}
}

func TestDiscoverEngine(t *testing.T) {
	dir := t.TempDir()
	dat := filepath.Join(dir, "LauncherInstalled.dat")
	os.WriteFile(dat, []byte(`{"InstallationList":[
		{"InstallLocation":"C:/UE_5.6","AppName":"UE_5.6"},
		{"InstallLocation":"C:/UE_5.10","AppName":"UE_5.10"},
		{"InstallLocation":"C:/Other","AppName":"SomeGame"}]}`), 0o644)
	old := launcherInstalledPath
	launcherInstalledPath = func() string { return dat }
	defer func() { launcherInstalledPath = old }()
	t.Setenv("UE_ENGINE_DIR", "")

	if got := DiscoverEngine(""); got != filepath.Join("C:/UE_5.10", "Engine") {
		t.Fatalf("newest engine expected (numeric compare), got %q", got)
	}
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, "Game.uproject"), []byte(`{"EngineAssociation":"5.6"}`), 0o644)
	if got := DiscoverEngine(proj); got != filepath.Join("C:/UE_5.6", "Engine") {
		t.Fatalf("project's EngineAssociation must win, got %q", got)
	}
	t.Setenv("UE_ENGINE_DIR", "D:/Custom")
	if got := DiscoverEngine(proj); got != "D:/Custom" {
		t.Fatalf("UE_ENGINE_DIR must win, got %q", got)
	}
	launcherInstalledPath = func() string { return filepath.Join(dir, "missing.dat") }
	t.Setenv("UE_ENGINE_DIR", "")
	if got := DiscoverEngine(proj); got != "" {
		t.Fatalf("nothing installed → empty, got %q", got)
	}
}

func TestLoadFromFlagsEnvAndUexec(t *testing.T) {
	t.Setenv("UMCP_PROJECT_DIR", "C:/proj")
	t.Setenv("UMCP_COMMAND_TIMEOUT", "7s")
	t.Setenv("UMCP_AUTO_RELAUNCH", "true")
	t.Setenv("UMCP_ENGINE_DIR", "D:/UE")
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c, err := LoadFrom(fs, []string{"-snippet-mode", "ondisk", "-session-idle", "5m", "-log-format", "text"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectDir != "C:/proj" || c.CommandTimeout != 7*time.Second || !c.AutoRelaunch || c.EngineDir != "D:/UE" {
		t.Fatalf("env defaults not applied: %+v", c)
	}
	if c.SnippetMode != "ondisk" || c.SessionIdle != 5*time.Minute || c.LogFormat != "text" {
		t.Fatalf("flags not applied: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("loaded config should validate: %v", err)
	}
	u := c.Uexec()
	if u.ProjectDir != "C:/proj" || u.CommandTimeout != 7*time.Second || u.CommandAddr == "" || !u.StrictNode {
		t.Fatalf("Uexec mapping wrong: %+v", u)
	}
	if _, err := LoadFrom(flag.NewFlagSet("t", flag.ContinueOnError), []string{"-nope"}); err == nil {
		t.Fatal("unknown flag must error")
	}
	t.Setenv("UMCP_COMMAND_TIMEOUT", "garbage")
	c2, _ := LoadFrom(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if c2.CommandTimeout <= 0 {
		t.Fatal("an unparseable duration env must fall back to the default")
	}
}

func TestEngineHelpersEdges(t *testing.T) {
	if engineAssociation("") != "" || engineAssociation(t.TempDir()) != "" {
		t.Fatal("no project / no .uproject → no association")
	}
	if !versionLess("5.6", "5.10") || versionLess("5.10", "5.6") || !versionLess("5", "5.1") {
		t.Fatal("numeric version compare wrong")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.dat")
	os.WriteFile(bad, []byte("not json"), 0o644)
	old := launcherInstalledPath
	launcherInstalledPath = func() string { return bad }
	defer func() { launcherInstalledPath = old }()
	t.Setenv("UE_ENGINE_DIR", "")
	if DiscoverEngine("") != "" {
		t.Fatal("corrupt launcher file → empty")
	}
	if launcherInstalledPath() == "" || old() == "" {
		t.Fatal("default launcher path must be non-empty")
	}
}
