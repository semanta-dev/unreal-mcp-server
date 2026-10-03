// primitiveaudit (merged into package audit) implements the primitive_audit Layer-A audit from
// AGENTIC_GAMEDEV_PLAN.md §7.1 / RC3, detecting primitive-fallback programmer art
// in captured scene traces.
package audit

import (
	"strings"

)

const (
	basicShapesPrefix   = "/engine/basicshapes/"
	basicShapeMaterial  = "/engine/basicshapes/basicshapematerial"
	kindPrimitiveMesh   = "primitive_mesh"
	kindPrimitiveMat    = "primitive_material"
	kindDebugDraw       = "debug_draw"
	reasonPrimitiveMesh = "mesh path uses /Engine/BasicShapes/"
	reasonPrimitiveMat  = "material path uses /Engine/BasicShapes/BasicShapeMaterial"
	reasonDebugDraw     = "actor uses debug draw"
)

// PrimitiveReport is the primitive audit result.
type PrimitiveReport struct {
	Pass       bool
	Violations []Violation
}

// Violation is one programmer-art tell found on an actor.
type Violation struct {
	Actor  string
	Kind   string
	Reason string
	Hero   bool
}

// Audit scans a scene for engine primitives, basic shape materials, and debug
// draw visuals. Non-hero primitive use is recorded but only hero/structural slot
// violations fail the gate.
func Audit(scene Scene) PrimitiveReport {
	var violations []Violation
	pass := true

	for _, actor := range scene.Actors {
		if strings.HasPrefix(strings.ToLower(actor.MeshPath), basicShapesPrefix) {
			violations = appendViolation(violations, actor, kindPrimitiveMesh, reasonPrimitiveMesh)
			if actor.Hero {
				pass = false
			}
		}
		if strings.EqualFold(actor.MaterialPath, basicShapeMaterial) {
			violations = appendViolation(violations, actor, kindPrimitiveMat, reasonPrimitiveMat)
			if actor.Hero {
				pass = false
			}
		}
		if actor.DebugDraw {
			violations = appendViolation(violations, actor, kindDebugDraw, reasonDebugDraw)
			if actor.Hero {
				pass = false
			}
		}
	}

	return PrimitiveReport{Pass: pass, Violations: violations}
}

func appendViolation(violations []Violation, actor Actor, kind, reason string) []Violation {
	return append(violations, Violation{
		Actor:  actor.Label,
		Kind:   kind,
		Reason: reason,
		Hero:   actor.Hero,
	})
}
