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

var parityToolNames = []string{
	"delete_actor", "editor_status", "execute_console_command", "execute_python",
	"get_actor", "import_assets", "list_actors", "list_assets", "live_coding_compile",
	"open_level", "save_all", "set_actor_transform", "spawn_actor", "start_play",
	"stop_play", "take_screenshot",
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

// TestServerListsParityTools is the P3 offline acceptance: the server boots over
// an in-memory transport and advertises the 16 frozen parity tools (plus e2e
// additions), without any editor running.
func TestServerListsParityTools(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	names := listToolNames(t, Deps{Bridge: b})
	for _, want := range parityToolNames {
		if !names[want] {
			t.Errorf("missing frozen parity tool: %q", want)
		}
	}
}

// TestBuildToolsRegisterWithJobs verifies the build/lifecycle tools appear only
// when a jobs registry is provided.
func TestBuildToolsRegisterWithJobs(t *testing.T) {
	b := bridge.New(noEditorRunner{}, bridge.Options{})
	withJobs := listToolNames(t, Deps{Bridge: b, Jobs: jobs.NewRegistry()})
	for _, name := range []string{"build_compile", "job_status", "job_cancel", "project_ensure_open", "editor_restart"} {
		if !withJobs[name] {
			t.Errorf("expected %q with a jobs registry", name)
		}
	}
	withoutJobs := listToolNames(t, Deps{Bridge: b})
	if withoutJobs["build_compile"] {
		t.Error("build_compile should not register without a jobs registry")
	}
	// e2e tools that don't need jobs still register.
	for _, name := range []string{"git_status", "pie_observe", "logs_mark", "apply_level_recipe"} {
		if !withoutJobs[name] {
			t.Errorf("expected %q to register without jobs", name)
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
