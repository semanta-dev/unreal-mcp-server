// Package scenespec is the compiler spine for declarative Unreal scene building
// (goal B). It parses and validates a scene spec (schema "unreal.scene/v1"),
// expands layouts / prefabs / environment presets into a FLAT, deterministically
// labeled placement list that a single editor op can realize, and diffs a plan
// against a level snapshot for idempotent apply / dry-run.
//
// Everything here is pure (no editor, no disk): given the same Spec, Compile
// returns byte-for-byte the same Plan, and the deterministic labels are the diff
// key that makes apply idempotent. The Spec/Layout/PrefabMember/Environment
// structs are exported so callers can build specs programmatically as well as
// decode them from JSON.
//
// Unreal convention that pervades the math: rotation is a rotation_pyr triple
// [pitch, yaw, roll]; location and scale are [x, y, z]. Positive yaw turns an
// actor from +X toward +Y. The single most common gotcha this package guards is
// a directional (sun) light with pitch >= 0, which makes the level render black.
package scenespec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// Schema is the only accepted value of the spec's "schema" field.
const Schema = "unreal.scene/v1"

// sunPitchMsg is the #1 documented gotcha: a sun with non-negative pitch points
// at or above the horizon and the scene renders black.
const sunPitchMsg = "sun pitch must be < 0 or the level renders black"

// sceneIDRe constrains scene_id to a filesystem/label-safe token.
var sceneIDRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// kindEnum is the set of accepted actor/member/group kinds.
var kindEnum = map[string]bool{
	"static_mesh":       true,
	"class":             true,
	"directional_light": true,
	"sky_light":         true,
	"sky_atmosphere":    true,
	"height_fog":        true,
	"post_process":      true,
}

// layoutEnum is the set of accepted layout types.
var layoutEnum = map[string]bool{
	"grid": true, "ring": true, "line": true, "scatter": true,
}

// presetEnum is the set of accepted environment presets.
var presetEnum = map[string]bool{
	"daytime_clear": true, "overcast": true, "dusk": true, "night": true, "studio": true,
}

// Diagnostic is a single semantic problem found during Parse or Compile. Severity
// is "error" or "warning"; Field is a dotted/indexed path into the spec.
type Diagnostic struct {
	Severity string `json:"severity"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// --- spec input structs (decoded from JSON or built programmatically) ---

// Spec is a full declarative scene. Optional transform fields on the input
// structs are pointers so Compile can distinguish "unset" (apply a default) from
// an explicit zero value.
type Spec struct {
	Schema      string            `json:"schema"`
	SceneID     string            `json:"scene_id"`
	Defaults    Defaults          `json:"defaults,omitempty"`
	Environment *Environment      `json:"environment,omitempty"`
	Prefabs     map[string]Prefab `json:"prefabs,omitempty"`
	Actors      []Actor           `json:"actors,omitempty"`
	Groups      []Group           `json:"groups,omitempty"`
	Cameras     []Camera          `json:"cameras,omitempty"`
	Checks      Checks            `json:"checks,omitempty"`
}

// Defaults supplies fallbacks folded into every placement.
type Defaults struct {
	Folder string      `json:"folder,omitempty"`
	Scale  *[3]float64 `json:"scale,omitempty"`
}

// Environment selects a lighting preset and optional numeric overrides.
type Environment struct {
	Preset    string         `json:"preset"`
	Overrides map[string]any `json:"overrides,omitempty"`
}

// Prefab is a named, reusable cluster of members placed relative to an instance.
type Prefab struct {
	Members []PrefabMember `json:"members"`
}

// PrefabMember is one element of a prefab, positioned in the prefab-local frame.
type PrefabMember struct {
	Role           string      `json:"role"`
	Kind           string      `json:"kind"`
	ClassPath      string      `json:"class_path,omitempty"`
	StaticMeshPath string      `json:"static_mesh_path,omitempty"`
	Location       *[3]float64 `json:"location,omitempty"`
	RotationPyr    *[3]float64 `json:"rotation_pyr,omitempty"`
	Scale          *[3]float64 `json:"scale,omitempty"`
}

// Actor is a single explicit placement.
type Actor struct {
	Label          string         `json:"label"`
	Kind           string         `json:"kind"`
	ClassPath      string         `json:"class_path,omitempty"`
	StaticMeshPath string         `json:"static_mesh_path,omitempty"`
	Location       *[3]float64    `json:"location,omitempty"`
	RotationPyr    *[3]float64    `json:"rotation_pyr,omitempty"`
	Scale          *[3]float64    `json:"scale,omitempty"`
	Properties     map[string]any `json:"properties,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
}

