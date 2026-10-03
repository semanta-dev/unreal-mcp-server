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
	}
	for _, r := range required {
		if _, ok := s.Properties[r]; !ok {
			panic(fmt.Sprintf("spec.SchemaFor: required unknown property %q", r))
		}
	}
	s.Required = required
	return s
}

// OpEnum lists a spec's op names as enum values for its "op" property.
func OpEnum(ops ...OpSpec) []any {
	out := make([]any, len(ops))
	for i, o := range ops {
		out[i] = o.Name
	}
	return out
}
