// scenario (merged into package eval) is the pure spec + validator for scenario/v1 — a saved,
// named, replayable functional test: arrange a world (set-props on live actors),
// act (ordered beats: UFUNCTION calls incl. Server RPCs, console commands, waits),
// and assert (a rubric over the recorded timeline). The saved suite is the
// regression gate "iterate at scale" needs. The orchestrator that RUNS a scenario
// (driving the editor + capture) is a gated Go tool; this package is the offline,
// unit-tested spec/parse/validate spine.
package eval

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

// Beat is one timed step over the capture window. It is scheduled on the wall clock
// (at_s, seconds after play starts) or on the game's own clock (at_world_s, game
// seconds after the window opens: it stops while the game is paused and follows time
// dilation) — one clock per scenario.
type Beat struct {
	AtS         float64          `json:"at_s,omitempty"`
	AtWorldS    float64          `json:"at_world_s,omitempty"`
	Exec        *ExecStep        `json:"exec,omitempty"`
	WaitUntil   string           `json:"wait_until,omitempty"`
	TimeoutS    float64          `json:"timeout_s,omitempty"`
	Console     string           `json:"console,omitempty"`
	Input       *InputStep       `json:"input,omitempty"`
	GameCommand *GameCommandStep `json:"game_command,omitempty"`
}

// InputStep plays input like a player (pie op=input / cursor / ui_click): a key or
// axis (key), a cursor action at a viewport position (position), or a click on a
// visible widget (widget) — exactly one of the three.
type InputStep struct {
	Key       string    `json:"key,omitempty"`
	Action    string    `json:"action,omitempty"`
	Value     *float64  `json:"value,omitempty"`
	DurationS float64   `json:"duration_s,omitempty"`
	Position  []float64 `json:"position,omitempty"`
	To        []float64 `json:"to,omitempty"`
	Button    string    `json:"button,omitempty"`
	Widget    string    `json:"widget,omitempty"`
}

// Kind is the pie op the step maps to: input, cursor or ui_click ("" when malformed).
func (s *InputStep) Kind() string {
	n, kind := 0, ""
	if s.Key != "" {
		n, kind = n+1, "input"
	}
	if s.Position != nil {
		n, kind = n+1, "cursor"
	}
	if s.Widget != "" {
		n, kind = n+1, "ui_click"
	}
	if n != 1 {
		return ""
	}
	return kind
}

// inputActions are the actions each kind of input step takes.
var inputActions = map[string]map[string]bool{
	"input":  {"": true, "tap": true, "press": true, "release": true, "hold": true, "axis": true},
	"cursor": {"": true, "move": true, "click": true, "drag": true},
}

// check says what is wrong with the step ("" when nothing), the same rules as pie
// op=input / cursor / ui_click — caught when the scenario is parsed, not mid-run.
func (s *InputStep) check() string {
	kind := s.Kind()
	switch {
	case kind == "":
		return "input needs exactly one of key, position, widget"
	case kind != "ui_click" && !inputActions[kind][s.Action]:
		return fmt.Sprintf("a %s step takes no action %q", kind, s.Action)
	case kind == "ui_click" && s.Action != "":
		return "a widget click takes no action"
	case (s.Action == "axis") != (s.Value != nil):
		return "value goes with action=axis, and action=axis needs value"
	case kind == "cursor" && len(s.Position) != 2:
		return "position is [x, y] in viewport pixels"
	case (s.Action == "drag") != (s.To != nil):
		return "to goes with action=drag, and action=drag needs to"
	case s.To != nil && len(s.To) != 2:
		return "to is [x, y] in viewport pixels"
	}
	return ""
}

// GameCommandStep runs one of the game's commands (game_command; the project's
// game_api). RequestID defaults to one unique per run and beat.
type GameCommandStep struct {
	Name      string         `json:"name"`
	Args      map[string]any `json:"args,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// WorldClock reports whether the beats are scheduled on the game's clock (at_world_s).
func (s *Scenario) WorldClock() bool {
	for _, b := range s.Beats {
		if b.AtWorldS > 0 {
			return true
		}
	}
	return false
}

type ExecStep struct {
	Target    string         `json:"target"`
	UFunction string         `json:"ufunction"`
	Args      map[string]any `json:"args,omitempty"`
}

// RubricCheck mirrors an internal/Check (kept here so scenario stays
// dependency-free; the runner maps it to Check).
type RubricCheck struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	Path     string         `json:"path"`
	Params   map[string]any `json:"params,omitempty"`
	Severity string         `json:"severity,omitempty"`
	// AllowPerturbed lets a check read recorder.* — the recorder's own tick timing,
	// which capture perturbs — knowing it is not the game's frame rate.
	AllowPerturbed bool `json:"allow_perturbed,omitempty"`
}

// Diagnostic is a validation problem (severity error|warning).
type Diagnostic struct {
	Severity string `json:"severity"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

var validModes = map[string]bool{"pie": true, "simulate": true, "editor": true, "": true}

// ParseScenario decodes and validates a scenario/v1 document. JSON errors are returned as
// the error; semantic problems (bad schema/mode, missing name, malformed beats or
// checks) are collected as Diagnostics.
func ParseScenario(data []byte) (*Scenario, []Diagnostic, error) {
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
	wall, world := false, false
	for i, b := range s.Beats {
		wall, world = wall || b.AtS > 0, world || b.AtWorldS > 0
		if b.AtS < 0 || b.AtWorldS < 0 {
			add("error", fmt.Sprintf("beats[%d]", i), "at_s / at_world_s must be >= 0")
		}
		n := 0
		if b.Input != nil {
			n++
			if msg := b.Input.check(); msg != "" {
				add("error", fmt.Sprintf("beats[%d].input", i), msg)
			}
		}
		if b.GameCommand != nil {
			n++
			if strings.TrimSpace(b.GameCommand.Name) == "" {
				add("error", fmt.Sprintf("beats[%d].game_command", i), "game_command needs name")
			}
		}
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
			add("warning", fmt.Sprintf("beats[%d]", i), "beat has no exec/wait_until/console/input/game_command (no-op)")
		}
	}
	if wall && world {
		add("error", "beats", "beats are scheduled on one clock: at_s (wall) or at_world_s (game time), not both")
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
	diags = append(diags, LintRubric(s.Rubric)...)
	return &s, diags, nil
}

// LintRubric rejects checks that would score the recorder instead of the game:
// perf.* (removed in v2.1 — it was the recorder's tick, slowed by its own captures,
// reported as fps) and recorder.* unless the check sets allow_perturbed.
func LintRubric(checks []RubricCheck) []Diagnostic {
	var diags []Diagnostic
	for i, c := range checks {
		field := fmt.Sprintf("rubric[%d].path", i)
		root, _, _ := strings.Cut(c.Path, ".")
		switch {
		case root == "perf":
			diags = append(diags, Diagnostic{"error", field, "perf.* was removed: it timed the recorder (which its own captures " +
				"slow down), not the game. Measure the game with a CsvProfiler capture (analyze op=perf); the recorder's tick " +
				"is recorder.tick_ms (needs allow_perturbed: true)"})
		case root == "recorder" && !c.AllowPerturbed:
			diags = append(diags, Diagnostic{"error", field, "recorder.* is the recorder's own tick timing, perturbed by " +
				"capture — not the game's frame rate; set allow_perturbed: true to score it anyway"})
		}
	}
	return diags
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
