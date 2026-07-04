// Package manifest is the Cockpit capability manifest — the fail-closed classification of
// every editor op (EDITOR_PLUGIN_PLAN.md §5.3). It is the security boundary the
// human-in-the-loop gate keys off: an op's Class decides its gate tier, and an op that is
// NOT classified is treated as the most dangerous kind (never permissively defaulted).
// A bijection conformance test (Verify) fails CI if the table and the real _OPS list ever
// drift, so a newly-added op cannot silently bypass the gate.
package manifest

import "sort"

// Class is the gate tier of an op.
type Class string

const (
	Readonly    Class = "readonly"    // observes only; no state change
	Mutating    Class = "mutating"    // reversibly changes editor/actor/asset/world state
	Destructive Class = "destructive" // deletes / irreversibly overwrites
	Exec        Class = "exec"        // runs arbitrary code / compiles
	SCC         Class = "scc"         // source control / writes to disk
)

// OpClass is the capability classification of one op. Class is the gate tier; the flags
// drive concurrency/undo/conflict handling (§4.3/§4.4/§5.2).
type OpClass struct {
	Class          Class `json:"class"`
	Cancellable    bool  `json:"cancellable"`     // long + chunked → mid-flight cancellable
	SelfTransacted bool  `json:"self_transacted"` // wraps its own undo txn (don't double-wrap)
	TouchesAsset   bool  `json:"touches_asset"`   // targets an on-disk asset → asset-open conflict
	Gateable       bool  `json:"gateable"`        // a human may require approval before it runs
}

// FailClosed is applied to an UNKNOWN op: the most restrictive tier, always gateable — a
// new/unclassified op is dangerous until explicitly classified (§5.3).
var FailClosed = OpClass{Class: Exec, TouchesAsset: true, Gateable: true}

// Classify returns the op's classification and whether it was explicitly known. An unknown
// op returns FailClosed, never a permissive default.
func Classify(op string) (OpClass, bool) {
	if c, ok := table[op]; ok {
		return c, true
	}
	return FailClosed, false
}

// Verify checks the bijection between the classification table and the authoritative op
// list (snippets.OpNames()): every real op must be classified, and every classified op
// must be a real op. Returns the sorted missing (unclassified real ops) and extra
// (classified non-ops). A non-empty result fails the conformance test.
func Verify(ops []string) (missing, extra []string) {
	set := make(map[string]bool, len(ops))
	for _, op := range ops {
		set[op] = true
		if _, ok := table[op]; !ok {
			missing = append(missing, op)
		}
	}
	for op := range table {
		if !set[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// Manifest returns a copy of the full classification table (served to the cockpit +
// consumed by the gate).
func Manifest() map[string]OpClass {
	out := make(map[string]OpClass, len(table))
	for k, v := range table {
		out[k] = v
	}
	return out
}
