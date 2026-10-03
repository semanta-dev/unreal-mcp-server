package spec

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// SchemaFor infers a flat object schema from T (json tags + jsonschema descriptions),
// then applies enums (property → allowed values) and marks required properties.
// v2 tools use it so every input is validated before a handler runs.
func SchemaFor[T any](enums map[string][]any, required ...string) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("spec.SchemaFor: %v", err))
	}
	for prop, vals := range enums {
		p, ok := s.Properties[prop]
		if !ok {
			panic(fmt.Sprintf("spec.SchemaFor: enum for unknown property %q", prop))
		}
		p.Enum = vals
		if prop == "op" {
			p.Description = "" // the enum and the tool description's per-op list say it all
		}
	}
	for _, r := range required {
		if _, ok := s.Properties[r]; !ok {
			panic(fmt.Sprintf("spec.SchemaFor: required unknown property %q", r))
		}
	}
	s.Required = required
	dropNull(s)
	return s
}

// dropNull turns the ["null", T] types jsonschema-go emits for slices, maps and
// pointers into plain T: an optional field is simply omitted, never sent as null
// (smaller tools/list, stricter validation).
func dropNull(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) == 2 && (s.Types[0] == "null" || s.Types[1] == "null") {
		t := s.Types[0]
		if t == "null" {
			t = s.Types[1]
		}
		s.Type, s.Types = t, nil
	}
	for _, p := range s.Properties {
		dropNull(p)
	}
	dropNull(s.Items)
	if s.AdditionalProperties != nil {
		dropNull(s.AdditionalProperties)
	}
}

// OpEnum lists a spec's op names as enum values for its "op" property.
func OpEnum(ops ...OpSpec) []any {
	out := make([]any, len(ops))
	for i, o := range ops {
		out[i] = o.Name
	}
	return out
}
