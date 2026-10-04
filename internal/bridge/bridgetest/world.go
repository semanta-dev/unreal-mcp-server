package bridgetest

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Actor is one actor in an emulated world.
type Actor struct {
	Label      string         `json:"label"`
	Path       string         `json:"path"`
	Class      string         `json:"class"`
	Location   [3]float64     `json:"location"`
	Properties map[string]any `json:"-"`
}

// World is a minimal stateful model of the editor level and (when started) its PIE
// copy, so multi-step flows (spawn → query → edit → delete) observe real state.
type World struct {
	mu       sync.Mutex
	editor   map[string]*Actor // path -> actor
	pie      map[string]*Actor // nil when PIE is not running
	level    string
	seq      int
	assets   map[string]string // asset path -> kind
	selected []string          // selected editor actor paths
	// PluginAPI is the UnrealMCP plugin API version editor_ping reports (default 3).
	PluginAPI int
	undo      []txn // the editor's transaction buffer (spawn/delete record theirs)
	redo      []txn
}

// txn is one undoable editor transaction.
type txn struct {
	title      string
	undo, redo func()
}

// record pushes a transaction and clears the redo stack, like a new editor edit.
func (w *World) record(title string, undo, redo func()) {
	w.undo = append(w.undo, txn{title, undo, redo})
	w.redo = nil
}

// UserEdit records a transaction made by the human (not titled "MCP: ").
func (w *World) UserEdit(title string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.record(title, func() {}, func() {})
}

// editorUndo models the plugin's UndoIfTitled/RedoIfTitled behind the companion's
// editor_undo op: refused in PIE, CONFLICT when the next step is not the server's.
func (w *World) editorUndo(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	redo, _ := args["redo"].(bool)
	if w.PluginAPI < 3 {
		return nil, &OpError{Code: "PLUGIN_MISSING", Message: "undo needs the UnrealMCP plugin API 3"}
	}
	if w.pie != nil {
		return nil, &OpError{Code: "PRECONDITION", Message: "undo is refused while PIE runs"}
	}
	from, to := &w.undo, &w.redo
	if redo {
		from, to = &w.redo, &w.undo
	}
	if len(*from) == 0 {
		return nil, &OpError{Code: "PRECONDITION", Message: "nothing to undo/redo"}
	}
	t := (*from)[len(*from)-1]
	if op, ok := strings.CutPrefix(t.title, "untracked:"); ok {
		return nil, &OpError{Code: "CONFLICT", Message: "the server's last edit (" + op + ") has no undo step", Details: map[string]any{"untracked": op}}
	}
	if !strings.HasPrefix(t.title, "MCP: ") {
		return nil, &OpError{Code: "CONFLICT", Message: "the next step is not the server's: " + t.title, Details: map[string]any{"title": t.title}}
	}
	*from = (*from)[:len(*from)-1]
	*to = append(*to, t)
	if redo {
		t.redo()
		return map[string]any{"redone": t.title}, nil
	}
	t.undo()
	return map[string]any{"undone": t.title}, nil
}

// NewWorld returns an empty level.
func NewWorld() *World {
	return &World{editor: map[string]*Actor{}, level: "/Game/Maps/L_Test", assets: map[string]string{}, PluginAPI: 3}
}

// Install registers the ops the world answers.
func (w *World) Install(e *Emulator) {
	e.Handle("editor_status", w.editorStatus)
	e.Handle("editor_ping", w.editorPing)
	e.Handle("editor_undo", w.editorUndo)
	e.Handle("note_edit", func(args map[string]any) (any, *OpError) {
		w.mu.Lock()
		defer w.mu.Unlock()
		op, _ := args["op"].(string)
		w.record("untracked:"+op, func() {}, func() {})
		return map[string]any{"noted": true}, nil
	})
	e.Handle("list_actors", w.listActorsV1)
	e.Handle("actor_query", w.actorQuery)
	e.Handle("actor_spawn", w.actorSpawn)
	e.Handle("actor_delete", w.actorDelete)
	e.Handle("actor_transform", w.actorTransform)
	e.Handle("actor_set_properties", w.actorSetProperties)
	e.Handle("actor_call", w.actorCall)
	w.installAssets(e)
	w.installPlay(e)
}

