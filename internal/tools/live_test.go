//go:build live

// Live end-to-end test against a running editor (aesir-wave-defense open).
// Exercises the write path + structHandler + CallText text tool:
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

	// spawn_actor (structHandler) — a PointLight we can clean up.
	sp, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "spawn_actor", Arguments: map[string]any{
		"class_path": "/Script/Engine.PointLight", "x": 0, "y": 0, "z": 500, "label": label,
	}})
	if err != nil || sp.IsError {
		t.Fatalf("spawn_actor failed: %v %+v", err, sp.Content)
	}
	var spawned map[string]any
	structInto(t, sp, &spawned)
	if spawned["label"] != label {
		t.Fatalf("spawn returned unexpected label: %v", spawned)
	}
	t.Logf("spawned: %v", spawned)

	// get_actor (structHandler) — verify it exists.
	ga, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_actor", Arguments: map[string]any{"actor_label": label}})
	if err != nil || ga.IsError {
		t.Fatalf("get_actor failed: %v", err)
	}
	var got map[string]any
	structInto(t, ga, &got)
	if !strings.Contains(strings.ToLower(got["class"].(string)), "pointlight") {
		t.Fatalf("get_actor class = %v, want a PointLight", got["class"])
	}
	t.Logf("get_actor: class=%v location=%v", got["class"], got["location"])

	// delete_actor (textHandler -> CallText) — verify the message text.
	da, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "delete_actor", Arguments: map[string]any{"actor_label": label}})
	if err != nil || da.IsError {
		t.Fatalf("delete_actor failed: %v", err)
	}
	txt := ""
	for _, c := range da.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			txt = tc.Text
		}
	}
	if !strings.Contains(txt, "Deleted "+label) {
		t.Fatalf("delete_actor text = %q, want it to contain 'Deleted %s'", txt, label)
	}
	t.Logf("delete_actor: %q", txt)
}

func structInto(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
}
