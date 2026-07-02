package tools

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRecoverMiddlewareContainsPanic registers a tool that panics and asserts the
// server survives and returns an error result rather than crashing.
func TestRecoverMiddlewareContainsPanic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ServerOptions{Logger: logger})
	InstallMiddleware(srv, logger)
	mcp.AddTool(srv, &mcp.Tool{Name: "boom", Description: "panics"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			panic("intentional test panic")
		})

	ctx := context.Background()
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

	// The call should return an error (from the recovered panic), not hang or
	// crash the server session.
	_, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "boom", Arguments: map[string]any{}})
	if err == nil {
		t.Fatal("expected an error from the panicking tool")
	}

	// The server must still be alive: a follow-up ListTools succeeds.
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatalf("server did not survive the panic: %v", err)
	}
}