// Group places many actors via a layout, optionally instantiating a prefab.
type Group struct {
	Name           string         `json:"name"`
	Use            string         `json:"use,omitempty"`
	Kind           string         `json:"kind,omitempty"`
	ClassPath      string         `json:"class_path,omitempty"`
	StaticMeshPath string         `json:"static_mesh_path,omitempty"`
	Layout         Layout         `json:"layout"`
	Properties     map[string]any `json:"properties,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	RotationPyr    *[3]float64    `json:"rotation_pyr,omitempty"`
	Scale          *[3]float64    `json:"scale,omitempty"`
}

// Layout parameterizes how a group's instances are distributed in space. Only
// the fields relevant to Type are consulted.
type Layout struct {
	Type          string     `json:"type"`
	Count         int        `json:"count,omitempty"`
	Rows          int        `json:"rows,omitempty"`
	Cols          int        `json:"cols,omitempty"`
	Spacing       float64    `json:"spacing,omitempty"`
	Center        [3]float64 `json:"center,omitempty"`
	Radius        float64    `json:"radius,omitempty"`
	StartAngleDeg float64    `json:"start_angle_deg,omitempty"`
	AlignToCenter bool       `json:"align_to_center,omitempty"`
	Start         [3]float64 `json:"start,omitempty"`
	End           [3]float64 `json:"end,omitempty"`
	Extent        [3]float64 `json:"extent,omitempty"`
	Seed          int64      `json:"seed,omitempty"`
}

// Camera is a framing intent evaluated by a later pass; it produces no placement.
type Camera struct {
	Name         string   `json:"name"`
	Targets      []string `json:"targets,omitempty"`
	AzimuthDeg   float64  `json:"azimuth_deg,omitempty"`
	ElevationDeg float64  `json:"elevation_deg,omitempty"`
	FOVDeg       float64  `json:"fov_deg,omitempty"`
	Fill         float64  `json:"fill,omitempty"`
}

// Checks are post-realization assertions (evaluated elsewhere); carried through
// so a spec round-trips.
type Checks struct {
	RequireEnvironmentLit  bool `json:"require_environment_lit,omitempty"`
	RequireNoMissingMeshes bool `json:"require_no_missing_meshes,omitempty"`
	RequirePlayerStart     bool `json:"require_player_start,omitempty"`
	RequireNavBounds       bool `json:"require_nav_bounds,omitempty"`
	SpawnsWithinBounds     bool `json:"spawns_within_bounds,omitempty"`
}

// --- compiled output structs ---

// Transform is a location/rotation/scale triple in the shared UE convention.
type Transform struct {
	Location    [3]float64 `json:"location"`
	RotationPyr [3]float64 `json:"rotation_pyr"`
	Scale       [3]float64 `json:"scale"`
}

// Placement is one flat, realized item in a Plan. Label is the deterministic
// diff key.
type Placement struct {
	Label          string         `json:"label"`
	Kind           string         `json:"kind"`
	ClassPath      string         `json:"class_path,omitempty"`
	StaticMeshPath string         `json:"static_mesh_path,omitempty"`
	Location       [3]float64     `json:"location"`
	RotationPyr    [3]float64     `json:"rotation_pyr"`
	Scale          [3]float64     `json:"scale"`
	Material       string         `json:"material,omitempty"`
	Properties     map[string]any `json:"properties,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	Folder         string         `json:"folder,omitempty"`
}

// Plan is the flat placement list a single editor op realizes.
type Plan struct {
	SceneID    string      `json:"scene_id"`
	Placements []Placement `json:"placements"`
}

