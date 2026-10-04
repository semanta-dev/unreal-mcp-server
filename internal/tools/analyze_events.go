package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// analyze op=events reads a playtest's recorded events (its playtest.json, or the
// capture folder holding it), so judging a run needs no python: every event's fields
// as recorded, the kinds asked for, and the counts of every kind.
func playtestEvents(path string, kinds []string, limit int) (*spec.Result, error) {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, "playtest.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, envelope.New(envelope.NotFound, "%v", err).WithHint("pass a playtest result's playtest_path")
	}
	var doc struct {
		Events  []map[string]any `json:"events"`
		Window  []float64        `json:"event_window"`
		Sources map[string]any   `json:"event_sources"`
		Gaps    []any            `json:"event_gaps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "%s is not a playtest result: %v", path, err)
	}
	if doc.Events == nil {
		return nil, envelope.New(envelope.Precondition, "%s recorded no events", path).
			WithHint("run the scenario with record_events: true")
	}
	if limit <= 0 {
		limit = 200
	}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	counts := map[string]int{}
	out := []map[string]any{}
	matched := 0
	for _, e := range doc.Events {
		k, _ := e["kind"].(string)
		counts[k]++
		if len(want) > 0 && !want[k] {
			continue
		}
		matched++
		if len(out) < limit {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return num(out[i]["t"]) < num(out[j]["t"]) })
	data := map[string]any{"path": filepath.ToSlash(path), "counts": counts, "events": out, "matched": matched,
		"truncated": matched > len(out), "window": doc.Window, "sources": doc.Sources}
	if len(doc.Gaps) > 0 {
		data["gaps"] = doc.Gaps
	}
	what := "events"
	if len(kinds) > 0 {
		what = strings.Join(kinds, "/") + " events"
	}
	return &spec.Result{Data: data, Summary: fmt.Sprintf("%d %s (of %d recorded)", matched, what, len(doc.Events))}, nil
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
