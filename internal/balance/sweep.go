package balance

import "math"

type Grid struct {
	SSteps      int
	ESteps      int
	SplashSteps int
}

type SweepReport struct {
	DominantPolicy      bool
	DegenerateOptimum   bool
	AxisLiveness        float64
	FencedCorners       []string
	WinRateBySpike      map[bool]float64
	BestPolicyByVariant map[string]Policy
}

type scoredPolicy struct {
	p Policy
	o Outcome
}

func Sweep(sc Scaffold, grid Grid) SweepReport {
	grid = normalizeGrid(grid)
	policies := enumeratePolicies(sc, grid)
	variants := map[string]Scaffold{
		"base":       sc,
		"swarm":      swarmHeavy(sc),
		"elite":      eliteHeavy(sc),
		"spike_near": spikeNear(sc),
		"spike_far":  spikeFar(sc),
	}

	bestByVariant := make(map[string]Policy, len(variants))
	var dominant *Policy
	dominantPolicy := true
	for name, variant := range variants {
		best := bestPolicy(variant, policies)
		bestByVariant[name] = best.p
		if dominant == nil {
			p := best.p
			dominant = &p
			continue
		}
		if !samePolicy(*dominant, best.p) {
			dominantPolicy = false
		}
	}

	return SweepReport{
		DominantPolicy:      dominantPolicy,
		DegenerateOptimum:   dominantPolicy || degenerateCorners(bestByVariant),
		AxisLiveness:        axisLiveness(sc, grid),
		FencedCorners:       fencedCorners(sc),
		WinRateBySpike:      winRateBySpike(sc, policies),
		BestPolicyByVariant: bestByVariant,
	}
}

func enumeratePolicies(sc Scaffold, grid Grid) []Policy {
	sVals := gridValues(0, 1, grid.SSteps)
	eVals := gridValues(0, 1, grid.ESteps)
	splashVals := gridValues(sc.SplashMin, 1, grid.SplashSteps)
	policies := make([]Policy, 0, len(sVals)*len(eVals)*len(splashVals))
	for _, s := range sVals {
		for _, e := range eVals {
			for _, splash := range splashVals {
				policies = append(policies, Policy{S: s, E: e, Splash: splash})
			}
		}
	}
	return policies
}

func axisLiveness(sc Scaffold, grid Grid) float64 {
	sVals := gridValues(0, 1, grid.SSteps)
	eVals := gridValues(0, 1, grid.ESteps)
	splashVals := gridValues(sc.SplashMin, 1, grid.SplashSteps)

	swarm := swarmHeavy(sc)
	elite := eliteHeavy(sc)
	near := spikeNear(sc)
	far := spikeFar(sc)

	live := 0
	total := 0
	for _, e := range eVals {
		for _, splash := range splashVals {
			total++
			if bestS(swarm, sVals, e, splash) != bestS(elite, sVals, e, splash) {
				live++
			}
		}
	}

	for _, s := range sVals {
		total++
		if bestE(near, eVals, s, 1) != bestE(far, eVals, s, 1) {
			live++
		}
	}

	if total == 0 {
		return 0
	}
	return float64(live) / float64(total)
}

func bestS(sc Scaffold, sVals []float64, e, splash float64) int {
	bestIdx := 0
	best := scoredPolicy{o: Outcome{Margin: math.Inf(-1)}}
	for i, s := range sVals {
		score := scoredPolicy{p: Policy{S: s, E: e, Splash: splash}}
		score.o = Simulate(sc, score.p)
		if better(score, best) {
			best = score
			bestIdx = i
		}
	}
	return bestIdx
}

func bestE(sc Scaffold, eVals []float64, s, splash float64) int {
	bestIdx := 0
	best := scoredPolicy{o: Outcome{Margin: math.Inf(-1)}}
	for i, e := range eVals {
		score := scoredPolicy{p: Policy{S: s, E: e, Splash: splash}}
		score.o = Simulate(sc, score.p)
		if better(score, best) {
			best = score
			bestIdx = i
		}
	}
	return bestIdx
}

func bestPolicy(sc Scaffold, policies []Policy) scoredPolicy {
	best := scoredPolicy{o: Outcome{Margin: math.Inf(-1)}}
	for _, p := range policies {
		score := scoredPolicy{p: p, o: Simulate(sc, p)}
		if better(score, best) {
			best = score
		}
	}
	return best
}

