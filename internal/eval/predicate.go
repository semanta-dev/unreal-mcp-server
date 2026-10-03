// predicate (merged into package eval) parses and evaluates a single comparison over a nested
// JSON-decoded state map (the pie_observe schema). Extracted verbatim from the
// PIE tools so both pie_wait_until and the rubric engine share one evaluator.
//
// A predicate is "<dotted.path> <op> <value>", op in >= <= == != > <. The value
// is parsed as a float when possible, else compared as a string (UENUM fields
// arrive as their enumerator NAME, so string predicates match against that name).
package eval

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Predicate is a parsed single-comparison expression over a state map.
type Predicate struct {
	path  []string
	op    string
	num   float64
	isNum bool
	str   string
}

// Ops are the supported comparison operators, longest-first so ">=" is matched
// before ">".
var Ops = []string{">=", "<=", "==", "!=", ">", "<"}

// ParsePredicate parses "<path> <op> <value>" into a Predicate.
func ParsePredicate(expr string) (*Predicate, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range Ops {
		if i := strings.Index(expr, op); i > 0 {
			lhs := strings.TrimSpace(expr[:i])
			rhs := strings.TrimSpace(expr[i+len(op):])
			p := &Predicate{path: strings.Split(lhs, "."), op: op}
			rhs = strings.Trim(rhs, `'"`)
			if f, err := strconv.ParseFloat(rhs, 64); err == nil {
				p.num = f
				p.isNum = true
			} else {
				p.str = rhs
			}
			return p, nil
		}
	}
	return nil, fmt.Errorf("predicate must be '<path> <op> <value>' with op in %v; got %q", Ops, expr)
}

// Eval reports whether the predicate holds over state. A nil state or an absent
// path is never satisfied (returns false, nil) so a wait can poll until present.
func (p *Predicate) Eval(state map[string]any) (bool, error) {
	if state == nil {
		return false, nil
	}
	val, ok := LookupPath(state, p.path)
	if !ok {
		return false, nil // not present yet
	}
	if p.isNum {
		f, ok := predFloat(val)
		if !ok {
			return false, nil
		}
		switch p.op {
		case ">=":
			return f >= p.num, nil
		case "<=":
			return f <= p.num, nil
		case ">":
			return f > p.num, nil
		case "<":
			return f < p.num, nil
		case "==":
			return f == p.num, nil
		case "!=":
			return f != p.num, nil
		}
	}
	s := fmt.Sprintf("%v", val)
	switch p.op {
	case "==":
		return s == p.str, nil
	case "!=":
		return s != p.str, nil
	}
	return false, nil
}

// LookupPath walks a dotted path through nested map[string]any values.
func LookupPath(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, k := range path {
		switch obj := cur.(type) {
		case map[string]any:
			v, ok := obj[k]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			// numeric segment indexes into a JSON array (e.g. nodes.0.SupplyRatio)
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(obj) {
				return nil, false
			}
			cur = obj[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// predFloat coerces the common JSON-decoded numeric kinds to float64.
func predFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
