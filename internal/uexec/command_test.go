package uexec

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/fakeeditor"
)

func testCfg() Config {
	c := DefaultConfig()
	c.CommandAddr = "127.0.0.1:0" // ephemeral -> parallel-safe, advertised port = bound port
	c.PingInterval = 50 * time.Millisecond
	c.NodeTimeout = 3 * time.Second
	c.DiscoveryTimeout = 3 * time.Second
	c.CommandTimeout = 3 * time.Second
	c.AcceptAttempts = 4
	c.AcceptTimeout = 400 * time.Millisecond
	return c
}

// dialFake wires a Session's discovery at a fake editor over unicast loopback
// (no multicast), exercising the real socket IO, reverse-connect, and framing.
func dialFake(t *testing.T, e *fakeeditor.Editor, cfg Config) *Session {
	t.Helper()
	cfg = cfg.withDefaults()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	self := newUUID()
	logger := slog.New(slog.DiscardHandler)
	bc := newBroadcastConn(self, pc, e.Addr(), cfg.PingInterval, cfg.NodeTimeout, logger)
	bc.start(context.Background())
	s := &Session{cfg: cfg, self: self, logger: logger, bc: bc}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCommandRoundTrip(t *testing.T) {
	e, err := fakeeditor.Start(fakeeditor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s := dialFake(t, e, testCfg())

	res, err := s.RunCommand(context.Background(), "21 * 2", ModeEval)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Success || res.Result != "21 * 2" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCommandExecFileGuardPrefix(t *testing.T) {
	var seen string
	e, _ := fakeeditor.Start(fakeeditor.Options{
		OnCommand: func(req fakeeditor.CommandRequest) fakeeditor.CommandResponse {
			seen = req.Command
			return fakeeditor.CommandResponse{Success: true, Result: "ok"}
		},
	})
	defer e.Close()
	s := dialFake(t, e, testCfg())
	if _, err := s.RunCommand(context.Background(), `"docstring first line"`, ModeExecFile); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seen, "# mcp\n") {
		t.Fatalf("ExecuteFile command missing '# mcp' guard prefix: %q", seen)
	}
}

func TestCommandLargeResult(t *testing.T) {
	big := strings.Repeat("A", 200_000) // >> 64 KiB, spans many reads
	e, _ := fakeeditor.Start(fakeeditor.Options{
		OnCommand: func(fakeeditor.CommandRequest) fakeeditor.CommandResponse {
			return fakeeditor.CommandResponse{Success: true, Result: big}
		},
	})
	defer e.Close()
	s := dialFake(t, e, testCfg())
	res, err := s.RunCommand(context.Background(), "big", ModeEval)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Result) != len(big) {
		t.Fatalf("large result truncated: got %d want %d", len(res.Result), len(big))
	}
}

func TestCommandSegmentedWrites(t *testing.T) {
	// Editor writes the reply in 8192-byte TCP segments: the reference
	// recv-until-short-read heuristic would mis-frame; our json.Decoder must not.
	payload := strings.Repeat("S", 40_000)
	e, _ := fakeeditor.Start(fakeeditor.Options{
		SplitWritesAt: 8192,
		OnCommand: func(fakeeditor.CommandRequest) fakeeditor.CommandResponse {
			return fakeeditor.CommandResponse{Success: true, Result: payload}
		},
	})
	defer e.Close()
	s := dialFake(t, e, testCfg())
	res, err := s.RunCommand(context.Background(), "seg", ModeEval)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != payload {
		t.Fatalf("segmented reply mis-framed (len got=%d want=%d)", len(res.Result), len(payload))
	}
}

func TestCommandExactMultipleOf8192(t *testing.T) {
	// The exact bug: a reply whose length is an exact multiple of 8192 deadlocks
	// the recv<8192 heuristic. json.Decoder is immune. Guard with a timeout so a
	// regression hangs the test rather than passing.
	e, _ := fakeeditor.Start(fakeeditor.Options{
		PadResultToMultiple: 8192,
		OnCommand: func(fakeeditor.CommandRequest) fakeeditor.CommandResponse {
			return fakeeditor.CommandResponse{Success: true, Result: "exact"}
		},
	})
	defer e.Close()
	cfg := testCfg()
	cfg.CommandTimeout = 2 * time.Second
	s := dialFake(t, e, cfg)

	done := make(chan error, 1)
	go func() {
		_, err := s.RunCommand(context.Background(), "exact", ModeEval)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exact-multiple reply failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exact-multiple-of-8192 reply hung (framing regression)")
	}
}

func TestCommandFailureSurfacesResult(t *testing.T) {
	e, _ := fakeeditor.Start(fakeeditor.Options{
		OnCommand: func(fakeeditor.CommandRequest) fakeeditor.CommandResponse {
			return fakeeditor.CommandResponse{Success: false, Result: "Traceback: boom",
				Output: []fakeeditor.OutputEntry{{Type: "Error", Output: "boom"}}}
		},
	})
	defer e.Close()
	s := dialFake(t, e, testCfg())
	res, err := s.RunCommand(context.Background(), "explode", ModeExecFile)
	if err != nil {
		t.Fatalf("transport error should be nil for an editor-reported failure: %v", err)
	}
	if res.Success {
		t.Fatal("expected success=false")
	}
	if len(res.Output) != 1 || res.Output[0].Type != "Error" {
		t.Fatalf("output not surfaced: %+v", res.Output)
	}
}

// TestTimeoutTaintRecover is the §6.4 recovery contract: a slow command times
// out and taints the connection; the editor completes it late on a now-closed
// socket; the NEXT call reconnects cleanly and returns its own correct result.
func TestTimeoutTaintRecover(t *testing.T) {
	e, _ := fakeeditor.Start(fakeeditor.Options{
		OnCommand: func(req fakeeditor.CommandRequest) fakeeditor.CommandResponse {
			if strings.Contains(req.Command, "SLOWCMD") {
				time.Sleep(1500 * time.Millisecond) // >> CommandTimeout
			}
			return fakeeditor.CommandResponse{Success: true, Result: trimGuard(req.Command)}
		},
	})
	defer e.Close()
	cfg := testCfg()
	cfg.CommandTimeout = 300 * time.Millisecond
	s := dialFake(t, e, cfg)

	// 1) slow command -> timeout + taint.
	_, err := s.RunCommand(context.Background(), "SLOWCMD", ModeExecFile)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if s.cmd != nil {
		t.Fatal("expected command connection to be dropped after timeout")
	}

	// 2) next command reconnects cleanly (no desync from the abandoned reply).
	res, err := s.RunCommand(context.Background(), "AFTER", ModeEval)
	if err != nil {
		t.Fatalf("recovery command failed: %v", err)
	}
	if res.Result != "AFTER" {
		t.Fatalf("desync: got %q want %q", res.Result, "AFTER")
	}
}

func TestBusyAcceptExhaustion(t *testing.T) {
	// Editor never dials back -> reverse-connect accept exhausts -> actionable ErrConnectionLost.
	e, _ := fakeeditor.Start(fakeeditor.Options{RefuseConnectBack: true})
	defer e.Close()
	cfg := testCfg()
	cfg.AcceptAttempts = 3
	cfg.AcceptTimeout = 150 * time.Millisecond
	s := dialFake(t, e, cfg)

	node, err := s.WaitForNode(context.Background())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	err = s.OpenCommand(context.Background(), node.ID)
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected ErrConnectionLost, got %v", err)
	}
	if !strings.Contains(err.Error(), "did not connect back") {
		t.Fatalf("expected actionable message, got %v", err)
	}
}

func TestSpoofedSourceRejected(t *testing.T) {
	// A reply from a different source (or bad magic) must be rejected + tainted,
	// never returned as a result (§12.4 result-spoofing defense).
	e, _ := fakeeditor.Start(fakeeditor.Options{
		RawResponse: func(dest string) []byte {
			b, _ := json.Marshal(map[string]any{
				"version": 1, "magic": "ue_py", "type": "command_result",
				"source": "attacker-node", "dest": dest,
				"data": map[string]any{"success": true, "result": "pwned", "output": []any{}},
			})
			return b
		},
	})
	defer e.Close()
	s := dialFake(t, e, testCfg())
	_, err := s.RunCommand(context.Background(), "x", ModeEval)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected ErrProtocol for spoofed source, got %v", err)
	}
}

func trimGuard(s string) string { return strings.TrimPrefix(s, "# mcp\n") }
