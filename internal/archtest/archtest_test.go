// Package archtest enforces the internal import DAG (docs/plans/OVERHAUL_PLAN.md
// §2.6): any internal → internal edge not listed here fails the build.
package archtest

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const mod = "github.com/jdziat/unreal-mcp-server/"

// allowed lists, per package (module-relative, "internal/" stripped), the internal
// packages it may import. Packages absent from the map may import nothing internal.
var allowed = map[string][]string{
	// P3 replaces these direct edges with `cmd/unreal-mcp → app, config, version`.
	"cmd/unreal-mcp": {"bridge", "cockpit/attach", "config", "daemon", "jobs", "session", "supervisor", "tools", "uexec", "version"},

	"session":           {"bridge", "jobs"},
	"daemon":            {"supervisor", "session", "lifecycle", "bridge", "uexec", "jobs"},
	"supervisor":        {"lifecycle", "bridge", "uexec", "config"},
	"cockpit/attach":    {"cockpit", "bridge"},
	"bridge":            {"uexec"},
	"bridge/bridgetest": {"uexec/uexectest"},
	"config":            {"uexec"},
	"visual":            {"audit"},
	// envelope maps bridge.OpError and uexec sentinel errors onto the closed code set.
	"tools/envelope": {"bridge", "uexec"},
	"tools/spec":     {"session", "bridge", "tools/envelope"},
	"tools": {
		"tools/spec", "tools/envelope",
		"session", "bridge", "uexec", "jobs", "lifecycle", "logs", "build", "headless", "desktop",
		"crash", "perf", "projectconfig", "projectmap", "scenespec", "audit", "visual", "eval",
		"design", "snapshot",
		"affordances", // removed in P3 (subsumed by the spec table)
	},
}

// testOnly may import anything (they are test harnesses, never linked into the binary).
var testOnly = map[string]bool{"e2e": true, "archtest": true}

func TestImportDAG(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = filepath.Join(runtime.GOROOT(), "bin", "go") // fallback when go is not on PATH
	}
	out, err := exec.Command(gobin, "list", "-f", `{{.ImportPath}}|{{join .Imports " "}}`, "../../...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var violations []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, imports, _ := strings.Cut(line, "|")
		pkg := short(path)
		if testOnly[pkg] {
			continue
		}
		ok := map[string]bool{}
		for _, a := range allowed[pkg] {
			ok[a] = true
		}
		for _, imp := range strings.Fields(imports) {
			if !strings.HasPrefix(imp, mod) {
				continue
			}
			if dep := short(imp); !ok[dep] {
				violations = append(violations, pkg+" → "+dep)
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("disallowed internal imports (update the plan's DAG first if intended):\n  %s", strings.Join(violations, "\n  "))
	}
}

func short(importPath string) string {
	return strings.TrimPrefix(strings.TrimPrefix(importPath, mod), "internal/")
}
