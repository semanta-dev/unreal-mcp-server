package tools

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// noEditorRunner satisfies bridge.Runner but never reaches an editor. Listing
// tools must not invoke it, so the offline smoke test passes with no editor.
type noEditorRunner struct{}

func (noEditorRunner) Generation() uint64 { return 0 }
func (noEditorRunner) RunCommand(context.Context, string, uexec.ExecMode) (uexec.CommandResult, error) {
	return uexec.CommandResult{}, uexec.ErrEditorNotFound
}

func listToolNames(t *testing.T, d Deps) map[string]bool {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "test"}, nil)
	RegisterAll(srv, d)
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make(map[string]bool)
	for _, tool := range res.Tools {
		if names[tool.Name] {
			t.Fatalf("duplicate tool name registered: %q", tool.Name)
		}
		names[tool.Name] = true
	}
	return names
}

// TestLongRunningToolsAlwaysRegister: build/job/editor_lifecycle register whether or
// not a jobs registry is wired; without one they fail with PRECONDITION at call time
// (a stable tools/list, plan R1), never by vanishing.
func TestLongRunningToolsAlwaysRegister(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	for _, d := range []Deps{{Bridge: b, Jobs: jobs.NewRegistry()}, {Bridge: b}} {
		names := listToolNames(t, d)
		for _, name := range []string{"build", "job", "editor_lifecycle", "git", "logs", "python"} {
			if !names[name] {
				t.Errorf("expected %q (jobs wired: %v)", name, d.Jobs != nil)
			}
		}
	}
}

// TestToolsHaveInputSchemas checks each tool advertises an input schema (the SDK
// infers it from the typed In struct), so Claude gets argument hints.
func TestToolsHaveInputSchemas(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "test"}, nil)
	RegisterAll(srv, Deps{Bridge: b, Jobs: jobs.NewRegistry()})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, _ := srv.Connect(ctx, serverT, nil)
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, _ := client.Connect(ctx, clientT, nil)
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
	}
}
