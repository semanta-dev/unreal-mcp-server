package uexec

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// TestWaitForNodeStrictNoCrossTenantBind covers the multi-project daemon blocker: a
// leased session cold-starting its editor must NOT bind the only-currently-visible
// node when that node is a DIFFERENT project's editor on the shared discovery.
func TestWaitForNodeStrictNoCrossTenantBind(t *testing.T) {
	bc := &broadcastConn{nodes: newNodeTable(), logger: slog.New(slog.DiscardHandler)}
	// Another tenant's editor is the only node on the shared discovery right now.
	bc.nodes.upsert("aesir", pong(map[string]any{"project_root": "C:/games/aesir/"}), time.Now())

	// Non-strict (single-project stdio): falls back to the first node — legacy behavior.
	n, err := bc.waitForNode(context.Background(), "C:/games/poly-world", 30*time.Millisecond, false)
	if err != nil || n == nil || n.ID != "aesir" {
		t.Fatalf("non-strict should fall back to nodes[0], got n=%v err=%v", n, err)
	}

	// Strict (leased): refuses the foreign node — no cross-tenant bind, no orphan.
	if _, err := bc.waitForNode(context.Background(), "C:/games/poly-world", 30*time.Millisecond, true); !errors.Is(err, ErrEditorNotFound) {
		t.Fatalf("strict must refuse a foreign node with ErrEditorNotFound, got %v", err)
	}

	// Once poly-world's OWN editor advertises, strict returns it (the correct bind).
	bc.nodes.upsert("poly", pong(map[string]any{"project_root": "C:/games/poly-world/"}), time.Now())
	n, err = bc.waitForNode(context.Background(), "C:/games/poly-world", 30*time.Millisecond, true)
	if err != nil || n == nil || n.ID != "poly" {
		t.Fatalf("strict should bind the matching own node, got n=%v err=%v", n, err)
	}
}

// Found live (P7): a just-killed editor of the same project still advertised, so a
// daemon spawn bound its stale node instead of the editor it launched. Excluded nodes
// are never selected.
func TestWaitForNodeSkipsExcludedNodes(t *testing.T) {
	bc := &broadcastConn{nodes: newNodeTable(), logger: slog.New(slog.DiscardHandler)}
	bc.nodes.upsert("stale", pong(map[string]any{"project_root": "C:/games/poly-world/"}), time.Now())
	skip := map[string]bool{"stale": true}
	if _, err := bc.waitForNode(context.Background(), "C:/games/poly-world", 30*time.Millisecond, true, skip); !errors.Is(err, ErrEditorNotFound) {
		t.Fatalf("an excluded node must not be bound, got %v", err)
	}
	bc.nodes.upsert("fresh", pong(map[string]any{"project_root": "C:/games/poly-world/"}), time.Now())
	n, err := bc.waitForNode(context.Background(), "C:/games/poly-world", 30*time.Millisecond, true, skip)
	if err != nil || n == nil || n.ID != "fresh" {
		t.Fatalf("want the fresh node, got n=%v err=%v", n, err)
	}
}
