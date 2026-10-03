package bridgetest

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Actor is one actor in the emulated level.
type Actor struct {
	Label    string     `json:"label"`
	Class    string     `json:"class"`
	Location [3]float64 `json:"location"`
}

// World is a minimal stateful level model so multi-step flows (spawn → list →
// delete) observe real state instead of canned replies.
type World struct {
	mu     sync.Mutex
	actors map[string]*Actor
	level  string
	pie    bool
}

// NewWorld returns an empty level.
func NewWorld() *World {
	return &World{actors: map[string]*Actor{}, level: "/Game/Maps/L_Test"}
}

// Install registers the v1 actor/status ops on the emulator.
func (w *World) Install(e *Emulator) {
	e.Handle("editor_status", w.editorStatus)
	e.Handle("list_actors", w.listActors)
	e.Handle("get_actor", w.getActor)
	e.Handle("spawn_actor", w.spawnActor)
	e.Handle("delete_actor", w.deleteActor)
}

// Labels returns the current actor labels, sorted.
func (w *World) Labels() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.actors))
	for l := range w.actors {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func (w *World) editorStatus(map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]any{
		"engine_version": "5.7.0-fake", "project": "FakeProject",
		"level": w.level, "pie_running": w.pie,
	}, nil
}

func (w *World) listActors(args map[string]any) (any, *OpError) {
	filter, _ := args["name_filter"].(string)
	filter = strings.ToLower(filter)
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []Actor{}
	for _, a := range w.actors {
		if filter == "" || strings.Contains(strings.ToLower(a.Label), filter) || strings.Contains(strings.ToLower(a.Class), filter) {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func (w *World) getActor(args map[string]any) (any, *OpError) {
	label, _ := args["actor_label"].(string)
	w.mu.Lock()
	defer w.mu.Unlock()
	a, ok := w.actors[label]
	if !ok {
		return nil, &OpError{Code: "NOT_FOUND", Message: "Actor not found: " + label}
	}
	return a, nil
}

func (w *World) spawnActor(args map[string]any) (any, *OpError) {
	class, _ := args["class_path"].(string)
	if class == "" || strings.Contains(class, "Missing") {
		return nil, &OpError{Code: "CLASS_UNRESOLVED", Message: "Could not resolve class: " + class}
	}
	label, _ := args["label"].(string)
	w.mu.Lock()
	defer w.mu.Unlock()
	if label == "" {
		label = fmt.Sprintf("%s_%d", class[strings.LastIndex(class, ".")+1:], len(w.actors))
	}
	num := func(k string) float64 { f, _ := args[k].(float64); return f }
	a := &Actor{Label: label, Class: class, Location: [3]float64{num("x"), num("y"), num("z")}}
	w.actors[label] = a
	return map[string]any{"label": a.Label, "class": a.Class, "location": a.Location}, nil
}

func (w *World) deleteActor(args map[string]any) (any, *OpError) {
	label, _ := args["actor_label"].(string)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.actors[label]; !ok {
		return nil, &OpError{Code: "NOT_FOUND", Message: "Actor not found: " + label}
	}
	delete(w.actors, label)
	return map[string]any{"message": "Deleted actor: " + label}, nil
}
