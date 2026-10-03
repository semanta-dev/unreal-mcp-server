package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/daemon"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

type noEditor struct{}

func (noEditor) Generation() uint64 { return 0 }
func (noEditor) RunCommand(context.Context, string, uexec.ExecMode) (uexec.CommandResult, error) {
	return uexec.CommandResult{}, uexec.ErrEditorNotFound
}

// v1Surface lists every tool name the v1 server can advertise: stdio mode with
// all optional deps wired (jobs, cockpit URL) plus the daemon-only project tools,
// and the byte size of the stdio tools/list result.
func v1Surface(t *testing.T) (names []string, listBytes int) {
	names, raw := v1SurfaceRaw(t)
	return names, len(raw)
}

// v1SurfaceRaw also returns the stdio tools/list result JSON.
func v1SurfaceRaw(t *testing.T) (names []string, stdioList []byte) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "v1"}, nil)
	tools.RegisterAll(srv, tools.Deps{
		Bridge:     bridge.New(noEditor{}, bridge.Options{}),
		Jobs:       jobs.NewRegistry(),
		CockpitURL: func() (string, bool) { return "", false },
	})
	stdio := listTools(t, srv)
	raw, err := json.Marshal(stdio)
	if err != nil {
		t.Fatal(err)
	}

	// Daemon mode adds the project_* tools (registered only when a ProjectManager is
	// wired); everything else is shared with stdio.
	seen := map[string]bool{}
	for _, tl := range stdio.Tools {
		if seen[tl.Name] {
			t.Fatalf("duplicate v1 tool %q", tl.Name)
		}
		seen[tl.Name] = true
		names = append(names, tl.Name)
	}
	for _, sp := range tools.Specs(tools.Deps{Bridge: bridge.New(noEditor{}, bridge.Options{}), Projects: &daemon.Daemon{}}) {
		if !seen[sp.Name] {
			seen[sp.Name] = true
			names = append(names, sp.Name)
		}
	}
	sort.Strings(names)
	return names, raw
}

func listTools(t *testing.T, srv *mcp.Server) *mcp.ListToolsResult {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestV1SurfaceFrozen pins the complete v1 tool-name list (the migration
// bijection's left-hand side, docs/OVERHAUL_PLAN.md §2.3). Regenerate with
// UMCP_UPDATE_GOLDEN=1 only while v1 is still the registered surface.
func TestV1SurfaceFrozen(t *testing.T) {
	names, n := v1Surface(t)
	t.Logf("v1: %d tools (stdio+daemon); stdio tools/list = %d bytes", len(names), n)
	path := filepath.Join("testdata", "v1_tools.txt")
	got := strings.Join(names, "\n") + "\n"
	if os.Getenv("UMCP_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UMCP_UPDATE_GOLDEN=1 to create): %v", err)
	}
	if string(want) != got {
		t.Fatalf("v1 tool surface changed; diff %s against the registered names", path)
	}
}

// TestV1EnvelopeGolden pins the full stdio tools/list as served through the spec
// layer (v1 names + schemas, v2 annotations, no output schemas). Any schema,
// description or annotation change shows up as a diff. Regenerate with
// UMCP_UPDATE_GOLDEN=1.
func TestV1EnvelopeGolden(t *testing.T) {
	_, raw := v1SurfaceRaw(t)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("testdata", "tools_list.v1-envelope.golden.json")
	if os.Getenv("UMCP_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (UMCP_UPDATE_GOLDEN=1 to create): %v", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), pretty.Bytes()) {
		t.Fatalf("tools/list changed vs %s (%d bytes now); review and regenerate if intended", path, len(raw))
	}
}
