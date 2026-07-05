package explore

import (
	"math"
	"math/rand"
	"sort"

	"github.com/jdziat/unreal-mcp-server/internal/balance"
)

const (
	entropyBins = 5
	slopeBins   = 5
	noveltyBins = 4
)

var gateGrid = balance.Grid{SSteps: 5, ESteps: 5, SplashSteps: 5}

type Genotype struct {
	Policy   balance.Policy
	Scaffold balance.Scaffold
}

type Cell struct {
	D1, D2, D3 int
}

type Config struct {
	Evaluations int
	Seed        int64
	TopK        int
	Donors      []balance.Scaffold
}

type Result struct {
	Archive map[Cell]Genotype
	Elites  []Genotype
	Filled  int
}

func Search(seed Genotype, cfg Config) Result {
	rng := rand.New(rand.NewSource(cfg.Seed))
	if cfg.TopK <= 0 {
		cfg.TopK = 8
	}

	a := newArchive()
	seed = project(seed)
	if fit, ok := evaluate(seed); ok {
		a.insert(descriptor(seed), seed, fit)
	}

	for i := 0; i < cfg.Evaluations; i++ {
		parent, ok := randomArchiveMember(a, rng)
		if !ok {
			parent = seed
		}
		candidate := vary(parent, cfg, rng)
		candidate = project(candidate)
		fit, ok := evaluate(candidate)
		if !ok {
			continue
		}
		a.insert(descriptor(candidate), candidate, fit)
	}

	return Result{
		Archive: a.public(),
		Elites:  topElites(a, cfg.TopK),
		Filled:  len(a.cells),
	}
}

func evaluate(g Genotype) (float64, bool) {
	out := balance.Simulate(g.Scaffold, g.Policy)
	if !out.Survived {
		return 0, false
	}
	gate := balance.Gate(g.Scaffold, gateGrid)
	if !gate.Pass || gate.Liveness < 0.85 {
		return 0, false
	}

	// Persona-delight is intentionally out of scope for this package; keep the
	// hook explicit so that future delight scoring can be added without changing
	// the archive contract.
	return out.Margin + 0, true
}

func descriptor(g Genotype) Cell {
	return Cell{
		D1: clampInt(entropyBin(g), 0, entropyBins-1),
		D2: clampInt(slopeBin(g), 0, slopeBins-1),
		D3: clampInt(noveltyBin(g.Scaffold), 0, noveltyBins-1),
	}
}

func entropyBin(g Genotype) int {
	seq := dominantActionSequence(g)
	if len(seq) == 0 {
		return 0
	}
	counts := [4]int{}
	for _, action := range seq {
		counts[action]++
	}
	entropy := 0.0
	for _, count := range counts {
		if count == 0 {
			continue
		}
		p := float64(count) / float64(len(seq))
		entropy -= p * math.Log(p)
	}

	// D1 formula: normalized Shannon entropy of the per-wave dominant action
	// sequence over {single, aoe, expand, bank}. Repeating one action gives 0;
	// using all four evenly approaches 1.
	normalized := entropy / math.Log(4)
	return binUnit(normalized, entropyBins)
}

func dominantActionSequence(g Genotype) []int {
	sc := g.Scaffold
	p := g.Policy
	p.S = clamp01(p.S)
	p.E = clamp01(p.E)
	if p.Splash < sc.SplashMin {
		p.Splash = sc.SplashMin
	}

	d := sc.BaseDamage
	reserve := 0.0
	seq := make([]int, 0, len(sc.Waves))
	for _, w := range sc.Waves {
		if w.HP <= 0 || w.Count <= 0 || sc.Spread <= 0 {
			continue
		}

		expandD := d * (1 + sc.ExpandGrowth)
		bankD := sc.BaseDamage + reserve
		dw := (1-p.E)*bankD + p.E*expandD
		single := math.Min((1-p.S)*dw/w.HP, float64(maxInt(1, sc.Focus)))
		perTarget := p.S * dw * p.Splash / float64(maxInt(1, sc.Spread))
		aoe := float64(w.Count)
		if perTarget < w.HP {
			aoe *= perTarget / w.HP
		}
		expand := p.E * math.Max(0, expandD-bankD) / math.Max(1, w.HP)
		bank := (1 - p.E) * math.Max(0, bankD-expandD) / math.Max(1, w.HP)

		seq = append(seq, argmax4(single, aoe, expand, bank))

		if p.E >= 0.5 {
			d = expandD
		} else {
			reserve += sc.BaseDamage * 0.5
			if w.Spike {
				reserve = 0
			}
		}
	}
	return seq
}

