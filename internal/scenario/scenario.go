// Package scenario is the pure spec + validator for scenario/v1 — a saved,
// named, replayable functional test: arrange a world (set-props on live actors),
// act (ordered beats: UFUNCTION calls incl. Server RPCs, console commands, waits),
// and assert (a rubric over the recorded timeline). The saved suite is the
// regression gate "iterate at scale" needs. The orchestrator that RUNS a scenario
// (driving the editor + capture) is a gated Go tool; this package is the offline,
// unit-tested spec/parse/validate spine.
package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Scenario is a scenario/v1 document.
type Scenario struct {
	Schema       string        `json:"schema"`
	Name         string        `json:"name"`
	Mode         string        `json:"mode,omitempty"` // pie|simulate|editor; default pie
	Level        string        `json:"level,omitempty"`
	DurationS    float64       `json:"duration_s,omitempty"`
	IntervalS    float64       `json:"interval_s,omitempty"`
	TimeDilation float64       `json:"time_dilation,omitempty"`
	TrackActors  []string      `json:"track_actors,omitempty"`
	Setup        Setup         `json:"setup,omitempty"`
	Beats        []Beat        `json:"beats,omitempty"`
	Rubric       []RubricCheck `json:"rubric,omitempty"`
}

// Setup arranges preconditions before play. Spawning into the game world is
// plugin-gated (P7); scenarios set properties on actors already present.
type Setup struct {
	SetProps []SetProp `json:"set_props,omitempty"`
}

type SetProp struct {
	Target     string         `json:"target"`
	Properties map[string]any `json:"properties"`
}

// Beat is one timed step over the capture window.
type Beat struct {
	AtS       float64   `json:"at_s,omitempty"`
	Exec      *ExecStep `json:"exec,omitempty"`
	WaitUntil string    `json:"wait_until,omitempty"`
	TimeoutS  float64   `json:"timeout_s,omitempty"`
	Console   string    `json:"console,omitempty"`
}

type ExecStep struct {
	Target    string         `json:"target"`
	UFunction string         `json:"ufunction"`
	Args      map[string]any `json:"args,omitempty"`
}

// RubricCheck mirrors an internal/rubric.Check (kept here so scenario stays
// dependency-free; the runner maps it to rubric.Check).
type RubricCheck struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	Path     string         `json:"path"`
	Params   map[string]any `json:"params,omitempty"`
	Severity string         `json:"severity,omitempty"`
}

// Diagnostic is a validation problem (severity error|warning).
type Diagnostic struct {
	Severity string `json:"severity"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

var validModes = map[string]bool{"pie": true, "simulate": true, "editor": true, "": true}

// Parse decodes and validates a scenario/v1 document. JSON errors are returned as
// the error; semantic problems (bad schema/mode, missing name, malformed beats or
// checks) are collected as Diagnostics.
func Parse(data []byte) (*Scenario, []Diagnostic, error) {
	var s Scenario
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&s); err != nil {
		return nil, nil, err
	}
	var diags []Diagnostic
	add := func(sev, field, msg string) { diags = append(diags, Diagnostic{sev, field, msg}) }

	if s.Schema != "scenario/v1" {
		add("error", "schema", fmt.Sprintf("schema must be \"scenario/v1\", got %q", s.Schema))
	}
	if strings.TrimSpace(s.Name) == "" {
		add("error", "name", "name is required")
	}
	if !validModes[s.Mode] {
		add("error", "mode", fmt.Sprintf("mode must be pie|simulate|editor, got %q", s.Mode))
	}
	for i, sp := range s.Setup.SetProps {
		if sp.Target == "" {
			add("error", fmt.Sprintf("setup.set_props[%d].target", i), "target is required")
		}
	}
	for i, b := range s.Beats {
		n := 0
		if b.Exec != nil {
			n++
			if b.Exec.Target == "" || b.Exec.UFunction == "" {
				add("error", fmt.Sprintf("beats[%d].exec", i), "exec needs target and ufunction")
			}
		}
		if b.WaitUntil != "" {
			n++
		}
		if b.Console != "" {
			n++
		}
		if n == 0 {
			add("warning", fmt.Sprintf("beats[%d]", i), "beat has no exec/wait_until/console (no-op)")
		}
	}
	seen := map[string]bool{}
	for i, c := range s.Rubric {
		if c.ID == "" {
			add("error", fmt.Sprintf("rubric[%d].id", i), "check id is required")
		} else if seen[c.ID] {
			add("error", fmt.Sprintf("rubric[%d].id", i), "duplicate check id "+c.ID)
		}
		seen[c.ID] = true
		if c.Kind == "" {
			add("error", fmt.Sprintf("rubric[%d].kind", i), "check kind is required")
		}
	}
	return &s, diags, nil
}

// HasErrors reports whether any diagnostic is an error (vs a warning).
func HasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}
