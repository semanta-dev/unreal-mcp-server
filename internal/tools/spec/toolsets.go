package spec

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Toolsets manages which toolsets are enabled on one session's server. Enabling or
// disabling adds/removes tools on the live server, which emits
// notifications/tools/list_changed (go-sdk debounces these by ~10 ms).
type Toolsets struct {
	mu      sync.Mutex
	srv     *mcp.Server
	opts    Options
	byTS    map[Toolset][]*Spec
	toolTS  map[string]Toolset
	enabled map[Toolset]bool
	base    map[Toolset]bool // enabled at construction (startup flags); Apply keeps them
}

// NewToolsets registers the catalog's specs for Core plus the initial toolsets.
func NewToolsets(srv *mcp.Server, catalog []*Spec, o Options, initial ...Toolset) *Toolsets {
	t := &Toolsets{srv: srv, opts: o, byTS: map[Toolset][]*Spec{}, toolTS: map[string]Toolset{},
		enabled: map[Toolset]bool{}, base: map[Toolset]bool{Core: true}}
	for _, s := range catalog {
		ts := s.Toolset
		if ts == "" {
			ts = Core
		}
		t.byTS[ts] = append(t.byTS[ts], s)
		t.toolTS[s.Name] = ts
	}
	t.enableLocked(Core)
	for _, ts := range initial {
		if _, ok := t.byTS[ts]; ok {
			t.enableLocked(ts)
			t.base[ts] = true
		}
	}
	return t
}

func (t *Toolsets) enableLocked(ts Toolset) []string {
	if t.enabled[ts] {
		return nil
	}
	t.enabled[ts] = true
	var names []string
	for _, s := range t.byTS[ts] {
		names = append(names, s.Name)
	}
	Register(t.srv, t.byTS[ts], t.opts)
	return names
}

// Enable turns on a toolset and returns the tools it added.
func (t *Toolsets) Enable(ts Toolset) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.byTS[ts]; !ok {
		return nil, fmt.Errorf("unknown toolset %q (known: %v)", ts, t.knownLocked())
	}
	return t.enableLocked(ts), nil
}

// Disable turns off a toolset (Core cannot be disabled) and returns the tools removed.
func (t *Toolsets) Disable(ts Toolset) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ts == Core {
		return nil, fmt.Errorf("the core toolset cannot be disabled")
	}
	if _, ok := t.byTS[ts]; !ok {
		return nil, fmt.Errorf("unknown toolset %q (known: %v)", ts, t.knownLocked())
	}
	if !t.enabled[ts] {
		return nil, nil
	}
	delete(t.enabled, ts)
	var names []string
	for _, s := range t.byTS[ts] {
		names = append(names, s.Name)
	}
	t.srv.RemoveTools(names...)
	return names, nil
}

// Apply enables exactly the given toolsets plus the startup ones (Core and the
// initial set), disabling the rest. Every name is validated before anything
// changes, so an unknown toolset leaves the session untouched.
func (t *Toolsets) Apply(want []Toolset) error {
	t.mu.Lock()
	set := map[Toolset]bool{}
	for ts := range t.base {
		set[ts] = true
	}
	var unknown []Toolset
	for _, ts := range want {
		if _, ok := t.byTS[ts]; !ok {
			unknown = append(unknown, ts)
		}
		set[ts] = true
	}
	if len(unknown) > 0 {
		known := t.knownLocked()
		t.mu.Unlock()
		return fmt.Errorf("unknown toolset(s) %v (known: %v)", unknown, known)
	}
	var toRemove []string
	for ts := range t.byTS {
		switch {
		case set[ts]:
			t.enableLocked(ts)
		case t.enabled[ts]:
			delete(t.enabled, ts)
			for _, s := range t.byTS[ts] {
				toRemove = append(toRemove, s.Name)
			}
		}
	}
	t.mu.Unlock()
	if len(toRemove) > 0 {
		t.srv.RemoveTools(toRemove...)
	}
	return nil
}

// Enabled lists the enabled toolsets, sorted.
func (t *Toolsets) Enabled() []Toolset {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Toolset
	for ts := range t.enabled {
		out = append(out, ts)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

// Known lists every toolset in the catalog, sorted.
func (t *Toolsets) Known() []Toolset {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.knownLocked()
}

func (t *Toolsets) knownLocked() []Toolset {
	var out []Toolset
	for ts := range t.byTS {
		out = append(out, ts)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

// Spec returns a catalog tool (enabled or not) and its toolset.
func (t *Toolsets) Spec(name string) (*Spec, Toolset, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts, ok := t.toolTS[name]
	if !ok {
		return nil, "", false
	}
	for _, s := range t.byTS[ts] {
		if s.Name == name {
			return s, ts, true
		}
	}
	return nil, "", false
}

// IsEnabled reports whether a toolset is enabled.
func (t *Toolsets) IsEnabled(ts Toolset) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enabled[ts]
}

// Tools lists the tool names in a toolset.
func (t *Toolsets) Tools(ts Toolset) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var names []string
	for _, s := range t.byTS[ts] {
		names = append(names, s.Name)
	}
	return names
}

// Disabled reports the toolset of a tool that exists in the catalog but is not
// enabled (the envelope.SafetyNet lookup).
func (t *Toolsets) Disabled(name string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts, ok := t.toolTS[name]
	if !ok || t.enabled[ts] {
		return "", false
	}
	return string(ts), true
}

type toolsetsCtxKey struct{}

// WithToolsets returns ctx carrying the session's toolset manager.
func WithToolsets(ctx context.Context, t *Toolsets) context.Context {
	return context.WithValue(ctx, toolsetsCtxKey{}, t)
}

// ToolsetsFrom returns the session's toolset manager, if any.
func ToolsetsFrom(ctx context.Context) (*Toolsets, bool) {
	t, ok := ctx.Value(toolsetsCtxKey{}).(*Toolsets)
	return t, ok
}
