package app

import (
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// StartupPolicy is a stdio session's startup toolsets (the comma-separated extra list,
// e.g. from -toolsets, plus the project's .umcp.json) and its approval gate:
// gate_policy "require" fails closed (spec.DenyGate) until an approval surface is
// wired to tool calls — a destructive or exec op is never silently allowed.
func StartupPolicy(projectDir, extra string) ([]spec.Toolset, spec.Gate, error) {
	pf, err := session.LoadProjectFile(projectDir)
	if err != nil {
		return nil, nil, err
	}
	var ts []spec.Toolset
	for _, t := range append(strings.Split(extra, ","), pf.Toolsets...) {
		if t = strings.TrimSpace(t); t != "" {
			ts = append(ts, spec.Toolset(t))
		}
	}
	var gate spec.Gate
	if pf.GatePolicy == "require" {
		gate = spec.DenyGate{Reason: spec.NoApprovalSurface}
	}
	return ts, gate, nil
}