// StartPIE copies the editor actors into a PIE world (paths gain UEDPIE_0_).
func (w *World) StartPIE() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pie = map[string]*Actor{}
	for _, a := range w.editor {
		c := *a
		c.Path = strings.Replace(a.Path, "/L_Test.", "/UEDPIE_0_L_Test.", 1)
		c.Properties = make(map[string]any, len(a.Properties)) // a PIE edit never reaches the level
		for k, v := range a.Properties {
			c.Properties[k] = v
		}
		w.pie[c.Path] = &c
	}
}

// Labels returns the editor actor labels, sorted.
func (w *World) Labels() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, a := range w.editor {
		out = append(out, a.Label)
	}
	sort.Strings(out)
	return out
}

func (w *World) editorStatus(map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]any{"engine_version": "5.7.0-fake", "project_dir": "C:/fake/", "current_level": "L_Test",
		"is_in_pie": w.pie != nil, "actor_count": len(w.editor), "bridge_version": 1}, nil
}

func (w *World) editorPing(map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]any{"ok": true, "version": 1, "pie": w.pie != nil, "plugin_api": w.PluginAPI}, nil
}

// worldFor resolves the world argument like the companion's _v2_world.
func (w *World) worldFor(args map[string]any, def string) (map[string]*Actor, string, *OpError) {
	name, _ := args["world"].(string)
	if name == "" {
		name = def
	}
	switch name {
	case "editor":
		return w.editor, "editor", nil
	case "pie":
		if w.pie == nil {
			return nil, "", &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
		}
		return w.pie, "pie", nil
	case "auto":
		if w.pie != nil {
			return w.pie, "pie", nil
		}
		return w.editor, "editor", nil
	}
	return nil, "", &OpError{Code: "BAD_VALUE", Message: "world must be editor, pie or auto"}
}

func normPath(p string) string { return strings.Replace(p, "UEDPIE_0_", "", 1) }