func better(candidate, current scoredPolicy) bool {
	if candidate.o.Survived != current.o.Survived {
		return candidate.o.Survived
	}
	if candidate.o.WavesCleared != current.o.WavesCleared {
		return candidate.o.WavesCleared > current.o.WavesCleared
	}
	if candidate.o.Margin != current.o.Margin {
		return candidate.o.Margin > current.o.Margin
	}
	if candidate.p.S != current.p.S {
		return candidate.p.S < current.p.S
	}
	if candidate.p.E != current.p.E {
		return candidate.p.E < current.p.E
	}
	return candidate.p.Splash < current.p.Splash
}

func winRateBySpike(sc Scaffold, policies []Policy) map[bool]float64 {
	result := map[bool]float64{false: 0, true: 0}
	counts := map[bool]int{false: 0, true: 0}
	wins := map[bool]int{false: 0, true: 0}
	for _, variant := range []Scaffold{spikeNear(sc), spikeFar(sc)} {
		hasSpike := hasSpike(variant)
		for _, p := range policies {
			counts[hasSpike]++
			if Simulate(variant, p).Survived {
				wins[hasSpike]++
			}
		}
	}
	for spike, count := range counts {
		if count > 0 {
			result[spike] = float64(wins[spike]) / float64(count)
		}
	}
	return result
}

func hasSpike(sc Scaffold) bool {
	for _, w := range sc.Waves {
		if w.Spike {
			return true
		}
	}
	return false
}

func degenerateCorners(bestByVariant map[string]Policy) bool {
	if len(bestByVariant) == 0 {
		return true
	}
	sCorner := true
	eCorner := true
	sameS := true
	sameE := true
	var first *Policy
	for _, p := range bestByVariant {
		if p.S != 0 && p.S != 1 {
			sCorner = false
		}
		if p.E != 0 && p.E != 1 {
			eCorner = false
		}
		if first == nil {
			cp := p
			first = &cp
			continue
		}
		if p.S != first.S {
			sameS = false
		}
		if p.E != first.E {
			sameE = false
		}
	}
	return (sameS && sCorner) || (sameE && eCorner)
}

func fencedCorners(sc Scaffold) []string {
	if sc.SplashMin <= 0 {
		return nil
	}
	return []string{"Splash<SplashMin"}
}

func samePolicy(a, b Policy) bool {
	return a.S == b.S && a.E == b.E && a.Splash == b.Splash
}

func normalizeGrid(grid Grid) Grid {
	if grid.SSteps < 2 {
		grid.SSteps = 2
	}
	if grid.ESteps < 2 {
		grid.ESteps = 2
	}
	if grid.SplashSteps < 1 {
		grid.SplashSteps = 1
	}
	return grid
}

func gridValues(min, max float64, steps int) []float64 {
	if steps <= 1 {
		return []float64{min}
	}
	vals := make([]float64, steps)
	step := (max - min) / float64(steps-1)
	for i := range vals {
		vals[i] = min + float64(i)*step
	}
	return vals
}

func swarmHeavy(sc Scaffold) Scaffold {
	out := sc
	out.Waves = []Wave{
		{Count: 20, HP: 3},
		{Count: 22, HP: 3},
		{Count: 24, HP: 3, Spike: true},
	}
	return out
}

func eliteHeavy(sc Scaffold) Scaffold {
	out := sc
	out.Waves = []Wave{
		{Count: 2, HP: 50},
		{Count: 2, HP: 55},
		{Count: 2, HP: 60, Spike: true},
	}
	return out
}

func spikeNear(sc Scaffold) Scaffold {
	out := sc
	out.Waves = []Wave{
		{Count: 1, HP: 5},
		{Count: 1, HP: 5},
		{Count: 4, HP: 39.5, Spike: true},
	}
	return out
}

func spikeFar(sc Scaffold) Scaffold {
	out := sc
	out.Waves = []Wave{
		{Count: 1, HP: 5},
		{Count: 1, HP: 5},
		{Count: 1, HP: 5},
		{Count: 1, HP: 5},
		{Count: 4, HP: 61, Spike: true},
	}
	return out
}
