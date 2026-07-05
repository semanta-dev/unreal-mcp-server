// Package utilization computes asset_utilization as a diagnostic-only signal,
// matching AGENTIC_GAMEDEV_PLAN.md section 7.5.
package utilization

import "github.com/jdziat/unreal-mcp-server/internal/gametrace"

// Report summarizes how much of a pack inventory is referenced by a build.
type Report struct {
	Pack       string
	Total      int
	Referenced int
	Fraction   float64
	Note       string
}

// Utilization reports the fraction of distinct pack assets referenced by a build.
func Utilization(inv gametrace.PackInventory) Report {
	all := make(map[string]struct{}, len(inv.AllAssets))
	for _, asset := range inv.AllAssets {
		all[asset] = struct{}{}
	}

	referenced := make(map[string]struct{}, len(inv.Referenced))
	for _, asset := range inv.Referenced {
		if _, ok := all[asset]; ok {
			referenced[asset] = struct{}{}
		}
	}

	total := len(all)
	fraction := 0.0
	if total > 0 {
		fraction = float64(len(referenced)) / float64(total)
	}

	return Report{
		Pack:       inv.Pack,
		Total:      total,
		Referenced: len(referenced),
		Fraction:   fraction,
		Note:       "DIAGNOSTIC signal only; asset utilization is not a gate.",
	}
}
