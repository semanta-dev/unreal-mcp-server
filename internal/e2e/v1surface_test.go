package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

type noEditor struct{}

func (noEditor) Generation() uint64 { return 0 }
func (noEditor) RunCommand(context.Context, string, uexec.ExecMode) (uexec.CommandResult, error) {
	return uexec.CommandResult{}, uexec.ErrEditorNotFound
}

// stdioSurface returns the stdio tools/list result JSON with every optional dep wired.
func stdioSurface(t *testing.T) []byte {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "golden"}, nil)
	tools.RegisterAll(srv, tools.Deps{
		Bridge:     bridge.New(noEditor{}, bridge.Options{}),
		Jobs:       jobs.NewRegistry(),
		CockpitURL: func() (string, bool) { return "", false },
	})
	raw, err := json.Marshal(listTools(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	return raw
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

// TestSurfaceGolden pins the full stdio tools/list (names, schemas, descriptions,
// annotations). Every change to the surface shows up as a diff; regenerate with
// UMCP_UPDATE_GOLDEN=1 after reviewing it. The v1 → v2 accounting lives in
// internal/tools/migration_test.go.
func TestSurfaceGolden(t *testing.T) {
	raw := stdioSurface(t)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	t.Logf("stdio tools/list = %d bytes", len(raw))
	path := filepath.Join("testdata", "tools_list.golden.json")
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