// Parse decodes data into a Spec and validates it. JSON/structural errors are
// returned as the error; semantic problems (bad schema tag, bad scene_id,
// unknown enum values, missing required fields) are collected as Diagnostics so
// callers can surface them all at once. A non-nil Spec is returned whenever the
// bytes decode, even if Diagnostics contains errors.
func Parse(data []byte) (*Spec, []Diagnostic, error) {
	var s Spec
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&s); err != nil {
		return nil, nil, err
	}
	var diags []Diagnostic

	if s.Schema != Schema {
		diags = append(diags, Diagnostic{"error", "schema",
			fmt.Sprintf("schema must be %q, got %q", Schema, s.Schema)})
	}
	switch {
	case s.SceneID == "":
		diags = append(diags, Diagnostic{"error", "scene_id", "scene_id is required"})
	case !sceneIDRe.MatchString(s.SceneID):
		diags = append(diags, Diagnostic{"error", "scene_id",
			"scene_id must match ^[a-z0-9_]+$, got " + strconv.Quote(s.SceneID)})
	}

	if s.Environment != nil {
		switch {
		case s.Environment.Preset == "":
			diags = append(diags, Diagnostic{"error", "environment.preset", "preset is required"})
		case !presetEnum[s.Environment.Preset]:
			diags = append(diags, Diagnostic{"error", "environment.preset",
				"unknown preset " + strconv.Quote(s.Environment.Preset)})
		}
	}

	// Prefabs: iterate names in sorted order so diagnostics are deterministic.
	names := make([]string, 0, len(s.Prefabs))
	for name := range s.Prefabs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pf := s.Prefabs[name]
		if len(pf.Members) == 0 {
			diags = append(diags, Diagnostic{"error",
				fmt.Sprintf("prefabs[%s].members", name), "prefab has no members"})
		}
		for j, m := range pf.Members {
			base := fmt.Sprintf("prefabs[%s].members[%d]", name, j)
			if m.Role == "" {
				diags = append(diags, Diagnostic{"error", base + ".role", "role is required"})
			}
			diags = appendKindDiag(diags, base+".kind", m.Kind, true)
		}
	}

	for i, a := range s.Actors {
		base := fmt.Sprintf("actors[%d]", i)
		if a.Label == "" {
			diags = append(diags, Diagnostic{"error", base + ".label", "label is required"})
		}
		diags = appendKindDiag(diags, base+".kind", a.Kind, true)
	}

	for i, g := range s.Groups {
		base := fmt.Sprintf("groups[%d]", i)
		if g.Name == "" {
			diags = append(diags, Diagnostic{"error", base + ".name", "name is required"})
		}
		if g.Layout.Type == "" {
			diags = append(diags, Diagnostic{"error", base + ".layout.type", "layout.type is required"})
		} else if !layoutEnum[g.Layout.Type] {
			diags = append(diags, Diagnostic{"error", base + ".layout.type",
				"unknown layout type " + strconv.Quote(g.Layout.Type)})
		}
		// A prefab group takes its kind(s) from members; a plain group needs one.
		diags = appendKindDiag(diags, base+".kind", g.Kind, g.Use == "")
	}

	for i, c := range s.Cameras {
		if c.Name == "" {
			diags = append(diags, Diagnostic{"error",
				fmt.Sprintf("cameras[%d].name", i), "name is required"})
		}
	}

	return &s, diags, nil
}

// appendKindDiag validates a kind value. When required is true an empty kind is
// an error; a non-empty kind outside the enum is always an error.
func appendKindDiag(diags []Diagnostic, field, kind string, required bool) []Diagnostic {
	switch {
	case kind == "":
		if required {
			diags = append(diags, Diagnostic{"error", field, "kind is required"})
		}
	case !kindEnum[kind]:
		diags = append(diags, Diagnostic{"error", field, "unknown kind " + strconv.Quote(kind)})
	}
	return diags
}

