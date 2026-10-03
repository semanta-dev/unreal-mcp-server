package design

import "testing"

func TestTwoAxisScaffoldCombatAxisFlips(t *testing.T) {
	sc := TwoAxisScaffold()

	elite := sc
	elite.Waves = []Wave{{Count: 2, HP: 50}}
	singleElite := Simulate(elite, Policy{S: 0, Splash: 1, E: 1})
	aoeElite := Simulate(elite, Policy{S: 1, Splash: 1, E: 1})
	if !singleElite.Survived || singleElite.Margin <= aoeElite.Margin {
		t.Fatalf("single-target should beat AoE on elite wave: single=%+v aoe=%+v", singleElite, aoeElite)
	}

	swarm := sc
	swarm.Waves = []Wave{{Count: 20, HP: 5}}
	singleSwarm := Simulate(swarm, Policy{S: 0, Splash: 1, E: 0})
	aoeSwarm := Simulate(swarm, Policy{S: 1, Splash: 1, E: 0})
	if !aoeSwarm.Survived || aoeSwarm.Margin <= singleSwarm.Margin {
		t.Fatalf("AoE should beat single-target on swarm wave: single=%+v aoe=%+v", singleSwarm, aoeSwarm)
	}
}

func TestTwoAxisScaffoldEconomyAxisFlips(t *testing.T) {
	sc := TwoAxisScaffold()

	near := spikeNear(sc)
	bankNear := Simulate(near, Policy{S: 0, Splash: 1, E: 0})
	expandNear := Simulate(near, Policy{S: 0, Splash: 1, E: 1})
	if bankNear.Margin <= expandNear.Margin {
		t.Fatalf("banking should beat expanding right before a spike: bank=%+v expand=%+v", bankNear, expandNear)
	}

	far := spikeFar(sc)
	bankFar := Simulate(far, Policy{S: 0, Splash: 1, E: 0})
	expandFar := Simulate(far, Policy{S: 0, Splash: 1, E: 1})
	if !expandFar.Survived || expandFar.Margin <= bankFar.Margin {
		t.Fatalf("expanding should beat banking when the spike is far: bank=%+v expand=%+v", bankFar, expandFar)
	}
}

func TestTwoAxisScaffoldGatePassesWithFencedSplashCorner(t *testing.T) {
	report := Gate(TwoAxisScaffold(), Grid{SSteps: 5, ESteps: 5, SplashSteps: 5})
	if !report.Pass {
		t.Fatalf("two-axis scaffold gate should pass: %+v", report)
	}
	if report.Liveness < 0.85 {
		t.Fatalf("liveness = %.2f, want >= 0.85", report.Liveness)
	}
	if !contains(report.Fenced, "Splash<SplashMin") {
		t.Fatalf("expected Splash-min corner fenced, got %#v", report.Fenced)
	}
}

func TestMetronomeScaffoldFailsGate(t *testing.T) {
	sweep := Sweep(MetronomeScaffold(), Grid{SSteps: 5, ESteps: 5, SplashSteps: 5})
	if !sweep.DominantPolicy {
		t.Fatalf("metronome should have a dominant policy: %+v", sweep)
	}

	report := Gate(MetronomeScaffold(), Grid{SSteps: 5, ESteps: 5, SplashSteps: 5})
	if report.Pass {
		t.Fatalf("metronome gate should fail: %+v", report)
	}
}

func contains(vals []string, want string) bool {
	for _, val := range vals {
		if val == want {
			return true
		}
	}
	return false
}
