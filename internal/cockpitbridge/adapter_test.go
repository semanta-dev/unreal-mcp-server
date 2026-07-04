package cockpitbridge

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/cockpit"
)

// tinyEditor is a minimal TCP editor speaking the cockpit protocol: hello->welcome, then
// replies to each rpc with a scripted flat rpc_result frame.
func tinyEditor(t *testing.T, reply func(op string) *cockpit.Frame) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if _, err := cockpit.ReadFrame(conn); err != nil { // hello
			return
		}
		// welcome epoch matches the cockpit_info probe epoch (both are MCPCore's in reality)
		_ = cockpit.WriteFrame(conn, &cockpit.Frame{Type: cockpit.FrameWelcome, SessionEpoch: "ep-1", ProtocolVersion: cockpit.ProtocolVersion})
		for {
			f, err := cockpit.ReadFrame(conn)
			if err != nil {
				return
			}
			if f.Type == cockpit.FrameRPC {
				r := reply(f.Op)
				r.OpID = f.OpID
				_ = cockpit.WriteFrame(conn, r)
			}
		}
	}()
	return ln.Addr().String()
}

func TestAdapterFlattensResult(t *testing.T) {
	ok := true
	addr := tinyEditor(t, func(op string) *cockpit.Frame {
		return &cockpit.Frame{Type: cockpit.FrameRPCResult, OK: &ok, Result: json.RawMessage(`{"spawned":"a1"}`)}
	})
	client, err := cockpit.Dial(context.Background(), addr, cockpit.DialConfig{Timeout: 2 * time.Second}, cockpit.Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	a := New(client)
	nr, err := a.RPCNative(context.Background(), "spawn_actor", json.RawMessage(`{}`), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !nr.OK || string(nr.Result) != `{"spawned":"a1"}` {
		t.Fatalf("flattened result = %+v", nr)
	}
}

func TestAdapterFlattensError(t *testing.T) {
	no := false
	addr := tinyEditor(t, func(op string) *cockpit.Frame {
		return &cockpit.Frame{Type: cockpit.FrameRPCResult, OK: &no, Error: "no class", Code: "CLASS_UNRESOLVED", Retryable: false}
	})
	client, err := cockpit.Dial(context.Background(), addr, cockpit.DialConfig{Timeout: 2 * time.Second}, cockpit.Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	nr, err := New(client).RPCNative(context.Background(), "spawn_actor", json.RawMessage(`{}`), "", "")
	if err != nil {
		t.Fatalf("transport error should be nil for an op-level failure: %v", err)
	}
	if nr.OK || nr.Code != "CLASS_UNRESOLVED" || nr.Error != "no class" {
		t.Fatalf("flattened error = %+v", nr)
	}
}
