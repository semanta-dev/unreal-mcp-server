package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Typed adapts a v1 typed handler (mcp.ToolHandlerFor) onto a Spec so the v1 surface
// is served through the spec layer unchanged: same name, same input schema (inferred
// from In), same result shape (text-only, structured, or image results pass through),
// but with v2 annotations, the envelope on every error path, and per-call recovery.
// It exists only until the v2 surface replaces the v1 tools (P5e deletes it).
func Typed[In, Out any](name, desc string, tier Tier, h mcp.ToolHandlerFor[In, Out]) *Spec {
	schema, resolved := typedSchema[In](name)
	s := &Spec{
		Name: name, Description: desc, Toolset: Core, Schema: schema,
		Ops: []OpSpec{{Tier: tier}},
		Handler: func(ctx context.Context, c *Call) (*Result, error) {
			var in In
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			res, out, err := h(ctx, c.Request, in)
			if err != nil {
				return nil, err
			}
			if res == nil {
				res = &mcp.CallToolResult{}
			}
			if !isNil(out) {
				b, err := json.Marshal(out)
				if err != nil {
					return nil, fmt.Errorf("marshaling output: %w", err)
				}
				res.StructuredContent = json.RawMessage(b)
				if res.Content == nil {
					res.Content = []mcp.Content{&mcp.TextContent{Text: string(b)}}
				}
			}
			return &Result{Passthrough: res}, nil
		},
	}
	s.resolveOnce.Do(func() { s.resolved = resolved })
	return s
}

// typedCache memoizes inferred+resolved schemas per input type: per-session servers
// rebuild the v1 specs, and inference/resolution is the expensive part.
var typedCache sync.Map // reflect.Type -> *typedEntry

type typedEntry struct {
	schema   *jsonschema.Schema
	resolved *jsonschema.Resolved
}

func typedSchema[In any](name string) (*jsonschema.Schema, *jsonschema.Resolved) {
	rt := reflect.TypeFor[In]()
	if e, ok := typedCache.Load(rt); ok {
		te := e.(*typedEntry)
		return te.schema, te.resolved
	}
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("spec.Typed %s: %v", name, err))
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		panic(fmt.Sprintf("spec.Typed %s: resolve: %v", name, err))
	}
	e, _ := typedCache.LoadOrStore(rt, &typedEntry{schema: schema, resolved: resolved})
	te := e.(*typedEntry)
	return te.schema, te.resolved
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	b, err := json.Marshal(v)
	return err == nil && string(b) == "null"
}
