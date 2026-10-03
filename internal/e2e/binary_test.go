package e2e

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// T3 (plan §3.1): the real binary, built with coverage instrumentation, driven over
// its real transports. Skipped with -short.

func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("binary smoke test skipped in -short mode")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	exe := filepath.Join(t.TempDir(), "unreal-mcp")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command(gobin, "build", "-cover", "-coverpkg=github.com/jdziat/unreal-mcp-server/...", "-o", exe, "../../cmd/unreal-mcp")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return exe
}

func covDir(t *testing.T) string {
	if d := os.Getenv("UMCP_T3_GOCOVERDIR"); d != "" { // merged by scripts/coverage
		return d
	}
	return t.TempDir()
}

func TestBinaryStdio(t *testing.T) {
	exe := buildBinary(t)
	emu := bridgetest.New()
	bridgetest.NewWorld().Install(emu)
	ed, err := uexectest.Start(emu.Options())
	if err != nil {
		t.Fatal(err)
	}
	defer ed.Close()

	cov := covDir(t)
	cmd := exec.Command(exe, "-group", ed.Addr().String(), "-command-addr", "127.0.0.1:0",
		"-log-format", "text", "-cockpit", "off", "-discovery-timeout", "3s")
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+cov)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t3", Version: "1"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}

	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tl.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"editor", "python", "actor_query", "actor_edit"} {
		if !names[want] {
			t.Fatalf("binary does not serve %q (%d tools)", want, len(tl.Tools))
		}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "actor_edit",
		Arguments: map[string]any{"op": "spawn", "world": "editor", "class": "/Script/Engine.Actor", "label": "FromBinary"}})
	if err != nil || res.IsError {
		t.Fatalf("spawn via binary: %v %s\nstderr:\n%s", err, text(res), stderr.String())
	}
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "actor_query", Arguments: map[string]any{"op": "list"}})
	if !strings.Contains(text(res), "1 actors") {
		t.Fatalf("actor_query via binary: %s", text(res))
	}
	// Any non-JSON-RPC write to stdout would already have broken the transport.

	start := time.Now()
	if err := cs.Close(); err != nil { // closes stdin → the server must exit cleanly
		t.Fatalf("close: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("server took %s to exit on stdin EOF (CommandTransport kills after 5s, losing coverage)", d)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("server did not exit 0: %v\nstderr:\n%s", cmd.ProcessState, stderr.String())
	}
	if ents, _ := filepath.Glob(filepath.Join(cov, "covcounters.*")); len(ents) == 0 {
		t.Fatal("no coverage counters written (unclean exit)")
	}
}

func TestBinaryDaemonTwoSessions(t *testing.T) {
	exe := buildBinary(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cov := covDir(t)
	// Point discovery at a dead unicast port: the daemon serves without any editor.
	cmd := exec.Command(exe, "-daemon-addr", addr, "-group", "127.0.0.1:9", "-log-format", "text", "-cockpit", "off")
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+cov, "APPDATA="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	url := "http://" + addr
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connect := func() *mcp.ClientSession {
		var lastErr error
		for i := 0; i < 50; i++ {
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "t3", Version: "1"}, nil).
				Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, MaxRetries: -1}, nil)
			if err == nil {
				return cs
			}
			lastErr = err
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("daemon not reachable: %v\nstderr:\n%s", lastErr, stderr.String())
		return nil
	}
	a, b := connect(), connect()
	defer a.Close()
	defer b.Close()
	for i, cs := range []*mcp.ClientSession{a, b} {
		tl, err := cs.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, tool := range tl.Tools {
			names[tool.Name] = true
		}
		if !names["project_attach"] || !names["project_list"] {
			t.Fatalf("session %d: daemon toolset missing: %d tools", i, len(tl.Tools))
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_list"})
		if err != nil || res.IsError {
			t.Fatalf("session %d project_list: %v %s", i, err, text(res))
		}
		// An unattached session gets an enveloped error, not a crash.
		res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "editor", Arguments: map[string]any{"op": "status"}})
		if err != nil || !res.IsError {
			t.Fatalf("session %d: unattached editor_status should fail cleanly", i)
		}
	}
}
