package bridgetest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// StopPIE discards the PIE world.
func (w *World) StopPIE() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pie = nil
}

// Move sets an editor actor's location by label (an out-of-band edit, as a user would).
func (w *World) Move(label string, loc [3]float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, a := range w.editor {
		if a.Label == label {
			a.Location = loc
		}
	}
}

func (w *World) installPlay(e *Emulator) {
	e.Handle("pie_preflight", func(map[string]any) (any, *OpError) { return map[string]any{}, nil })
	e.Handle("pie_start", func(map[string]any) (any, *OpError) {
		w.StartPIE()
		return map[string]any{"pie": "starting"}, nil
	})
	e.Handle("pie_stop", func(map[string]any) (any, *OpError) {
		if w.StickyPIE {
			return map[string]any{"pie": "stopping"}, nil // asked, but it never stops
		}
		w.StopPIE()
		return map[string]any{"pie": "stopping"}, nil
	})
	e.Handle("pie_observe", w.pieObserve)
	e.Handle("pie_time", func(map[string]any) (any, *OpError) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.pie == nil {
			return nil, &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
		}
		scale := w.WorldTimeScale
		if scale == 0 {
			scale = 1
		}
		world := w.PIEWorld
		if world == "" {
			world = "/Game/Maps/UEDPIE_0_L_Test.L_Test"
		}
		return map[string]any{"world_time_s": time.Since(w.pieStarted).Seconds() * scale, "paused": false, "world": world}, nil
	})
	// An axis hold the fake game has already finished: 6 ticks of the last value.
	e.Handle("pie_axis_stats", func(args map[string]any) (any, *OpError) {
		w.mu.Lock()
		defer w.mu.Unlock()
		v := 0.0
		for _, in := range w.Inputs {
			if in["op"] == "pie_input" && in["key"] == args["key"] {
				v, _ = in["value"].(float64)
			}
		}
		return map[string]any{"active": false, "ticks": 6.0, "total": 6 * v}, nil
	})
	for _, op := range []string{"pie_input", "pie_cursor", "pie_ui_click"} {
		op := op
		e.Handle(op, func(args map[string]any) (any, *OpError) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.pie == nil {
				return nil, &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
			}
			if op != "pie_input" && w.PluginAPI < 5 {
				return nil, &OpError{Code: "PLUGIN_MISSING", Message: op + " needs the UnrealMCP plugin API 5",
					Details: map[string]any{"needed": 5, "have": w.PluginAPI}}
			}
			rec := map[string]any{"op": op}
			for k, v := range args {
				rec[k] = v
			}
			w.Inputs = append(w.Inputs, rec)
			return map[string]any{"ok": true, "handled": true}, nil
		})
	}
	e.Handle("snapshot_actors", w.snapshotActors)
	e.Handle("snapshot_restore", w.snapshotRestore)
}

func (w *World) pieObserve(map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pie == nil {
		return nil, &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
	}
	counts := map[string]any{}
	for _, a := range w.pie {
		cls := a.Class[strings.LastIndex(a.Class, ".")+1:]
		n, _ := counts[cls].(int)
		counts[cls] = n + 1
	}
	return map[string]any{"gamestate": map[string]any{}, "counts": counts, "actors": []any{}}, nil
}

func (w *World) snapshotActors(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	flt, _ := args["class_filter"].(string)
	var names []string
	missing := map[string]bool{}
	if ps, ok := args["properties"].([]any); ok {
		for _, p := range ps {
			names = append(names, fmt.Sprint(p))
			missing[fmt.Sprint(p)] = true
		}
	}
	var out []map[string]any
	for _, a := range w.editor {
		if flt != "" && !strings.Contains(strings.ToLower(a.Class+a.Label), strings.ToLower(flt)) {
			continue
		}
		row := map[string]any{"path": a.Path, "label": a.Label, "class": a.Class,
			"loc": a.Location, "rot": []float64{0, 0, 0}, "scale": []float64{1, 1, 1}}
		props := map[string]any{}
		for _, n := range names {
			if v, ok := a.Properties[n]; ok {
				props[n] = map[string]any{"t": "float", "v": v}
				delete(missing, n)
			}
		}
		if len(props) > 0 {
			row["props"] = props
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["path"].(string) < out[j]["path"].(string) })
	res := map[string]any{"world": "editor", "count": len(out), "actors": out, "world_partition": false, "class_filter": flt}
	if len(names) > 0 {
		miss := []string{}
		for n := range missing {
			miss = append(miss, n)
		}
		sort.Strings(miss)
		res["properties_missing"], res["property_errors"] = miss, []any{}
	}
	return res, nil
}

func (w *World) snapshotRestore(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	snap, _ := args["actors"].([]any)
	seen := map[string]bool{}
	restored, removed := 0, []string{}
	for _, x := range snap {
		m, _ := x.(map[string]any)
		path, _ := m["path"].(string)
		seen[path] = true
		a, ok := w.editor[path]
		if !ok {
			removed = append(removed, path)
			continue
		}
		if loc, ok := m["loc"].([]any); ok && len(loc) == 3 {
			for i := range loc {
				a.Location[i], _ = loc[i].(float64)
			}
		}
		restored++
	}
	added := []string{}
	for p := range w.editor {
		if !seen[p] {
			added = append(added, p)
		}
	}
	sort.Strings(added)
	return map[string]any{"restored": restored, "not_restored": map[string]any{"added": added, "removed": removed}, "saved": true}, nil
}