// Compile folds defaults, expands the environment/prefabs/layouts, and emits a
// flat Plan of deterministically labeled Placements plus any semantic
// Diagnostics (notably the sun-pitch guard). Label scheme:
//
//	explicit actor          -> "<scene_id>.<label>"
//	group instance          -> "<scene_id>.<group>.<NN>"
//	prefab member instance  -> "<scene_id>.<group>.<NN>.<role>"
//	environment element     -> "<scene_id>.env.<name>"
//
// NN is zero-padded to the width of the instance count, in deterministic layout
// order. Every placement carries the tag "mcp_scene:<scene_id>" plus any user
// tags, and the resolved folder.
func Compile(s *Spec) (Plan, []Diagnostic) {
	var diags []Diagnostic
	plan := Plan{SceneID: s.SceneID}

	folder := s.Defaults.Folder
	defScale := [3]float64{1, 1, 1}
	if s.Defaults.Scale != nil {
		defScale = *s.Defaults.Scale
	}
	sceneTag := "mcp_scene:" + s.SceneID

	// add finalizes a placement (folder + scene tag + user tags) and records it.
	add := func(p Placement, userTags []string) {
		p.Folder = folder
		tags := make([]string, 0, 1+len(userTags))
		tags = append(tags, sceneTag)
		tags = append(tags, userTags...)
		p.Tags = tags
		plan.Placements = append(plan.Placements, p)
	}

	// Environment.
	if s.Environment != nil {
		eps, ed := EnvPlacements(s.Environment.Preset, s.Environment.Overrides)
		diags = append(diags, ed...)
		for _, p := range eps {
			p.Label = s.SceneID + "." + p.Label
			add(p, nil) // sun pitch already validated inside EnvPlacements
		}
	}

	// Explicit actors.
	for _, a := range s.Actors {
		p := Placement{
			Label:          s.SceneID + "." + a.Label,
			Kind:           a.Kind,
			ClassPath:      a.ClassPath,
			StaticMeshPath: a.StaticMeshPath,
			Location:       orVec(a.Location, [3]float64{}),
			RotationPyr:    orVec(a.RotationPyr, [3]float64{}),
			Scale:          orVec(a.Scale, defScale),
			Properties:     a.Properties,
			Material:       materialOf(a.Properties),
		}
		if a.Kind == "directional_light" {
			diags = append(diags, sunPitchDiags(p.Label, p.RotationPyr)...)
		}
		add(p, a.Tags)
	}

	// Groups.
	for gi, g := range s.Groups {
		base := Transform{
			Location:    [3]float64{},
			RotationPyr: orVec(g.RotationPyr, [3]float64{}),
			Scale:       orVec(g.Scale, defScale),
		}
		insts := Expand(g.Layout, base)
		if len(insts) == 0 {
			diags = append(diags, Diagnostic{"warning",
				fmt.Sprintf("groups[%d].layout", gi), "layout produced no placements"})
			continue
		}
		width := len(strconv.Itoa(len(insts)))

		if g.Use != "" {
			pf, ok := s.Prefabs[g.Use]
			if !ok {
				diags = append(diags, Diagnostic{"error",
					fmt.Sprintf("groups[%d].use", gi), "unknown prefab " + strconv.Quote(g.Use)})
				continue
			}
			for i, inst := range insts {
				prefix := fmt.Sprintf("%s.%s.%0*d", s.SceneID, g.Name, width, i)
				for _, mp := range ExpandPrefab(pf.Members, inst, prefix) {
					if mp.Kind == "directional_light" {
						diags = append(diags, sunPitchDiags(mp.Label, mp.RotationPyr)...)
					}
					add(mp, g.Tags)
				}
			}
			continue
		}

		for i, inst := range insts {
			p := Placement{
				Label:          fmt.Sprintf("%s.%s.%0*d", s.SceneID, g.Name, width, i),
				Kind:           g.Kind,
				ClassPath:      g.ClassPath,
				StaticMeshPath: g.StaticMeshPath,
				Location:       inst.Location,
				RotationPyr:    inst.RotationPyr,
				Scale:          inst.Scale,
				Properties:     g.Properties,
				Material:       materialOf(g.Properties),
			}
			if g.Kind == "directional_light" {
				diags = append(diags, sunPitchDiags(p.Label, p.RotationPyr)...)
			}
			add(p, g.Tags)
		}
	}

	// Idempotency guard: duplicate labels would collapse under diff.
	seen := make(map[string]bool, len(plan.Placements))
	for _, p := range plan.Placements {
		if seen[p.Label] {
			diags = append(diags, Diagnostic{"warning", "labels",
				"duplicate placement label " + strconv.Quote(p.Label)})
		}
		seen[p.Label] = true
	}

	return plan, diags
}

// sunPitchDiags returns the sun-pitch error diagnostic when pyr's pitch is not
// strictly negative, else nil.
func sunPitchDiags(field string, pyr [3]float64) []Diagnostic {
	if pyr[0] >= 0 {
		return []Diagnostic{{Severity: "error", Field: field, Message: sunPitchMsg}}
	}
	return nil
}

// materialOf pulls a string "material" property, if present, into the dedicated
// Placement.Material field the editor op consumes.
func materialOf(props map[string]any) string {
	if props == nil {
		return ""
	}
	if m, ok := props["material"].(string); ok {
		return m
	}
	return ""
}

// orVec dereferences an optional vector, falling back to def when unset.
func orVec(p *[3]float64, def [3]float64) [3]float64 {
	if p == nil {
		return def
	}
	return *p
}
