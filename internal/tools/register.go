// Package tools defines the v2 MCP tool surface (docs/plans/OVERHAUL_PLAN.md §2.3):
// one spec table — a spec.Spec per tool, with per-op tiers, timing, required and
// rejected params — from which registration, MCP annotations, the approval gate,
// toolsets, the generated docs and the v1 migration guide all derive.
package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// Deps are the collaborators the tools need (defined in package session so the
// daemon can build them without importing tools).
type Deps = session.Deps

// RegisterAll adds every tool to the server, whatever its toolset (servers that
// honour toolsets use app.NewServer, which registers through spec.Toolsets).
func RegisterAll(srv *mcp.Server, d Deps) {
	spec.Register(srv, Specs(d), spec.Options{Fallback: d})
}

// Specs returns every tool for the given deps. The daemon-only project tool is
// included when d.Projects is set.
func Specs(d Deps) []*spec.Spec {
	specs := append(coreSpecs(), assetSpecs()...)
	specs = append(specs, playSpecs()...)
	specs = append(specs, opsSpecs()...)
	specs = append(specs, lifecycleSpecs()...)
	specs = append(specs, gameSpecs()...)
	return append(specs, extraSpecs(d)...)
}
