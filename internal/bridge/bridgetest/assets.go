package bridgetest

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

var pkgPath = regexp.MustCompile(`^/[A-Za-z0-9_]+(/[A-Za-z0-9_\-]+)+$`)

// AddAsset puts an asset of kind at path (e.g. a pre-existing Blueprint).
func (w *World) AddAsset(path, kind string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.assets[path] = kind
}

// Asset returns the kind of the asset at path ("" if none).
func (w *World) Asset(path string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.assets[path]
}

func (w *World) installAssets(e *Emulator) {
	e.Handle("asset_create", w.assetCreate)
	e.Handle("list_assets", w.listAssets)
	e.Handle("select_actors", w.selectActors)
	e.Handle("get_selection", w.getSelection)
	e.Handle("reflect", w.reflect)
}

func (w *World) assetCreate(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	kind, _ := args["kind"].(string)
	dest, _ := args["dest"].(string)
	if !pkgPath.MatchString(dest) {
		return nil, &OpError{Code: "BAD_VALUE", Message: "dest must be a package path"}
	}
	replaced := false
	if _, exists := w.assets[dest]; exists {
		if args["replace"] != true {
			return nil, &OpError{Code: "CONFLICT", Message: dest + " already exists (op=replace overwrites it)"}
		}
		replaced = true
	}
	w.assets[dest] = kind
	return map[string]any{"asset": dest, "kind": kind, "replaced": replaced, "created": dest}, nil
}

func (w *World) listAssets(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	root, _ := args["path"].(string)
	if root == "" {
		root = "/Game"
	}
	var out []string
	for p := range w.assets {
		if strings.HasPrefix(p, root+"/") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return map[string]any{"total": len(out), "assets": out}, nil
}

func (w *World) selectionView() map[string]any {
	var sel []map[string]any
	for _, p := range w.selected {
		if a, ok := w.editor[p]; ok {
			sel = append(sel, map[string]any{"label": a.Label, "path": a.Path, "class": a.Class})
		}
	}
	return map[string]any{"count": len(sel), "selected": sel}
}

func (w *World) selectActors(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	mode, _ := args["mode"].(string)
	var picked []string
	if mode != "none" {
		refs, _ := args["actors"].([]any)
		for _, r := range refs {
			ref, _ := r.(string)
			a, err := resolve(w.editor, ref)
			if err != nil {
				return nil, err
			}
			picked = append(picked, a.Path)
		}
	}
	switch mode {
	case "none":
		w.selected = nil
	case "add":
		w.selected = append(w.selected, picked...)
	default:
		w.selected = picked
	}
	return w.selectionView(), nil
}

func (w *World) getSelection(map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.selectionView(), nil
}

func (w *World) reflect(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if args["op"] != "object" {
		return map[string]any{"class_path": args["class"], "properties": map[string]any{}}, nil
	}
	actors, name, err := w.worldFor(args, "editor")
	if err != nil {
		return nil, err
	}
	ref, _ := args["actor"].(string)
	a, err := resolve(actors, ref)
	if err != nil {
		return nil, err
	}
	return map[string]any{"world": name, "class": a.Class, "path": a.Path, "properties": a.Properties}, nil
}

func (w *World) actorCall(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	actors, name, err := w.worldFor(args, "pie")
	if err != nil {
		return nil, err
	}
	if name != "pie" {
		return nil, &OpError{Code: "UNSUPPORTED", Message: "actor_call runs in PIE only in v2.0"}
	}
	ref, _ := args["actor"].(string)
	a, err := w.resolveRef(actors, name, ref)
	if err != nil {
		return nil, err
	}
	fn, _ := args["function"].(string)
	v, ok := a.Properties[fn] // a "function" is modelled as a property holding its return value
	if !ok {
		return nil, &OpError{Code: "NOT_FOUND", Message: a.Label + " has no callable function " + fn}
	}
	if args["parse"] == "json" {
		s, isStr := v.(string)
		var decoded any
		if !isStr || json.Unmarshal([]byte(s), &decoded) != nil {
			return nil, &OpError{Code: "BAD_VALUE", Message: "parse=json: " + fn + " did not return JSON"}
		}
		v = decoded
	}
	return map[string]any{"world": name, "actor": a.Label, "function": fn, "result": v}, nil
}