func resolve(actors map[string]*Actor, ref string) (*Actor, *OpError) {
	if ref == "@pawn" || ref == "@gamestate" {
		// In PIE the pawn is the actor whose class names a Pawn/GameState; the editor
		// world has neither.
		want := map[string]string{"@pawn": "Pawn", "@gamestate": "GameState"}[ref]
		for _, a := range actors {
			if strings.Contains(a.Path, "UEDPIE_") && strings.Contains(a.Class, want) {
				return a, nil
			}
		}
		return nil, &OpError{Code: "NOT_FOUND", Message: ref + " exists only in PIE"}
	}
	if strings.ContainsAny(ref, "/:") {
		for _, a := range actors {
			if normPath(a.Path) == normPath(ref) {
				return a, nil
			}
		}
		return nil, &OpError{Code: "NOT_FOUND", Message: "no actor at path " + ref}
	}
	var hits []*Actor
	for _, a := range actors {
		if a.Label == ref {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 0:
		return nil, &OpError{Code: "NOT_FOUND", Message: "no actor labeled " + ref}
	case 1:
		return hits[0], nil
	}
	return nil, &OpError{Code: "CONFLICT", Message: fmt.Sprintf("%d actors are labeled %q", len(hits), ref)}
}

func view(a *Actor, world string) map[string]any {
	return map[string]any{"label": a.Label, "path": a.Path, "class": a.Class, "world": world, "location": a.Location}
}

func (w *World) listActorsV1(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []map[string]any{}
	for _, a := range w.editor {
		out = append(out, view(a, "editor"))
	}
	return out, nil
}

func (w *World) actorQuery(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	actors, name, err := w.worldFor(args, "editor")
	if err != nil {
		return nil, err
	}
	if args["op"] == "get" {
		ref, _ := args["actor"].(string)
		a, err := resolve(actors, ref)
		if err != nil {
			return nil, err
		}
		v := view(a, name)
		v["properties"] = a.Properties
		return map[string]any{"world": name, "actor": v}, nil
	}
	flt, _ := args["filter"].(string)
	var out []map[string]any
	for _, a := range actors {
		if flt == "" || strings.Contains(strings.ToLower(a.Label+a.Class), strings.ToLower(flt)) {
			out = append(out, view(a, name))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["path"].(string) < out[j]["path"].(string) })
	return map[string]any{"world": name, "count": len(out), "returned": len(out), "truncated": false, "actors": out}, nil
}

func (w *World) actorSpawn(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, name, err := w.worldFor(args, "")
	if err != nil {
		return nil, err
	}
	if name != "editor" {
		return nil, &OpError{Code: "UNSUPPORTED", Message: "spawning into PIE is not supported"}
	}
	class, _ := args["class"].(string)
	if class == "" || strings.Contains(class, "Missing") {
		return nil, &OpError{Code: "CLASS_UNRESOLVED", Message: "could not resolve class " + class}
	}
	w.seq++
	label, _ := args["label"].(string)
	if label == "" {
		label = fmt.Sprintf("Actor_%d", w.seq)
	}
	a := &Actor{Label: label, Class: class, Path: fmt.Sprintf("/Game/Maps/L_Test.L_Test:PersistentLevel.%s_%d", label, w.seq),
		Properties: map[string]any{}}
	if loc, ok := args["location"].([]any); ok && len(loc) == 3 {
		for i := range loc {
			a.Location[i], _ = loc[i].(float64)
		}
	}
	w.editor[a.Path] = a
	w.record("MCP: spawn "+class, func() { delete(w.editor, a.Path) }, func() { w.editor[a.Path] = a })
	return map[string]any{"world": "editor", "spawned": view(a, "editor"), "property_errors": []any{}}, nil
}

func (w *World) editTarget(args map[string]any) (map[string]*Actor, string, *Actor, *OpError) {
	actors, name, err := w.worldFor(args, "")
	if err != nil {
		return nil, "", nil, err
	}
	ref, _ := args["actor"].(string)
	a, err := resolve(actors, ref)
	return actors, name, a, err
}

func (w *World) actorDelete(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	actors, name, a, err := w.editTarget(args)
	if err != nil {
		return nil, err
	}
	delete(actors, a.Path)
	if name == "editor" {
		w.record("MCP: delete "+a.Label, func() { actors[a.Path] = a }, func() { delete(actors, a.Path) })
	}
	return map[string]any{"world": name, "deleted": view(a, name)}, nil
}

func (w *World) actorTransform(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, name, a, err := w.editTarget(args)
	if err != nil {
		return nil, err
	}
	if loc, ok := args["location"].([]any); ok && len(loc) == 3 {
		for i := range loc {
			a.Location[i], _ = loc[i].(float64)
		}
	}
	return map[string]any{"world": name, "actor": view(a, name)}, nil
}

func (w *World) actorSetProperties(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, name, a, err := w.editTarget(args)
	if err != nil {
		return nil, err
	}
	props, _ := args["properties"].(map[string]any)
	for k, v := range props {
		a.Properties[k] = v
	}
	return map[string]any{"world": name, "actor": view(a, name), "property_errors": []any{}}, nil
}

// AddActor places an editor actor directly (fixtures, e.g. the tool-selection eval's
// starting levels) and returns its object path.
func (w *World) AddActor(label, class string, loc [3]float64, props map[string]any) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	if props == nil {
		props = map[string]any{}
	}
	a := &Actor{Label: label, Class: class, Location: loc, Properties: props,
		Path: fmt.Sprintf("/Game/Maps/L_Test.L_Test:PersistentLevel.%s_%d", label, w.seq)}
	w.editor[a.Path] = a
	return a.Path
}

// State is a copy of the world for assertions.
type State struct {
	Editor, PIE []Actor
	PIERunning  bool
	Assets      map[string]string
	Level       string
}

// Snapshot copies the world's state.
func (w *World) Snapshot() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := State{PIERunning: w.pie != nil, Level: w.level, Assets: map[string]string{}}
	for _, a := range w.editor {
		st.Editor = append(st.Editor, *a)
	}
	for _, a := range w.pie {
		st.PIE = append(st.PIE, *a)
	}
	for k, v := range w.assets {
		st.Assets[k] = v
	}
	return st
}
