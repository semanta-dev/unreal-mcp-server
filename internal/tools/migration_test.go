package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// allSpecs is every tool the server can register (daemon included).
func allSpecs() []*spec.Spec { return CatalogForDocs() }

// TestMigrationAccounting is the v1 → v2 bijection (plan §2.3): every one of the 155
// v1 tool names is replaced by exactly one v2 tool or dropped — never silently lost —
// and V1Calls names the exact v2 call for each replaced one.
func TestMigrationAccounting(t *testing.T) {
	v1 := V1Tools()
	if len(v1) != 155 {
		t.Fatalf("v1 baseline has %d names, want 155", len(v1))
	}
	known := map[string]bool{}
	for _, n := range v1 {
		known[n] = true
	}
	replacedBy := map[string]string{}
	for _, s := range allSpecs() {
		for _, r := range s.Replaces {
			if prev, dup := replacedBy[r]; dup {
				t.Errorf("v1 %q is replaced by both %q and %q", r, prev, s.Name)
			}
			if !known[r] {
				t.Errorf("%s Replaces %q, which is not a v1 tool", s.Name, r)
			}
			replacedBy[r] = s.Name
		}
	}
	var lost []string
	for _, name := range v1 {
		tool, replaced := replacedBy[name]
		switch {
		case replaced && DroppedV1[name]:
			t.Errorf("v1 %q is both replaced and dropped", name)
		case !replaced && !DroppedV1[name]:
			lost = append(lost, name)
		case replaced:
			call := V1Calls[name]
			if call == "" {
				t.Errorf("V1Calls has no entry for %q (→ %s)", name, tool)
			} else if first, _, _ := strings.Cut(call, " "); first != tool {
				t.Errorf("V1Calls[%q] = %q, but %q is replaced by %s", name, call, name, tool)
			}
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Fatalf("v1 tools lost without a replacement or a drop entry: %v", lost)
	}
	for name := range V1Calls {
		if !known[name] {
			t.Errorf("V1Calls has an entry for %q, which is not a v1 tool", name)
		}
	}
}

// TestV2SpecsLint applies the static design rules (plan §2.1/§2.2) to every tool.
func TestV2SpecsLint(t *testing.T) {
	if v := spec.Lint(allSpecs()); len(v) > 0 {
		t.Fatalf("v2 spec lint:\n  %s", strings.Join(v, "\n  "))
	}
}

// TestGeneratedDocsAreCurrent: docs/tools.md and docs/migration-v2.md are generated
// from the spec table; regenerate with `go generate ./internal/tools`.
func TestGeneratedDocsAreCurrent(t *testing.T) {
	specs := allSpecs()
	for name, want := range map[string]string{"tools.md": ToolsMarkdown(specs), "migration-v2.md": MigrationMarkdown(specs)} {
		got, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
		if err != nil {
			t.Fatalf("%s: %v (run go generate ./internal/tools)", name, err)
		}
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Errorf("docs/%s is stale: run go generate ./internal/tools", name)
		}
	}
}

// R1.5: game_command is Exec (gating, annotations and retries come from this static
// tier — a game's declared tiers never relax it) and Idempotent only through request_id.
func TestGameCommandSpecPinned(t *testing.T) {
	for _, s := range allSpecs() {
		if s.Name != "game_command" {
			continue
		}
		if len(s.Ops) != 1 || s.Ops[0].Tier != spec.Exec || !s.Ops[0].Idempotent {
			t.Fatalf("game_command must be one Exec + Idempotent op: %+v", s.Ops)
		}
		req := strings.Join(s.Ops[0].Required, ",")
		if !strings.Contains(req, "request_id") {
			t.Fatalf("game_command must require request_id: %v", s.Ops[0].Required)
		}
		return
	}
	t.Fatal("no game_command spec")
}
