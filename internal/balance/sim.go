package balance

import "math"

type Wave struct {
	Count int
	HP    float64
	Spike bool
}

type Scaffold struct {
	Waves        []Wave
	BaseDamage   float64
	Focus        int
	Spread       int
	SplashMin    float64
	ExpandGrowth float64
}

type Policy struct {
	S      float64
	Splash float64
	E      float64
}

type Outcome struct {
	Survived     bool
	Margin       float64
	WavesCleared int
}

func Simulate(sc Scaffold, p Policy) Outcome {
	p.S = clamp01(p.S)
	p.E = clamp01(p.E)
	p.Splash = math.Max(p.Splash, sc.SplashMin)

	d := sc.BaseDamage
	reserve := 0.0
	out := Outcome{}

	for _, w := range sc.Waves {
		expandD := d * (1 + sc.ExpandGrowth)
		bankD := sc.BaseDamage + reserve
		dw := (1-p.E)*bankD + p.E*expandD

		singleKills := math.Min((1-p.S)*dw/w.HP, float64(sc.Focus))

		perTarget := p.S * dw * p.Splash / float64(sc.Spread)
		var aoeKills float64
		if perTarget >= w.HP {
			aoeKills = float64(w.Count)
		} else {
			aoeKills = float64(w.Count) * (perTarget / w.HP)
		}

		kills := singleKills + aoeKills
		if kills >= float64(w.Count) {
			out.WavesCleared++
			out.Margin += kills - float64(w.Count)
		} else {
			out.Margin -= float64(w.Count) - kills
			out.Survived = false
			return out
		}

		if p.E >= 0.5 {
			d = expandD
		} else {
			reserve += sc.BaseDamage * 0.5
			if w.Spike {
				reserve = 0
			}
		}
	}

	out.Survived = out.WavesCleared == len(sc.Waves)
	return out
}

func TwoAxisScaffold() Scaffold {
	return Scaffold{
		Waves: []Wave{
			{Count: 8, HP: 5},
			{Count: 2, HP: 50},
			{Count: 10, HP: 5, Spike: true},
			{Count: 2, HP: 50},
			{Count: 20, HP: 5},
		},
		BaseDamage:   80,
		Focus:        5,
		Spread:       4,
		SplashMin:    0.15,
		ExpandGrowth: 0.25,
	}
}

func MetronomeScaffold() Scaffold {
	return Scaffold{
		Waves: []Wave{
			{Count: 4, HP: 10},
			{Count: 4, HP: 10},
			{Count: 4, HP: 10},
			{Count: 4, HP: 10},
		},
		BaseDamage:   120,
		Focus:        100,
		Spread:       10000,
		SplashMin:    0.15,
		ExpandGrowth: 0.25,
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
