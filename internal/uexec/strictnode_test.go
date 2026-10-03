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

// Gate finding (P7): excluding the node a session is connected to must drop that
// channel and forget the node, so Close never sends it close_connection (the node may
// be another session's editor).
func TestExcludingTheCurrentNodeForgetsIt(t *testing.T) {
	s := New(DefaultConfig(), nil)
	s.nodeID = "theirs"
	s.ExcludeNode("theirs")
	if s.nodeID != "" || !s.excluded["theirs"] {
		t.Fatalf("nodeID=%q excluded=%v", s.nodeID, s.excluded)
	}
}

// Gate finding (P7): with strict selection on in stdio, matching must respect path
// segments, and a node advertising another root never matches by name or substring.
func TestNodeMatchesProjectOnSegmentBoundaries(t *testing.T) {
	poly := &Node{ProjectRoot: "C:/games/poly-world/", ProjectName: "PolyWorld"}
	if ok, _ := nodeMatchesProject(poly, "C:/games/poly"); ok {
		t.Fatal("C:/games/poly must not match C:/games/poly-world")
	}
	if ok, _ := nodeMatchesProject(poly, `c:\games\poly-world`); !ok {
		t.Fatal("same root with other separators/case should match")
	}
	if ok, _ := nodeMatchesProject(poly, "C:/games/poly-world/PolyWorld"); !ok {
		t.Fatal("a project dir inside the advertised root should match")
	}
	other := &Node{ProjectRoot: "D:/checkout2/PolyWorld/", ProjectName: "PolyWorld", Data: []byte(`{"x":"polyworld"}`)}
	if ok, _ := nodeMatchesProject(other, "C:/games/PolyWorld"); ok {
		t.Fatal("another checkout with the same name must not match")
	}
}
