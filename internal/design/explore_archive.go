package design

type archiveEntry struct {
	genotype Genotype
	fitness  float64
}

type archive struct {
	cells map[Cell]archiveEntry
}

func newArchive() archive {
	return archive{cells: make(map[Cell]archiveEntry)}
}

func (a archive) insert(cell Cell, genotype Genotype, fitness float64) bool {
	incumbent, ok := a.cells[cell]
	if ok && incumbent.fitness >= fitness {
		return false
	}
	a.cells[cell] = archiveEntry{genotype: genotype, fitness: fitness}
	return true
}

func (a archive) public() map[Cell]Genotype {
	out := make(map[Cell]Genotype, len(a.cells))
	for cell, entry := range a.cells {
		out[cell] = entry.genotype
	}
	return out
}
