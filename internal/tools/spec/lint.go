package spec

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// MaxSync is the longest a synchronous op may run (§2.2 R6).
const MaxSync = 30 * time.Second

var propName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Lint checks the v2 design rules that can be verified statically over a set of
// specs: names/vocabulary (R3), per-op timing (R6), the op-enum cap (R2), tier
// rules (§2.1 — ReadOnly tools hold only ReadOnly ops; a Destructive/Exec op only
// sits in a tool whose other ops are all ≥ Mutating; declared tier ≥ the worst tier
// of every Python op it Reaches), and unique tool names. It returns every violation.
func Lint(specs []*Spec) []string {
	var v []string
	add := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	seen := map[string]bool{}
	for _, s := range specs {
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
			}
		}
		if hasDanger && hasLow {
			add("%s: destructive/exec op mixed with read-only/ephemeral ops — split the tool", s.Name)
		}
	}
	sort.Strings(v)
	return v
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