func slopeBin(g Genotype) int {
	scales := []float64{0.6, 0.8, 1.0, 1.2, 1.4}
	xMean := 0.0
	yMean := 0.0
	ys := make([]float64, len(scales))
	for i, scale := range scales {
		out := balance.Simulate(scaleScaffold(g.Scaffold, scale), g.Policy)
		ys[i] = out.Margin / math.Max(1, totalDemand(g.Scaffold))
		xMean += scale
		yMean += ys[i]
	}
	xMean /= float64(len(scales))
	yMean /= float64(len(scales))

	num := 0.0
	den := 0.0
	for i, scale := range scales {
		dx := scale - xMean
		num += dx * (ys[i] - yMean)
		den += dx * dx
	}
	slope := 0.0
	if den > 0 {
		slope = num / den
	}

	// D2 formula: linear-regression slope of normalized survival margin as wave
	// HP/count scales from 0.6 to 1.4. Flat or positive slopes are walkovers;
	// increasingly negative slopes are steeper difficulty cliffs.
	severity := clamp01(-slope * 6)
	return binUnit(severity, slopeBins)
}

func noveltyBin(sc balance.Scaffold) int {
	events := noveltyEvents(sc)
	if len(events) <= 1 {
		return 3
	}
	if len(sc.Waves) <= 1 {
		return 3
	}
	sum := 0.0
	for _, idx := range events {
		sum += float64(idx) / float64(len(sc.Waves)-1)
	}
	center := sum / float64(len(events))

	// D3 formula: temporal center-of-mass of new-composition events. Sparse
	// schedules are their own bin; otherwise centers classify front/even/back.
	switch {
	case center < 0.33:
		return 0
	case center < 0.66:
		return 1
	default:
		return 2
	}
}

func noveltyEvents(sc balance.Scaffold) []int {
	if len(sc.Waves) == 0 {
		return nil
	}
	events := []int{0}
	prev := composition(sc, sc.Waves[0])
	for i := 1; i < len(sc.Waves); i++ {
		cur := composition(sc, sc.Waves[i])
		if cur != prev {
			events = append(events, i)
			prev = cur
		}
	}
	return events
}

func composition(sc balance.Scaffold, w balance.Wave) int {
	kind := 1
	if w.Count <= maxInt(1, sc.Focus) {
		kind = 0
	}
	if w.Count >= maxInt(1, sc.Spread*2) {
		kind = 2
	}
	if w.Spike {
		kind += 4
	}
	return kind
}

func vary(parent Genotype, cfg Config, rng *rand.Rand) Genotype {
	switch rng.Intn(3) {
	case 0:
		return transplant(parent, cfg.Donors, rng)
	case 1:
		return mutate(parent, rng)
	default:
		return repurpose(parent, rng)
	}
}

func transplant(parent Genotype, donors []balance.Scaffold, rng *rand.Rand) Genotype {
	// Operator 1 is fuel-gated: it needs at least two donor scaffolds so the
	// archive cannot hallucinate axis fuel from a single example.
	if len(donors) < 2 {
		return parent
	}
	donor := donors[rng.Intn(len(donors))]
	out := parent
	out.Scaffold.Waves = cloneWaves(donor.Waves)
	out.Scaffold.Focus = donor.Focus
	out.Scaffold.Spread = donor.Spread
	out.Scaffold.SplashMin = donor.SplashMin
	out.Scaffold.ExpandGrowth = donor.ExpandGrowth
	if rng.Intn(2) == 0 {
		out.Scaffold.BaseDamage = donor.BaseDamage
	}
	return out
}

func mutate(g Genotype, rng *rand.Rand) Genotype {
	out := g
	out.Policy.S = clamp01(out.Policy.S + rng.NormFloat64()*0.22)
	out.Policy.E = clamp01(out.Policy.E + rng.NormFloat64()*0.22)
	out.Policy.Splash = clamp01(out.Policy.Splash + rng.NormFloat64()*0.18)

	out.Scaffold.BaseDamage = clampFloat(out.Scaffold.BaseDamage+rng.NormFloat64()*10, 20, 220)
	out.Scaffold.ExpandGrowth = clampFloat(out.Scaffold.ExpandGrowth+rng.NormFloat64()*0.08, 0, 1)
	out.Scaffold.SplashMin = clampFloat(out.Scaffold.SplashMin+rng.NormFloat64()*0.03, 0.05, 0.8)
	out.Scaffold.Focus = clampInt(out.Scaffold.Focus+rng.Intn(3)-1, 1, 20)
	out.Scaffold.Spread = clampInt(out.Scaffold.Spread+rng.Intn(3)-1, 1, 30)

	if len(out.Scaffold.Waves) > 0 {
		idx := rng.Intn(len(out.Scaffold.Waves))
		w := out.Scaffold.Waves[idx]
		w.Count = clampInt(w.Count+int(math.Round(rng.NormFloat64()*3)), 1, 40)
		w.HP = clampFloat(w.HP+rng.NormFloat64()*8, 1, 120)
		if rng.Float64() < 0.12 {
			w.Spike = !w.Spike
		}
		out.Scaffold.Waves = cloneWaves(out.Scaffold.Waves)
		out.Scaffold.Waves[idx] = w
	}
	return out
}

