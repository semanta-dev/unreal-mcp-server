package balance

type GateReport struct {
	Pass     bool
	Liveness float64
	Fenced   []string
	Reasons  []string
}

func Gate(sc Scaffold, grid Grid) GateReport {
	sweep := Sweep(sc, grid)
	report := GateReport{
		Liveness: sweep.AxisLiveness,
		Fenced:   sweep.FencedCorners,
	}

	if sweep.DominantPolicy {
		report.Reasons = append(report.Reasons, "dominant policy across controlled variants")
	}
	if sweep.DegenerateOptimum {
		report.Reasons = append(report.Reasons, "degenerate optimum")
	}
	if sweep.AxisLiveness < 0.85 {
		report.Reasons = append(report.Reasons, "axis liveness below 0.85")
	}
	if len(sweep.FencedCorners) == 0 && sweep.AxisLiveness < 1 {
		report.Reasons = append(report.Reasons, "collapse corners are not fenced")
	}

	report.Pass = !sweep.DominantPolicy &&
		!sweep.DegenerateOptimum &&
		(sweep.AxisLiveness == 1 || (sweep.AxisLiveness >= 0.85 && len(sweep.FencedCorners) > 0))

	return report
}
