package tools

import (
	"path/filepath"

	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

func scenarioRubric(checks []eval.RubricCheck) eval.RubricSpec {
	spec := eval.RubricSpec{Checks: make([]eval.Check, len(checks))}
	for i, c := range checks {
		spec.Checks[i] = eval.Check{ID: c.ID, Kind: c.Kind, Path: c.Path, Params: c.Params, Severity: c.Severity}
	}
	return spec
}

// enrichVisual decodes each captured frame and stamps its mean luma into the
// frame's observed state as visual.luma, so a rubric "min visual.luma 0.02"
// catches a black/unrendered frame.
func enrichVisual(dir string, frames []captureFrame) {
	for i := range frames {
		img, err := visual.Load(filepath.Join(dir, frames[i].File))
		if err != nil {
			continue
		}
		if frames[i].State == nil {
			frames[i].State = map[string]any{}
		}
		frames[i].State["visual"] = map[string]any{"luma": visual.MeanLuma(img)}
	}
}
