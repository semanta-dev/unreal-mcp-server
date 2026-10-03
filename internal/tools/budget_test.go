package tools

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// Byte budgets for tools/list (plan §1 goal 2): what a client loads into context.
const (
	coreBudget = 45000 // core toolset only (the default session)
	allBudget  = 75000 // every toolset enabled, daemon included
)

func toolBytes(t *testing.T, s *spec.Spec) int {
	t.Helper()
	b, err := json.Marshal(s.Tool())
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// TestToolListBudgets pins the plan's counts and byte budgets: 45 tools (36 core),
// core tools/list ≤ 45 KB, everything ≤ 75 KB.
func TestToolListBudgets(t *testing.T) {
	specs := Specs(Deps{Bridge: bridge.New(noEditorRunner{}, bridge.Options{}), Jobs: jobs.NewRegistry(), Projects: docsProjects{}})
	core, all, nCore := 0, 0, 0
	type row struct {
		name string
		n    int
	}
	var rows []row
	for _, s := range specs {
		n := toolBytes(t, s)
		all += n
		if s.Toolset == spec.Core || s.Toolset == "" {
			core += n
			nCore++
		}
		rows = append(rows, row{s.Name, n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n })
	t.Logf("tools=%d core=%d core_bytes=%d all_bytes=%d largest=%v", len(specs), nCore, core, all, rows[:5])
	if len(specs) != 45 || nCore != 36 {
		t.Errorf("tool counts = %d (%d core), want 45 (36 core)", len(specs), nCore)
	}
	if core > coreBudget {
		t.Errorf("core tools/list = %d bytes, budget %d", core, coreBudget)
	}
	if all > allBudget {
		t.Errorf("all toolsets = %d bytes, budget %d", all, allBudget)
	}
}
