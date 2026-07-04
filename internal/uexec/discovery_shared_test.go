package uexec

import (
	"context"
	"testing"
)

// Two per-instance Sessions sharing one Discovery must reuse the SAME multicast
// socket + node table + self-id, and closing one Session must NOT tear down the
// shared Discovery (the daemon owns its lifetime).
func TestSharedDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	disc, err := OpenDiscovery(ctx, Config{}, nil)
	if err != nil {
		t.Skipf("multicast unavailable in this env: %v", err)
	}
	defer disc.Close()

	// Two per-instance sessions, each with its own ephemeral command port.
	a := NewOnDiscovery(Config{CommandAddr: "127.0.0.1:0", ProjectDir: "C:/proj/A"}, disc, nil)
	b := NewOnDiscovery(Config{CommandAddr: "127.0.0.1:0", ProjectDir: "C:/proj/B"}, disc, nil)

	// Both share the discovery's self-id and its broadcast conn (one socket).
	if a.SelfID() != disc.SelfID() || b.SelfID() != disc.SelfID() {
		t.Fatalf("shared sessions must use the discovery self-id: a=%s b=%s disc=%s", a.SelfID(), b.SelfID(), disc.SelfID())
	}
	if a.bc != disc.bc || b.bc != disc.bc {
		t.Fatal("shared sessions must reuse the discovery's broadcast conn (single socket)")
	}
	// Start() is a no-op for a shared session (discovery already running).
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start on shared session should be a no-op: %v", err)
	}

	// Closing session A must not close the shared discovery: B and disc still work.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if a.bc != nil {
		t.Fatal("closed session should drop its bc reference")
	}
	// The shared discovery's node table is still queryable (socket alive).
	_ = disc.Nodes()
	if b.bc != disc.bc {
		t.Fatal("closing A must not affect B's shared discovery")
	}
}
