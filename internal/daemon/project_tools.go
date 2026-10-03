package daemon

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type projectAttachIn struct {
	Project string `json:"project" jsonschema:"absolute path to the Unreal PROJECT DIRECTORY (the folder containing the .uproject) to bind THIS agent session to; a warm editor is reused or a fresh one is spawned"`
}
type projectReleaseIn struct{}
type projectListIn struct{}

// RegisterProjectTools registers the daemon-only session/lease control surface. These
// are the ONLY tools that work before project_attach; every other tool resolves its
// editor from the session's lease (NO_PROJECT_ATTACHED until attached).
func (dm *Daemon) RegisterProjectTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "project_attach",
		Description: "Bind THIS agent session to an Unreal project's editor (1:1 lease): reuses a warm idle editor for the project or spawns a fresh one, then routes all your subsequent tool calls to it. Call this first. Idempotent — re-attaching returns the same editor.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in projectAttachIn) (*mcp.CallToolResult, map[string]any, error) {
		sid := sessionID(req)
		if sid == "" {
			return nil, nil, fmt.Errorf("project_attach: no session on request")
		}
		if in.Project == "" {
			return nil, nil, fmt.Errorf("project_attach: `project` (project directory path) is required")
		}
		id, err := dm.Router.Attach(ctx, sid, in.Project)
		if err != nil {
			return nil, nil, fmt.Errorf("project_attach: %w", err)
		}
		return nil, map[string]any{"attached": true, "instance": id, "project": in.Project}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "project_release",
		Description: "Release this session's editor lease (the editor is kept warm for reuse if healthy, or killed if mid-restart). Called automatically when your session ends; call it explicitly to hand the editor back early.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in projectReleaseIn) (*mcp.CallToolResult, map[string]any, error) {
		dm.Router.Release(sessionID(req))
		return nil, map[string]any{"released": true}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "project_list",
		Description: "List every editor instance the daemon is managing (project, lifecycle state, whether it's leased) — the cross-session view of what's running. Yours is marked.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in projectListIn) (*mcp.CallToolResult, map[string]any, error) {
		me, _ := dm.Router.InstanceFor(sessionID(req))
		var list []map[string]any
		for _, inst := range dm.Pool.List() {
			list = append(list, map[string]any{
				"instance": inst.ID,
				"project":  inst.Project,
				"state":    string(inst.State),
				"leased":   inst.LeasedBy != "",
				"mine":     inst.ID == me,
			})
		}
		return nil, map[string]any{"instances": list, "count": len(list)}, nil
	})
}
