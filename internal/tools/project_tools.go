package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

type projectAttachIn struct {
	Project string `json:"project" jsonschema:"absolute path to the Unreal PROJECT DIRECTORY (the folder containing the .uproject) to bind THIS agent session to; a warm editor is reused or a fresh one is spawned"`
}
type projectReleaseIn struct{}
type projectListIn struct{}

// registerProjectTools adds the daemon-only session/lease tools (toolset "daemon").
// They are the only tools that work before project_attach.
func registerProjectTools(s *registrar, d Deps) {
	if d.Projects == nil {
		return
	}
	pm := d.Projects
	addIn(s, spec.Daemon, "project_attach",
		"Bind THIS agent session to an Unreal project's editor (1:1 lease): reuses a warm idle editor for the project or spawns a fresh one, then routes all your subsequent tool calls to it. Call this first. Idempotent — re-attaching returns the same editor.",
		func(ctx context.Context, req *mcp.CallToolRequest, in projectAttachIn) (*mcp.CallToolResult, map[string]any, error) {
			sid := ""
			if req.Session != nil {
				sid = req.Session.ID()
			}
			id, err := pm.Attach(ctx, sid, in.Project)
			if err != nil {
				return nil, nil, fmt.Errorf("project_attach: %w", err)
			}
			out := map[string]any{"attached": true, "instance": id, "project": in.Project}
			// Apply the project's .umcp.json toolsets to this session.
			if ts, ok := spec.ToolsetsFrom(ctx); ok {
				pf, perr := session.LoadProjectFile(in.Project)
				if perr != nil {
					out["project_file_error"] = perr.Error()
				} else {
					want := []spec.Toolset{spec.Daemon}
					for _, t := range pf.Toolsets {
						want = append(want, spec.Toolset(t))
					}
					if aerr := ts.Apply(want); aerr != nil {
						out["toolsets_error"] = aerr.Error()
					}
					out["toolsets"] = ts.Enabled()
				}
			}
			return nil, out, nil
		})
	addIn(s, spec.Daemon, "project_release",
		"Release this session's editor lease (the editor is kept warm for reuse if healthy, or killed if mid-restart). Called automatically when your session ends; call it explicitly to hand the editor back early.",
		func(ctx context.Context, req *mcp.CallToolRequest, _ projectReleaseIn) (*mcp.CallToolResult, map[string]any, error) {
			if req.Session != nil {
				pm.Release(req.Session.ID())
			}
			return nil, map[string]any{"released": true}, nil
		})
	addIn(s, spec.Daemon, "project_list",
		"List every editor instance the daemon is managing (project, lifecycle state, whether it's leased) — the cross-session view of what's running. Yours is marked.",
		func(ctx context.Context, req *mcp.CallToolRequest, _ projectListIn) (*mcp.CallToolResult, map[string]any, error) {
			sid := ""
			if req.Session != nil {
				sid = req.Session.ID()
			}
			list := pm.List(sid)
			return nil, map[string]any{"instances": list, "count": len(list)}, nil
		})
}

// addIn is add() for a tool that belongs to a non-core toolset.
func addIn[In, Out any](s *registrar, ts spec.Toolset, name, desc string, h mcp.ToolHandlerFor[In, Out]) {
	sp := spec.Typed(name, desc, v1Tier(name), h)
	sp.Toolset = ts
	s.specs = append(s.specs, sp)
}
