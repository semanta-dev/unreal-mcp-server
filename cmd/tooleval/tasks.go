package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
)

// task is one eval item. FirstV1/FirstV2 list the acceptable first tools on each
// surface (any of them counts as a correct first choice); Success is judged on the v2
// run's final world state, the calls it made and its final answer.
type task struct {
	ID      string   `json:"id"`
	Source  string   `json:"source"` // derived (from observed sessions/workflows) | heldout
	Tags    []string `json:"tags,omitempty"`
	Prompt  string   `json:"prompt"`
	Setup   setup    `json:"setup"`
	FirstV1 []string `json:"first_v1"`
	FirstV2 []string `json:"first_v2"`
	Success []check  `json:"success"`
	// Reference is a known-good v2 solution: -mode dry replays it and the success
	// checks must hold (every task is proven solvable before any API spend).
	Reference       []refCall `json:"reference"`
	ReferenceAnswer string    `json:"reference_answer,omitempty"`
}

type refCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

type setup struct {
	Actors []fixtureActor `json:"actors,omitempty"`
	Assets []string       `json:"assets,omitempty"`
	PIE    bool           `json:"pie,omitempty"`
}

type fixtureActor struct {
	Label      string         `json:"label"`
	Class      string         `json:"class"`
	Location   [3]float64     `json:"location"`
	Properties map[string]any `json:"properties,omitempty"`
}

// check is one success condition; all of a task's checks must hold.
//   - actor_exists / actor_absent: an editor actor with Label (and Class substring when set)
//   - actor_near: Label's location within Tol (default 1) of At
//   - prop: Label's property Key equals Value
//   - pie: PIE running == Want
//   - asset_exists: an asset at Path
//   - called: a call to Tool (with Op when set, and Args a subset of its arguments when set)
//   - answer: the final answer contains Text (case-insensitive)
type check struct {
	Kind  string         `json:"kind"`
	Label string         `json:"label,omitempty"`
	Class string         `json:"class,omitempty"`
	At    [3]float64     `json:"at,omitempty"`
	Tol   float64        `json:"tol,omitempty"`
	Key   string         `json:"key,omitempty"`
	Value any            `json:"value,omitempty"`
	Want  bool           `json:"want,omitempty"`
	Path  string         `json:"path,omitempty"`
	Tool  string         `json:"tool,omitempty"`
	Op    string         `json:"op,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
	Text  string         `json:"text,omitempty"`
	Any   []check        `json:"any,omitempty"`
}

func loadTasks(path string) ([]*task, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ts []*task
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, t := range ts {
		if seen[t.ID] {
			return nil, fmt.Errorf("duplicate task id %s", t.ID)
		}
		seen[t.ID] = true
		if t.Prompt == "" || len(t.FirstV2) == 0 || len(t.FirstV1) == 0 || len(t.Success) == 0 {
			return nil, fmt.Errorf("task %s: prompt, first_v1, first_v2 and success are required", t.ID)
		}
	}
	return ts, nil
}

// toolCall is one call an agent made (v2 runs).
type toolCall struct {
	Tool  string         `json:"tool"`
	Args  map[string]any `json:"args"`
	Error string         `json:"error,omitempty"` // envelope code when the call failed
}

// evaluate returns the failed checks (empty = success).
func evaluate(t *task, st bridgetest.State, calls []toolCall, answer string) []string {
	var failed []string
	for _, c := range t.Success {
		if !holds(c, st, calls, answer) {
			b, _ := json.Marshal(c)
			failed = append(failed, string(b))
		}
	}
	return failed
}

// holds reports whether one check is met.
//   - attempted: like called, but a call that failed (e.g. no engine in the emulator) counts
//   - any_of: at least one of Any holds
func holds(c check, st bridgetest.State, calls []toolCall, answer string) bool {
	find := func(label string) *bridgetest.Actor {
		for i := range st.Editor {
			if st.Editor[i].Label == label {
				return &st.Editor[i]
			}
		}
		return nil
	}
	switch c.Kind {
	case "actor_exists":
		a := find(c.Label)
		return a != nil && (c.Class == "" || strings.Contains(strings.ToLower(a.Class), strings.ToLower(c.Class)))
	case "actor_absent":
		return find(c.Label) == nil
	case "actor_near":
		a := find(c.Label)
		if a == nil {
			return false
		}
		tol := c.Tol
		if tol == 0 {
			tol = 1
		}
		for i := range 3 {
			if math.Abs(a.Location[i]-c.At[i]) > tol {
				return false
			}
		}
		return true
	case "prop":
		a := find(c.Label)
		return a != nil && fmt.Sprint(a.Properties[c.Key]) == fmt.Sprint(c.Value)
	case "pie":
		return st.PIERunning == c.Want
	case "asset_exists":
		_, ok := st.Assets[c.Path]
		return ok
	case "called", "attempted":
		for _, call := range calls {
			if call.Tool != c.Tool || (c.Kind == "called" && call.Error != "") {
				continue
			}
			if c.Op != "" && call.Args["op"] != c.Op {
				continue
			}
			if subset(c.Args, call.Args) {
				return true
			}
		}
		return false
	case "answer":
		return strings.Contains(strings.ToLower(answer), strings.ToLower(c.Text))
	case "any_of":
		for _, sub := range c.Any {
			if holds(sub, st, calls, answer) {
				return true
			}
		}
		return false
	}
	return false
}

func subset(want, got map[string]any) bool {
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}
