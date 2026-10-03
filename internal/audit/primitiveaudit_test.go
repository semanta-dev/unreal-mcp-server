package audit

import (
	"testing"

)

func TestAuditFixtures(t *testing.T) {
	t.Run("fails cube scene defect fixture", func(t *testing.T) {
		report := Audit(CubeScene())
		if report.Pass {
			t.Fatalf("Audit(CubeScene()).Pass = true, want false")
		}

		assertViolation(t, report, "Turret", kindPrimitiveMesh, true)
		assertViolation(t, report, "Turret", kindPrimitiveMat, true)
		assertViolation(t, report, "Turret", kindDebugDraw, true)
	})

	t.Run("passes dressed scene fixed fixture", func(t *testing.T) {
		report := Audit(DressedScene())
		if !report.Pass {
			t.Fatalf("Audit(DressedScene()).Pass = false, want true; violations: %#v", report.Violations)
		}
		if heroViolations := countHeroViolations(report); heroViolations != 0 {
			t.Fatalf("Audit(DressedScene()) hero violations = %d, want 0; violations: %#v", heroViolations, report.Violations)
		}
	})
}

func TestAuditRecordsNonHeroViolationsWithoutFailing(t *testing.T) {
	report := Audit(Scene{Actors: []Actor{
		{
			Label:        "GreyboxMarker",
			Class:        "AStaticMeshActor",
			MeshPath:     "/engine/basicshapes/cube",
			MaterialPath: "/engine/basicshapes/basicshapematerial",
			DebugDraw:    true,
		},
	}})

	if !report.Pass {
		t.Fatalf("non-hero primitive violations failed gate")
	}
	if len(report.Violations) != 3 {
		t.Fatalf("violations = %d, want 3: %#v", len(report.Violations), report.Violations)
	}
	for _, violation := range report.Violations {
		if violation.Hero {
			t.Fatalf("non-hero violation recorded as hero: %#v", violation)
		}
	}
}

func assertViolation(t *testing.T, report PrimitiveReport, actor, kind string, hero bool) {
	t.Helper()
	for _, violation := range report.Violations {
		if violation.Actor == actor && violation.Kind == kind && violation.Hero == hero {
			return
		}
	}
	t.Fatalf("missing violation actor=%q kind=%q hero=%v in %#v", actor, kind, hero, report.Violations)
}

func countHeroViolations(report PrimitiveReport) int {
	count := 0
	for _, violation := range report.Violations {
		if violation.Hero {
			count++
		}
	}
	return count
}