func repurpose(g Genotype, _ *rand.Rand) Genotype {
	// Operator 3 is an asset identity relabel. It changes skin-deep identity
	// only, so this pure-Go model intentionally leaves behavior and BD intact.
	return g
}

func project(g Genotype) Genotype {
	g.Policy.S = clamp01(g.Policy.S)
	g.Policy.E = clamp01(g.Policy.E)
	g.Policy.Splash = clamp01(g.Policy.Splash)
	if g.Policy.Splash < g.Scaffold.SplashMin {
		g.Policy.Splash = g.Scaffold.SplashMin
	}
	if g.Scaffold.SplashMin < 0 {
		g.Scaffold.SplashMin = 0
	}
	if g.Scaffold.SplashMin > 1 {
		g.Scaffold.SplashMin = 1
	}
	if g.Scaffold.Focus < 1 {
		g.Scaffold.Focus = 1
	}
	if g.Scaffold.Spread < 1 {
		g.Scaffold.Spread = 1
	}
	if g.Scaffold.BaseDamage < 1 {
		g.Scaffold.BaseDamage = 1
	}
	for i := range g.Scaffold.Waves {
		if g.Scaffold.Waves[i].Count < 1 {
			g.Scaffold.Waves[i].Count = 1
		}
		if g.Scaffold.Waves[i].HP < 1 {
			g.Scaffold.Waves[i].HP = 1
		}
	}
	return g
}

func randomArchiveMember(a archive, rng *rand.Rand) (Genotype, bool) {
	if len(a.cells) == 0 {
		return Genotype{}, false
	}
	cells := sortedCells(a.cells)
	return a.cells[cells[rng.Intn(len(cells))]].genotype, true
}

func sortedCells(cells map[Cell]archiveEntry) []Cell {
	out := make([]Cell, 0, len(cells))
	for cell := range cells {
		out = append(out, cell)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].D1 != out[j].D1 {
			return out[i].D1 < out[j].D1
		}
		if out[i].D2 != out[j].D2 {
			return out[i].D2 < out[j].D2
		}
		return out[i].D3 < out[j].D3
	})
	return out
}

func topElites(a archive, k int) []Genotype {
	entries := make([]archiveEntry, 0, len(a.cells))
	for _, entry := range a.cells {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].fitness != entries[j].fitness {
			return entries[i].fitness > entries[j].fitness
		}
		ci := descriptor(entries[i].genotype)
		cj := descriptor(entries[j].genotype)
		if ci.D1 != cj.D1 {
			return ci.D1 < cj.D1
		}
		if ci.D2 != cj.D2 {
			return ci.D2 < cj.D2
		}
		return ci.D3 < cj.D3
	})
	if k > len(entries) {
		k = len(entries)
	}
	out := make([]Genotype, k)
	for i := range out {
		out[i] = entries[i].genotype
	}
	return out
}

func scaleScaffold(sc balance.Scaffold, scale float64) balance.Scaffold {
	out := sc
	out.Waves = cloneWaves(sc.Waves)
	for i := range out.Waves {
		out.Waves[i].Count = maxInt(1, int(math.Round(float64(out.Waves[i].Count)*scale)))
		out.Waves[i].HP = math.Max(1, out.Waves[i].HP*scale)
	}
	return out
}

func totalDemand(sc balance.Scaffold) float64 {
	total := 0.0
	for _, w := range sc.Waves {
		total += float64(w.Count) * w.HP
	}
	return total
}

func cloneWaves(waves []balance.Wave) []balance.Wave {
	out := make([]balance.Wave, len(waves))
	copy(out, waves)
	return out
}

func binUnit(v float64, bins int) int {
	return clampInt(int(math.Floor(clamp01(v)*float64(bins))), 0, bins-1)
}

func argmax4(a, b, c, d float64) int {
	bestIdx := 0
	best := a
	if b > best {
		bestIdx, best = 1, b
	}
	if c > best {
		bestIdx, best = 2, c
	}
	if d > best {
		bestIdx = 3
	}
	return bestIdx
}

func clamp01(v float64) float64 {
	return clampFloat(v, 0, 1)
}

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
