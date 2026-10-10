//go:build live

// Package livetest is the repeatable live check: the real server binary, driven through
// an MCP client, against running Unreal editors on scratch copies of the target games.
// Each test asserts concrete outcomes in the editor or the game and cleans up after
// itself; a test whose project is not configured is skipped.
//
//	UMCP_LIVE_AESIR=<scratch aesir dir> UMCP_LIVE_AESIR_ADDR=127.0.0.1:6791 \
//	UMCP_LIVE_POLYWORLD=<scratch PolyWorld dir> UMCP_LIVE_POLYWORLD_ADDR=127.0.0.1:6792 \
//	UMCP_LIVE_GROUP=239.0.0.42:6799 UMCP_ENGINE_DIR=<UE_5.7> \
//	go test -tags live -v -count=1 ./internal/livetest/
//
// scripts/live.sh runs it with the scratch defaults. Never point it at a real project:
// the tests play, spawn, create assets and place roads.
package livetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var serverBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "umcp-live-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	serverBin = filepath.Join(dir, "unreal-mcp")
	if runtime.GOOS == "windows" {
		serverBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", serverBin, "github.com/jdziat/unreal-mcp-server/cmd/unreal-mcp")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build the server:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// live is one server session on one project's editor.
type live struct {
	t       *testing.T
	ctx     context.Context
	cs      *mcp.ClientSession
	project string
}

// session starts the server for project ("AESIR" or "POLYWORLD"), or skips the test
// when that project is not configured. The editor is opened if it is not running.
func session(t *testing.T, project string) *live {
	t.Helper()
	dir, addr := os.Getenv("UMCP_LIVE_"+project), os.Getenv("UMCP_LIVE_"+project+"_ADDR")
	if dir == "" || addr == "" {
		t.Skipf("UMCP_LIVE_%s / UMCP_LIVE_%s_ADDR not set", project, project)
	}
	args := []string{"-project", dir, "-command-addr", addr, "-log-level", "warn"}
	if g := os.Getenv("UMCP_LIVE_GROUP"); g != "" {
		args = append(args, "-group", g)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)
	cmd := exec.Command(serverBin, args...)
	cmd.Stderr = os.Stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "livetest", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	l := &live{t: t, ctx: ctx, cs: cs, project: dir}
	l.job(l.call("editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25}), 10*time.Minute)
	return l
}

// raw calls a tool: its structured result and whether it was an error.
func (l *live) raw(tool string, args map[string]any) (map[string]any, bool) {
	l.t.Helper()
	res, err := l.cs.CallTool(l.ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		l.t.Fatalf("%s %v: %v", tool, args, err)
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	return out, res.IsError
}

// call calls a tool that must succeed.
func (l *live) call(tool string, args map[string]any) map[string]any {
	l.t.Helper()
	out, isErr := l.raw(tool, args)
	if isErr {
		l.t.Fatalf("%s %v failed: %v", tool, args, out)
	}
	return out
}

// fail calls a tool that must fail with code; returns the error object.
func (l *live) fail(tool string, args map[string]any, code string) map[string]any {
	l.t.Helper()
	out, isErr := l.raw(tool, args)
	e, _ := out["error"].(map[string]any)
	if !isErr || e["code"] != code {
		l.t.Fatalf("%s %v: want %s, got %v", tool, args, code, out)
	}
	return e
}

// job follows an async result to its end and returns the job's result; a failed job
// fails the test.
func (l *live) job(out map[string]any, limit time.Duration) map[string]any {
	l.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		state, _ := out["state"].(string)
		id, _ := out["job_id"].(string)
		if id == "" {
			return out
		}
		switch state {
		case "succeeded":
			r, _ := out["result"].(map[string]any)
			return r
		case "failed", "cancelled":
			l.t.Fatalf("job %s %s: %v", id, state, out["error"])
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("job %s still %s after %s", id, state, limit)
		}
		out = l.call("job", map[string]any{"op": "wait", "job_id": id, "wait_s": 25})
	}
}

func (l *live) enable(toolsets ...string) {
	for _, ts := range toolsets {
		l.call("toolsets", map[string]any{"op": "enable", "toolset": ts})
	}
}

// python runs code in the editor as a test oracle (the tools under test never need it)
// and returns its printed output.
func (l *live) python(code string) string {
	l.t.Helper()
	return fmt.Sprint(l.call("python", map[string]any{"op": "run", "code": code})["output"])
}

// freshPIE restarts play so the test starts in a new game world, and stops it after.
func (l *live) freshPIE() {
	l.t.Helper()
	l.call("pie", map[string]any{"op": "stop"})
	l.call("pie", map[string]any{"op": "start"})
	l.t.Cleanup(func() { _, _ = l.raw("pie", map[string]any{"op": "stop"}) })
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func contains(v any, s string) bool { return strings.Contains(fmt.Sprint(v), s) }
