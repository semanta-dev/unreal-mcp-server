//go:build integration

package uexec

import (
	"context"
	"testing"
	"time"
)

// TestLoopbackMulticastRoundTrip validates the make-or-break OS mechanism
// (Risk #1/#2): two sockets can both bind the discovery port (SO_REUSEADDR
// before bind) and receive each other's datagrams on the loopback multicast
// group with TTL 0. This is exactly what the editor plugin relies on.
//
// Run on Windows with:  go test -tags integration ./internal/uexec/
func TestLoopbackMulticastRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()

	pcA, group, _, err := newMulticastConn(ctx, cfg)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer pcA.Close()
	pcB, _, _, err := newMulticastConn(ctx, cfg)
	if err != nil {
		t.Fatalf("open B (shared port needs SO_REUSEADDR before bind): %v", err)
	}
	defer pcB.Close()

	raw, _ := newMessage(TypePing, "A-sender", "", nil).encode()
	if _, err := pcA.WriteTo(raw, group); err != nil {
		t.Fatalf("A write to group: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	buf := make([]byte, 65536)
	for {
		if time.Now().After(deadline) {
			t.Fatal("B never received A's multicast ping on loopback (Risk #1: loopback multicast not delivering)")
		}
		_ = pcB.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := pcB.ReadFrom(buf)
		if err != nil {
			continue
		}
		m, derr := decodeMessage(buf[:n])
		if derr != nil {
			continue
		}
		if m.Type == TypePing && m.Source == "A-sender" {
			return // success: loopback multicast delivered across two REUSEADDR sockets
		}
	}
}
