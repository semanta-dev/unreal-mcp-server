package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The snapshot store (docs/plans/OVERHAUL_PLAN.md §2.5): one Go-owned, file-backed
// store at <project>/Saved/MCP/snapshots/<name>.json. Actors are matched by object
// path (labels are for readability only). Under World Partition the editor only sees
// loaded cells, so each snapshot records the actors World Partition knows about but
// had not loaded; Diff reports those as unknown, never removed.

// Actor is one level actor in a snapshot.
type Actor struct {
	Path  string     `json:"path"`
	Label string     `json:"label"`
	Class string     `json:"class"`
	Tags  []string   `json:"tags,omitempty"`
	Loc   [3]float64 `json:"loc"`
	Rot   [3]float64 `json:"rot"` // pitch, yaw, roll
	Scale [3]float64 `json:"scale"`
	// Props are the snapshot's chosen properties this actor has, each {t: type, v: value}.
	Props map[string]any `json:"props,omitempty"`
}

// File is a stored snapshot.
type File struct {
	Name           string    `json:"name"`
	TakenAt        time.Time `json:"taken_at"`
	World          string    `json:"world"`
	WorldPartition bool      `json:"world_partition"`
	ClassFilter    string    `json:"class_filter,omitempty"` // only actors whose class/label contain this
	Unloaded       []string  `json:"unloaded,omitempty"`     // WP: known but not loaded when taken (any filter)
	Properties     []string  `json:"properties,omitempty"`   // the properties recorded (Actor.Props)
	PIERunning     bool      `json:"pie_running,omitempty"`  // taken while PIE ran (it is still the editor level)
	Actors         []Actor   `json:"actors"`
}

var validName = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)

// ErrNotFound is returned by Load for a snapshot that does not exist.
var ErrNotFound = errors.New("snapshot not found")

// ValidName reports whether name is a safe snapshot name (it becomes a file name).
func ValidName(name string) bool { return validName.MatchString(name) }

// Dir is the store directory for a project.
func Dir(projectDir string) string { return filepath.Join(projectDir, "Saved", "MCP", "snapshots") }

func path(projectDir, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid snapshot name %q (1-64 letters, digits, '_' or '-')", name)
	}
	return filepath.Join(Dir(projectDir), name+".json"), nil
}

// Save writes f atomically (temp file + rename).
func Save(projectDir string, f *File) (string, error) {
	p, err := path(projectDir, f.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// Load reads a stored snapshot.
func Load(projectDir, name string) (*File, error) {
	p, err := path(projectDir, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("snapshot %s is corrupt: %w", name, err)
	}
	return &f, nil
}

// Entry is one stored snapshot in a listing.
type Entry struct {
	Name    string    `json:"name"`
	TakenAt time.Time `json:"taken_at"`
	Actors  int       `json:"actors"`
}

// List returns the stored snapshots, newest first.
func List(projectDir string) ([]Entry, error) {
	matches, err := filepath.Glob(filepath.Join(Dir(projectDir), "*.json"))
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".json")
		if f, err := Load(projectDir, name); err == nil {
			out = append(out, Entry{Name: name, TakenAt: f.TakenAt, Actors: len(f.Actors)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenAt.After(out[j].TakenAt) })
	return out, nil
}

// Ref names an actor in a diff.
type Ref struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// Move is an actor whose transform changed.
type Move struct {
	Ref
	From [3][3]float64 `json:"from"` // loc, rot, scale
	To   [3][3]float64 `json:"to"`
}

// Retag is an actor whose tags changed.
type Retag struct {
	Ref
	From []string `json:"from"`
	To   []string `json:"to"`
}

// Change is one recorded property whose value differs (From/To nil: the actor did not
// have it on that side).
type Change struct {
	Ref
	Property string `json:"property"`
	From     any    `json:"from"`
	To       any    `json:"to"`
}

// DiffResult is the change from snapshot A to B.
type DiffResult struct {
	Added    []Ref    `json:"added"`
	Removed  []Ref    `json:"removed"`
	Moved    []Move   `json:"moved"`
	Retagged []Retag  `json:"retagged"`
	Changed  []Change `json:"changed"` // recorded properties (File.Properties)
	// Unknown are actors present in one snapshot whose counterpart was in an unloaded
	// World Partition cell of the other: they may or may not still exist.
	Unknown []Ref `json:"unknown"`
}

// Tolerance below which a transform component counts as unchanged.
const Tolerance = 0.01

// Diff compares a to b by object path.
func Diff(a, b *File) DiffResult {
	res := DiffResult{Added: []Ref{}, Removed: []Ref{}, Moved: []Move{}, Retagged: []Retag{}, Changed: []Change{}, Unknown: []Ref{}}
	inA, inB := index(a), index(b)
	unloadedA, unloadedB := set(a.Unloaded), set(b.Unloaded)
	for p, x := range inA {
		y, ok := inB[p]
		switch {
		case !ok && unloadedB[p]:
			res.Unknown = append(res.Unknown, Ref{p, x.Label})
		case !ok:
			res.Removed = append(res.Removed, Ref{p, x.Label})
		default:
			if moved(x, y) {
				res.Moved = append(res.Moved, Move{Ref{p, y.Label}, [3][3]float64{x.Loc, x.Rot, x.Scale}, [3][3]float64{y.Loc, y.Rot, y.Scale}})
			}
			if !sameTags(x.Tags, y.Tags) {
				res.Retagged = append(res.Retagged, Retag{Ref{p, y.Label}, x.Tags, y.Tags})
			}
			for _, prop := range a.Properties {
				if !sameJSON(x.Props[prop], y.Props[prop]) {
					res.Changed = append(res.Changed, Change{Ref{p, y.Label}, prop, x.Props[prop], y.Props[prop]})
				}
			}
		}
	}
	for p, y := range inB {
		if _, ok := inA[p]; ok {
			continue
		}
		if unloadedA[p] {
			res.Unknown = append(res.Unknown, Ref{p, y.Label})
		} else {
			res.Added = append(res.Added, Ref{p, y.Label})
		}
	}
	for _, l := range []([]Ref){res.Added, res.Removed, res.Unknown} {
		sort.Slice(l, func(i, j int) bool { return l[i].Path < l[j].Path })
	}
	sort.Slice(res.Moved, func(i, j int) bool { return res.Moved[i].Path < res.Moved[j].Path })
	sort.Slice(res.Retagged, func(i, j int) bool { return res.Retagged[i].Path < res.Retagged[j].Path })
	sort.SliceStable(res.Changed, func(i, j int) bool { return res.Changed[i].Path < res.Changed[j].Path })
	return res
}

func sameJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func index(f *File) map[string]Actor {
	m := make(map[string]Actor, len(f.Actors))
	for _, a := range f.Actors {
		m[a.Path] = a
	}
	return m
}

func set(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

func moved(x, y Actor) bool {
	for i := 0; i < 3; i++ {
		if math.Abs(x.Loc[i]-y.Loc[i]) > Tolerance || math.Abs(angle(x.Rot[i]-y.Rot[i])) > Tolerance || math.Abs(x.Scale[i]-y.Scale[i]) > Tolerance {
			return true
		}
	}
	return false
}

// angle normalizes a degree difference into (-180, 180] so 359.99 vs -0.01 is no move.
func angle(d float64) float64 {
	d = math.Mod(d, 360)
	if d > 180 {
		d -= 360
	} else if d <= -180 {
		d += 360
	}
	return d
}

func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
