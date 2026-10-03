package tools

import (
	"context"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/audit"
	"github.com/jdziat/unreal-mcp-server/internal/design"
)

func callDesignTool(t *testing.T, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "unreal", Version: "test"}, nil)
	reg := &registrar{}
	registerDesignTools(reg)
	spec.Register(srv, reg.specs, spec.Options{})

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

	return cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func TestDesignToolsRepresentativeAudits(t *testing.T) {
	t.Run("decision_audit metronome fails", func(t *testing.T) {
		res, err := callDesignTool(t, "decision_audit", map[string]any{
			"points": audit.MetronomeDecisions(30),
		})
		if err != nil {
			t.Fatal(err)
		}
		var out decisionReportDTO
		decodeStructured(t, res, &out)
		if out.Pass {
			t.Fatalf("decision_audit pass = true, want false: %+v", out)
		}
	})

	t.Run("primitive_audit cube scene fails", func(t *testing.T) {
		res, err := callDesignTool(t, "primitive_audit", map[string]any{
			"scene": audit.CubeScene(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var out audit.PrimitiveReport
		decodeStructured(t, res, &out)
		if out.Pass {
			t.Fatalf("primitive_audit pass = true, want false: %+v", out)
		}
	})

	t.Run("balance_sweep metronome gate fails", func(t *testing.T) {
		res, err := callDesignTool(t, "balance_sweep", map[string]any{
			"scaffold": design.MetronomeScaffold(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var out balanceSweepOut
		decodeStructured(t, res, &out)
		if out.Gate.Pass {
			t.Fatalf("balance_sweep gate pass = true, want false: %+v", out.Gate)
		}
	})
}

type decisionReportDTO struct {
	Pass bool
}
