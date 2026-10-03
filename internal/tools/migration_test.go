package tools

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// droppedV1 are v1 tools retired without a v2 replacement (plan §2.3 row 46):
// advertised by v1, but their companion ops never existed.
var droppedV1 = map[string]bool{
	"widget_view": true, "widget_set_fields": true, "widget_inspect": true, "widget_read": true,
	"widget_capture": true, "widget_bind_field": true, "widget_track_actor": true, "set_input_mode": true,
	"widget_set_focus": true, "pie_set_source": true, "set_hud_widget": true, "widget_bind_event": true,
	"ui_click": true, "widget_viewmodel_create": true, "widget_bind_mvvm": true, "widget_make_rt_material": true,
}

type fakeProjects struct{}

func (fakeProjects) Attach(context.Context, string, string) (string, error) { return "", nil }
func (fakeProjects) Release(string)                                         {}
func (fakeProjects) List(string) []session.ProjectInstance                  { return nil }

// allSpecs is every tool the server can register (stdio + daemon deps wired).
func allSpecs() []*spec.Spec {
	return Specs(Deps{
		Bridge: bridge.New(noEditorRunner{}, bridge.Options{}), Jobs: jobs.NewRegistry(),
		CockpitURL: func() (string, bool) { return "", false }, Projects: fakeProjects{},
	})
}

// TestMigrationAccounting is the v1 → v2 bijection (plan §2.3): every one of the 155
// v1 tool names is still registered, or replaced by exactly one v2 tool, or dropped
// — never silently lost, never both replaced and still registered.
func TestMigrationAccounting(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "v1_tools.txt"))
	if err != nil {
		t.Fatal(err)
	}
	v1 := strings.Fields(string(raw))
	if len(v1) != 155 {
		t.Fatalf("v1 baseline has %d names, want 155", len(v1))
	}
	registered := map[string]bool{}
	replacedBy := map[string]string{}
	for _, s := range allSpecs() {
		registered[s.Name] = true
		for _, r := range s.Replaces {
			if prev, dup := replacedBy[r]; dup {
				t.Errorf("v1 %q is replaced by both %q and %q", r, prev, s.Name)
			}
			replacedBy[r] = s.Name
		}
	}
	var lost []string
	for _, name := range v1 {
		_, replaced := replacedBy[name]
		switch {
		case replaced && registered[name] && replacedBy[name] != name: // a v2 tool may keep its v1 name
			t.Errorf("v1 %q is replaced by %q but still registered", name, replacedBy[name])
		case !replaced && !registered[name] && !droppedV1[name]:
			lost = append(lost, name)
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		t.Fatalf("v1 tools lost without a replacement or a drop entry: %v", lost)
	}
	for r := range replacedBy {
		found := false
		for _, n := range v1 {
			if n == r {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Replaces names %q, which is not a v1 tool", r)
		}
	}
}

// TestV2SpecsLint applies the static design rules (plan §2.1/§2.2) to every tool.
func TestV2SpecsLint(t *testing.T) {
	if v := spec.Lint(allSpecs()); len(v) > 0 {
		t.Fatalf("v2 spec lint:\n  %s", strings.Join(v, "\n  "))
	}
}
