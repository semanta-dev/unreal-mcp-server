//go:build live

// Live end-to-end test against a running editor (aesir-wave-defense open).
// Exercises the v2 write path (actor_edit spawn/delete) and actor_query:
//
//	go test -tags live -run TestLiveSpawnGetDelete ./internal/tools/
//
// Set UMCP_PROJECT_DIR to select the editor (defaults to the aesir path).
package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

func TestLiveSpawnGetDelete(t *testing.T) {
	proj := os.Getenv("UMCP_PROJECT_DIR")
	if proj == "" {
		proj = "C:/Users/jorda/code/games/aesir-wave-defense"
	}
	cfg := uexec.DefaultConfig()
	cfg.ProjectDir = proj
	cfg.DiscoveryTimeout = 8 * time.Second

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sess := uexec.New(cfg, logger)
	ctx := context.Background()
	if err := sess.Start(ctx); err != nil {
		t.Fatalf("start discovery: %v", err)
	}
	defer sess.Close()
	b := bridge.New(sess, bridge.Options{Mode: bridge.ModeHotload, ProjectDir: proj, Logger: logger})

	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "live"}, nil)
	RegisterAll(srv, Deps{Bridge: b})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	label := "MCP_LiveTest_Light"
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s failed: %v %+v", name, err, res)
		}
		var out map[string]any
		structInto(t, res, &out)
		return out
	}
	spawned := call("actor_edit", map[string]any{"op": "spawn", "world": "editor", "class": "PointLight",
		"label": label, "location": []any{0, 0, 500}})
	if spawned["spawned"].(map[string]any)["label"] != label {
		t.Fatalf("spawn returned %v", spawned)
	}
	got := call("actor_query", map[string]any{"op": "get", "actor": label})
	if !strings.Contains(strings.ToLower(got["actor"].(map[string]any)["class"].(string)), "pointlight") {
		t.Fatalf("get = %v, want a PointLight", got)
	}
	call("actor_edit", map[string]any{"op": "delete", "world": "editor", "actor": label})
	if left := call("actor_query", map[string]any{"op": "find", "filter": label}); left["count"] != 0.0 {
		t.Fatalf("actor still present after delete: %v", left)
	}
}

func structInto(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
}
