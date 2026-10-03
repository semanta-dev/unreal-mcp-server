package audit

import (
	"math"
	"strconv"
	"testing"

)

func TestUtilizationPolyWorldScaleDiagnostic(t *testing.T) {
	assets := make([]string, 1000)
	for i := range assets {
		assets[i] = "/Game/Pack/Asset_" + strconv.Itoa(i)
	}

	inv := PackInventory{
		Pack:      "PolyWorldScale",
		AllAssets: assets,
		Referenced: []string{
			"/Game/Pack/Asset_0",
			"/Game/Pack/Asset_1",
			"/Game/Pack/Asset_2",
			"/Game/Pack/Asset_3",
			"/Game/Pack/Asset_4",
			"/Game/Pack/Missing",
		},
	}

	got := Utilization(inv)
	if got.Total != 1000 {
		t.Fatalf("Total = %d, want 1000", got.Total)
	}
	if got.Referenced != 5 {
		t.Fatalf("Referenced = %d, want 5", got.Referenced)
	}
	if math.Abs(got.Fraction-0.005) > 1e-12 {
		t.Fatalf("Fraction = %g, want 0.005", got.Fraction)
	}
}

func TestUtilizationEmptyInventory(t *testing.T) {
	got := Utilization(PackInventory{Pack: "Empty"})
	if got.Total != 0 {
		t.Fatalf("Total = %d, want 0", got.Total)
	}
	if got.Referenced != 0 {
		t.Fatalf("Referenced = %d, want 0", got.Referenced)
	}
	if got.Fraction != 0 {
		t.Fatalf("Fraction = %g, want 0", got.Fraction)
	}
}
