package scenespec

import "sort"

// SnapActor is one actor read from a level snapshot, keyed by its label in the
// before-map passed to Diff.
type SnapActor struct {
	Location [3]float64 `json:"location"`
	Class    string     `json:"class"`
}

// DiffResult classifies a plan against a level snapshot for idempotent apply /
// dry-run. Add and Update follow deterministic plan order; Prune is sorted.
type DiffResult struct {
	// Add are plan labels not present in the snapshot (to be created).
	Add []string `json:"add"`
	// Update are plan labels already present in the snapshot (to be reconciled).
	Update []string `json:"update"`
	// Prune are snapshot labels under this scene's namespace that the plan no
	// longer contains. Advisory for dry-run; the editor op performs the
	// authoritative, tag-scoped prune.
	Prune []string `json:"prune"`
	// MissingAssetRefs are the distinct static-mesh / class asset paths the plan
	// references, for a later existence check.
	MissingAssetRefs []string `json:"missing_asset_refs"`
}

// Diff compares a compiled Plan against a level snapshot (before), keyed by
// label. A label is an Add when the plan introduces it, an Update when it
// already exists, and a Prune candidate when the snapshot holds a label under
// "<sceneID>." that the plan omits. MissingAssetRefs collects every distinct
// asset path the plan references, in first-seen order.
func Diff(sceneID string, plan Plan, before map[string]SnapActor) DiffResult {
	var res DiffResult

	planned := make(map[string]bool, len(plan.Placements))
	seenAsset := make(map[string]bool)
	for _, p := range plan.Placements {
		planned[p.Label] = true
		if _, ok := before[p.Label]; ok {
			res.Update = append(res.Update, p.Label)
		} else {
			res.Add = append(res.Add, p.Label)
		}
		for _, a := range []string{p.StaticMeshPath, p.ClassPath} {
			if a != "" && !seenAsset[a] {
				seenAsset[a] = true
				res.MissingAssetRefs = append(res.MissingAssetRefs, a)
			}
		}
	}

	prefix := sceneID + "."
	for label := range before {
		if len(label) >= len(prefix) && label[:len(prefix)] == prefix && !planned[label] {
			res.Prune = append(res.Prune, label)
		}
	}
	sort.Strings(res.Prune)

	return res
}
