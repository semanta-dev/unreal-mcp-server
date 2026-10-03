package uexec

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Node is a discovered editor instance (from its pong). Metadata fields are
// best-effort: the pong payload is treated as opaque by the reference client, so
// selection tolerates missing/renamed fields and falls back gracefully.
type Node struct {
	ID            string          // pong source (the remote node id)
	Data          json.RawMessage // raw pong data, retained for tolerant matching
	EngineVersion string          `json:"-"`
	EngineRoot    string          `json:"-"`
	ProjectName   string          `json:"-"`
	ProjectRoot   string          `json:"-"`
	User          string          `json:"-"`
	Machine       string          `json:"-"`
	LastPong      time.Time       `json:"-"`
}

// pongMeta is the subset of pong fields we opportunistically parse.
type pongMeta struct {
	EngineVersion string `json:"engine_version"`
	EngineRoot    string `json:"engine_root"`
	ProjectName   string `json:"project_name"`
	ProjectRoot   string `json:"project_root"`
	User          string `json:"user"`
	Machine       string `json:"machine"`
}

type nodeTable struct {
	mu sync.RWMutex
	m  map[string]*Node
}

func newNodeTable() *nodeTable { return &nodeTable{m: make(map[string]*Node)} }

func (t *nodeTable) upsert(id string, data json.RawMessage, now time.Time) {
	var meta pongMeta
	_ = json.Unmarshal(data, &meta) // tolerant: ignore errors/missing fields
	n := &Node{
		ID:            id,
		Data:          append(json.RawMessage(nil), data...),
		EngineVersion: meta.EngineVersion,
		EngineRoot:    meta.EngineRoot,
		ProjectName:   meta.ProjectName,
		ProjectRoot:   meta.ProjectRoot,
		User:          meta.User,
		Machine:       meta.Machine,
		LastPong:      now,
	}
	t.mu.Lock()
	t.m[id] = n
	t.mu.Unlock()
}

// sweep removes nodes not seen within timeout.
func (t *nodeTable) sweep(now time.Time, timeout time.Duration) {
	t.mu.Lock()
	for id, n := range t.m {
		if now.Sub(n.LastPong) > timeout {
			delete(t.m, id)
		}
	}
	t.mu.Unlock()
}

// list returns a stable-ish snapshot (insertion order is not guaranteed by maps;
// sorted by node id for determinism in selection/logging).
func (t *nodeTable) list() []*Node {
	t.mu.RLock()
	out := make([]*Node, 0, len(t.m))
	for _, n := range t.m {
		out = append(out, n)
	}
	t.mu.RUnlock()
	sortNodes(out)
	return out
}

func sortNodes(ns []*Node) {
	// simple insertion sort by ID (tiny N); avoids importing sort for one use.
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && ns[j-1].ID > ns[j].ID; j-- {
			ns[j-1], ns[j] = ns[j], ns[j-1]
		}
	}
}

// normalizePath lowercases and normalizes separators for tolerant, case-insensitive
// (Windows) path comparison.
func normalizePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimRight(p, "/")
	return strings.ToLower(p)
}

// nodeMatchesProject reports whether a node's advertised project matches projectDir,
// with the reason (for logging). projectDir "" never matches.
func nodeMatchesProject(n *Node, projectDir string) (bool, string) {
	if projectDir == "" {
		return false, ""
	}
	want := normalizePath(projectDir)
	base := strings.ToLower(filepath.Base(strings.TrimRight(strings.ReplaceAll(projectDir, `\`, "/"), "/")))
	if n.ProjectRoot != "" {
		// The advertised root decides: one directory contains the other on a path-segment
		// boundary (C:/games/poly must not match C:/games/poly-world). A root that does not
		// match is another project or checkout, whatever its name says.
		got := strings.TrimRight(normalizePath(n.ProjectRoot), "/")
		w := strings.TrimRight(want, "/")
		if got == w || strings.HasPrefix(got, w+"/") || strings.HasPrefix(w, got+"/") {
			return true, "project_root path match"
		}
		return false, ""
	}
	if n.ProjectName != "" && strings.EqualFold(n.ProjectName, base) {
		return true, "project_name match"
	}
	if n.ProjectName == "" && len(n.Data) > 0 && base != "" && strings.Contains(strings.ToLower(string(n.Data)), base) {
		return true, "pong contains project dir name" // only for pongs without project fields
	}
	return false, ""
}

// pickNode selects the best node. With projectDir set, returns the first match
// (nil if none match — caller decides whether to fall back). Without a filter,
// returns the first discovered node (Python parity).
func pickNode(nodes []*Node, projectDir string) (*Node, string) {
	if len(nodes) == 0 {
		return nil, ""
	}
	if projectDir != "" {
		for _, n := range nodes {
			if ok, reason := nodeMatchesProject(n, projectDir); ok {
				return n, reason
			}
		}
		return nil, "no project match"
	}
	return nodes[0], "first discovered (no project filter)"
}
