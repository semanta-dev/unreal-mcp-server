// predicate (merged into package eval) parses and evaluates conditions over a nested
// JSON-decoded state map (the pie_observe schema), shared by pie_wait, actor_call's
// until, playtest wait_until beats and the rubric engine.
//
// A comparison is "<path> <op> <value>", op in >= <= == != > <. The value is parsed
// as a float when possible, else compared as a string (UENUM fields arrive as their
// enumerator NAME). Comparisons combine with and / or / not and parentheses
// ("gamestate.wave >= 2 and not counts.Boss >= 1").
//
// A path is a dotted path into the state, or an OBJECT path: an object reference
// followed by properties and getters, e.g.
// "@subsystem:AesirAgentSubsystem.PeekSnapshotJson().wave_number". Object paths are
// not looked up in the nested state: the caller resolves them in the editor (only
// BlueprintPure/const getters may be called) and puts each value in the state under
// the path string itself (ObjectPaths lists them).
package eval

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Predicate is a parsed condition: one comparison, or and/or/not of conditions.
type Predicate struct {
	kind    string // "cmp" | "and" | "or" | "not"
	sub     []*Predicate
	path    []string
	objPath string // set for an object path (looked up as a flat key)
	op      string
	num     float64
	isNum   bool
	str     string
}

// Ops are the supported comparison operators, longest-first so ">=" is matched
// before ">".
var Ops = []string{">=", "<=", "==", "!=", ">", "<"}

var (
	predPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$`)
	objPath  = regexp.MustCompile(`^@[A-Za-z_]+(:[A-Za-z0-9_]+)?(\.([A-Za-z_][A-Za-z0-9_]*(\(\))?|[0-9]+))+$`)
	predTok  = regexp.MustCompile(`\(\)|\(|\)|>=|<=|==|!=|>|<|"[^"]*"|'[^']*'|[^\s()<>=!"']+(?:\(\)[^\s()<>=!"']*)*|\S`)
)

// ParsePredicate parses a condition (see the package comment).
func ParsePredicate(expr string) (*Predicate, error) {
	ps := &predParser{toks: predTok.FindAllString(strings.TrimSpace(expr), -1), src: expr}
	if len(ps.toks) == 0 {
		return nil, ps.fail()
	}
	p, err := ps.or()
	if err != nil {
		return nil, err
	}
	if ps.i != len(ps.toks) {
		return nil, ps.fail()
	}
	return p, nil
}

type predParser struct {
	toks []string
	i    int
	src  string
}

func (ps *predParser) fail() error {
	return fmt.Errorf("predicate must be '<path> <op> <value>' with op in %v, combined with and / or / not and "+
		"parentheses; got %q", Ops, ps.src)
}

func (ps *predParser) peek() string {
	if ps.i < len(ps.toks) {
		return ps.toks[ps.i]
	}
	return ""
}

func (ps *predParser) or() (*Predicate, error) {
	l, err := ps.and()
	if err != nil {
		return nil, err
	}
	for strings.EqualFold(ps.peek(), "or") {
		ps.i++
		r, err := ps.and()
		if err != nil {
			return nil, err
		}
		l = &Predicate{kind: "or", sub: []*Predicate{l, r}}
	}
	return l, nil
}

func (ps *predParser) and() (*Predicate, error) {
	l, err := ps.factor()
	if err != nil {
		return nil, err
	}
	for strings.EqualFold(ps.peek(), "and") {
		ps.i++
		r, err := ps.factor()
		if err != nil {
			return nil, err
		}
		l = &Predicate{kind: "and", sub: []*Predicate{l, r}}
	}
	return l, nil
}

func (ps *predParser) factor() (*Predicate, error) {
	switch t := ps.peek(); {
	case strings.EqualFold(t, "not"):
		ps.i++
		f, err := ps.factor()
		if err != nil {
			return nil, err
		}
		return &Predicate{kind: "not", sub: []*Predicate{f}}, nil
	case t == "(":
		ps.i++
		e, err := ps.or()
		if err != nil {
			return nil, err
		}
		if ps.peek() != ")" {
			return nil, ps.fail()
		}
		ps.i++
		return e, nil
	}
	return ps.cmp()
}

func (ps *predParser) cmp() (*Predicate, error) {
	if ps.i+3 > len(ps.toks) {
		return nil, ps.fail()
	}
	lhs, op, rhs := ps.toks[ps.i], ps.toks[ps.i+1], ps.toks[ps.i+2]
	isOp := false
	for _, o := range Ops {
		isOp = isOp || o == op
	}
	// Strict: a path, one operator, a value ("a >>> 1" or "a >= " is a typo).
	if !isOp || strings.ContainsAny(rhs[:1], "<>=!()") || strings.EqualFold(rhs, "and") || strings.EqualFold(rhs, "or") {
		return nil, ps.fail()
	}
	p := &Predicate{kind: "cmp", op: op}
	switch {
	case objPath.MatchString(lhs):
		p.objPath = lhs
	case predPath.MatchString(lhs):
		p.path = strings.Split(lhs, ".")
	default:
		return nil, ps.fail()
	}
	ps.i += 3
	rhs = strings.Trim(rhs, `'"`)
	if f, err := strconv.ParseFloat(rhs, 64); err == nil {
		p.num, p.isNum = f, true
	} else {
		p.str = rhs
	}
	return p, nil
}

// ObjectPaths lists the object paths the condition reads (each once, in order).
func (p *Predicate) ObjectPaths() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*Predicate)
	walk = func(q *Predicate) {
		if q.objPath != "" && !seen[q.objPath] {
			seen[q.objPath] = true
			out = append(out, q.objPath)
		}
		for _, s := range q.sub {
			walk(s)
		}
	}
	walk(p)
	return out
}

// HasStatePaths reports whether the condition reads any non-object (state) path.
func (p *Predicate) HasStatePaths() bool {
	if p.kind == "cmp" {
		return p.objPath == ""
	}
	for _, s := range p.sub {
		if s.HasStatePaths() {
			return true
		}
	}
	return false
}

// Eval reports whether the condition holds over state. A nil state or an absent
// path makes a comparison unmet (false, nil) so a wait can poll until present;
// "not" of an absent path is therefore true.
func (p *Predicate) Eval(state map[string]any) (bool, error) {
	switch p.kind {
	case "and", "or":
		for _, s := range p.sub {
			ok, _ := s.Eval(state)
			if p.kind == "and" && !ok {
				return false, nil
			}
			if p.kind == "or" && ok {
				return true, nil
			}
		}
		return p.kind == "and", nil
	case "not":
		ok, _ := p.sub[0].Eval(state)
		return !ok, nil
	}
	if state == nil {
		return false, nil
	}
	var val any
	var ok bool
	if p.objPath != "" {
		val, ok = state[p.objPath]
		ok = ok && val != nil
	} else {
		val, ok = LookupPath(state, p.path)
	}
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
	// A bool compares as a bool: "True" (Python's spelling) and "true" both match.
	if b, isBool := val.(bool); isBool {
		if rb, err := strconv.ParseBool(p.str); err == nil {
			switch p.op {
			case "==":
				return b == rb, nil
			case "!=":
				return b != rb, nil
			}
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
