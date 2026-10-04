package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxSync is the longest a synchronous op may run (§2.2 R6).
const MaxSync = 30 * time.Second

var propName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Lint checks the v2 design rules that can be verified statically over a set of
// specs: names/vocabulary (R3), per-op timing (R6), the op-enum cap (R2), tier
// rules (§2.1 — ReadOnly tools hold only ReadOnly ops; a Destructive/Exec op only
// sits in a tool whose other ops are all ≥ Mutating; declared tier ≥ the worst tier
// of every Python op it Reaches), world=auto only on ReadOnly tools (R3), and unique
// tool names. It returns every violation.
func Lint(specs []*Spec) []string {
	var v []string
	add := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	seen := map[string]bool{}
	descOf := map[string]string{}
	for _, s := range specs {
		if strings.TrimSpace(s.Description) == "" {
			add("%s: empty description", s.Name)
		} else if other, dup := descOf[s.Description]; dup {
			add("%s: same description as %s", s.Name, other)
		} else {
			descOf[s.Description] = s.Name
		}
		if seen[s.Name] {
			add("%s: duplicate tool name", s.Name)
		}
		seen[s.Name] = true
		if !propName.MatchString(s.Name) {
			add("%s: tool name must match %s", s.Name, propName)
		}
		if len(s.Ops) == 0 {
			add("%s: no ops", s.Name)
		}
		if len(s.Ops) > 8 {
			add("%s: %d ops (max 8)", s.Name, len(s.Ops))
		}
		if s.Schema != nil {
			for k := range s.Schema.Properties {
				if !propName.MatchString(k) {
					add("%s: property %q must match %s", s.Name, k, propName)
				}
			}
			// R3: world "auto" (PIE if running, else the editor) is allowed only on
			// ReadOnly tools — a write must name the world it changes.
			if w, ok := s.Schema.Properties["world"]; ok && s.Tier() > ReadOnly {
				for _, e := range w.Enum {
					if e == "auto" {
						add("%s: world=auto on a %s tool (allowed only on readonly tools)", s.Name, s.Tier())
					}
				}
			}
		}
		hasDanger, hasLow := false, false
		for i := range s.Ops {
			op := &s.Ops[i]
			async, _, max := s.effective(op)
			if !async && (max == 0 || max > MaxSync) {
				add("%s op=%q: sync op needs 0 < Max ≤ %s (got %s)", s.Name, op.Name, MaxSync, max)
			}
			if op.Tier >= Destructive {
				hasDanger = true
			} else if op.Tier < Mutating {
				hasLow = true
			}
			for _, py := range op.Reaches {
				p, ok := PyOps[py]
				if !ok {
					add("%s op=%q: reaches unclassified python op %q", s.Name, op.Name, py)
					continue
				}
				if w := p.WorstTier(); w > op.Tier && !rejectsEscalations(op, p) {
					add("%s op=%q: declared %s but reaches %s (worst %s); declare ≥ %s or Reject its escalating args",
						s.Name, op.Name, op.Tier, py, w, w)
				}
				if p.Plugin > 0 && neededPlugin(op.Needs) < p.Plugin {
					add("%s op=%q: reaches %s, which needs plugin API %d: declare Needs \"plugin>=%d\"", s.Name, op.Name, py, p.Plugin, p.Plugin)
				}
			}
		}
		for i := range s.Ops {
			for _, n := range s.Ops[i].Needs {
				if !needsVocab[n] && !pluginNeed.MatchString(n) {
					add("%s op=%q: unknown Needs %q (want pie, plugin, plugin>=N, navmesh, project or engine)", s.Name, s.Ops[i].Name, n)
				}
			}
		}
		if hasDanger && hasLow {
			add("%s: destructive/exec op mixed with read-only/ephemeral ops — split the tool", s.Name)
		}
	}
	sort.Strings(v)
	return v
}

var (
	needsVocab = map[string]bool{"pie": true, "plugin": true, "navmesh": true, "project": true, "engine": true}
	pluginNeed = regexp.MustCompile(`^plugin>=([1-9][0-9]*)$`)
)

// neededPlugin is the plugin API an op's Needs declares (0 when none).
func neededPlugin(needs []string) int {
	best := 0
	for _, n := range needs {
		if m := pluginNeed.FindStringSubmatch(n); m != nil {
			if v, _ := strconv.Atoi(m[1]); v > best {
				best = v
			}
		}
	}
	return best
}

// rejectsEscalations reports whether op's base tier covers p's base tier and op
// rejects every argument that would escalate p beyond op's tier.
func rejectsEscalations(op *OpSpec, p PyOp) bool {
	if p.Tier > op.Tier {
		return false
	}
	rejected := map[string]bool{}
	for _, r := range op.Rejects {
		rejected[r] = true
	}
	for _, e := range p.Escalations {
		if e.Tier > op.Tier && !rejected[e.Arg] {
			return false
		}
	}
	return true
}
