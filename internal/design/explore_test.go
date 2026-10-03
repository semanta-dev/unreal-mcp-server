package design

import (
	"reflect"
	"testing"
)

func TestSearchRediscoversPlantedRegime(t *testing.T) {
	seed := Genotype{
		Policy:   Policy{S: 0.15, Splash: 0.7, E: 0.9},
		Scaffold: TwoAxisScaffold(),
	}
	planted := Genotype{
		Policy: Policy{S: 0.85, Splash: 0.95, E: 0.9},
		Scaffold: Scaffold{
			Waves: []Wave{
				{Count: 6, HP: 12},
				{Count: 24, HP: 4, Spike: true},
				{Count: 22, HP: 4},
				{Count: 3, HP: 35},
				{Count: 2, HP: 45, Spike: true},
			},
			BaseDamage:   80,
			Focus:        5,
			Spread:       4,
			SplashMin:    0.15,
			ExpandGrowth: 0.25,
		},
	}

	seed = project(seed)
	planted = project(planted)
	if _, ok := evaluate(seed); !ok {
		t.Fatalf("seed must be feasible: out=%+v gate=%+v", Simulate(seed.Scaffold, seed.Policy), Gate(seed.Scaffold, gateGrid))
	}
	if _, ok := evaluate(planted); !ok {
		t.Fatalf("planted regime must be feasible: out=%+v gate=%+v", Simulate(planted.Scaffold, planted.Policy), Gate(planted.Scaffold, gateGrid))
	}

	seedCell := descriptor(seed)
	plantedCell := descriptor(planted)
	if !farEnough(seedCell, plantedCell, 2) {
		t.Fatalf("planted cell must be at least 2 bins away from seed: seed=%+v planted=%+v", seedCell, plantedCell)
	}

	cfg := Config{
		Evaluations: 2000,
		Seed:        42,
		TopK:        8,
		Donors: []Scaffold{
			planted.Scaffold,
			TwoAxisScaffold(),
		},
	}
	r1 := Search(seed, cfg)
	r2 := Search(seed, cfg)

	if r1.Filled != r2.Filled {
		t.Fatalf("determinism filled mismatch: first=%d second=%d", r1.Filled, r2.Filled)
	}
	if !reflect.DeepEqual(occupiedCells(r1.Archive), occupiedCells(r2.Archive)) {
		t.Fatalf("determinism occupied cell mismatch: first=%v second=%v", occupiedCells(r1.Archive), occupiedCells(r2.Archive))
	}

	if !occupiedNearPlanted(r1.Archive, seedCell, plantedCell) {
		t.Fatalf("search did not rediscover planted regime near cell %+v; seed=%+v occupied=%v", plantedCell, seedCell, occupiedCells(r1.Archive))
	}
	for cell, genotype := range r1.Archive {
		if genotype.Policy.Splash < genotype.Scaffold.SplashMin {
			t.Fatalf("archived genotype violates splash fence at %+v: splash=%f min=%f", cell, genotype.Policy.Splash, genotype.Scaffold.SplashMin)
		}
		if _, ok := evaluate(genotype); !ok {
			t.Fatalf("archive contains infeasible genotype at %+v: %+v", cell, genotype)
		}
	}
}

func occupiedNearPlanted(archive map[Cell]Genotype, seedCell, plantedCell Cell) bool {
	for cell := range archive {
		if within(cell, plantedCell, 1) && farEnough(seedCell, cell, 2) {
			return true
		}
	}
	return false
}

func occupiedCells(archive map[Cell]Genotype) []Cell {
	cells := make([]Cell, 0, len(archive))
	for cell := range archive {
		cells = append(cells, cell)
	}
	for i := 0; i < len(cells); i++ {
		for j := i + 1; j < len(cells); j++ {
			if lessCell(cells[j], cells[i]) {
				cells[i], cells[j] = cells[j], cells[i]
			}
		}
	}
	return cells
}

func lessCell(a, b Cell) bool {
	if a.D1 != b.D1 {
		return a.D1 < b.D1
	}
	if a.D2 != b.D2 {
		return a.D2 < b.D2
	}
	return a.D3 < b.D3
}

func within(a, b Cell, maxDelta int) bool {
	return abs(a.D1-b.D1) <= maxDelta && abs(a.D2-b.D2) <= maxDelta && abs(a.D3-b.D3) <= maxDelta
}

func farEnough(a, b Cell, minDelta int) bool {
	return abs(a.D1-b.D1) >= minDelta || abs(a.D2-b.D2) >= minDelta || abs(a.D3-b.D3) >= minDelta
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
