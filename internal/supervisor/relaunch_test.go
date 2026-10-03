package supervisor

import (
	"testing"
	"time"
)

func TestRelaunchDue(t *testing.T) {
	t0 := time.Unix(1000, 0)
	gone, cool := 30*time.Second, 90*time.Second
	// Present: never relaunch, absence resets.
	if since, launch := relaunchDue(t0, t0.Add(-time.Hour), time.Time{}, true, gone, cool); launch || !since.IsZero() {
		t.Fatal("present editor must not relaunch")
	}
	// First absent tick only starts the clock.
	since, launch := relaunchDue(t0, time.Time{}, time.Time{}, false, gone, cool)
	if launch || !since.Equal(t0) {
		t.Fatal("first absence must only start the clock")
	}
	// Not absent long enough.
	if _, launch := relaunchDue(t0.Add(10*time.Second), since, time.Time{}, false, gone, cool); launch {
		t.Fatal("relaunched before the sustained-absence window")
	}
	// Sustained absence → relaunch, and the absence clock resets.
	since2, launch := relaunchDue(t0.Add(31*time.Second), since, time.Time{}, false, gone, cool)
	if !launch || !since2.IsZero() {
		t.Fatal("sustained absence must relaunch")
	}
	// Within the cooldown of a launch → no second launch even if absent long enough.
	lastLaunch := t0.Add(31 * time.Second)
	if _, launch := relaunchDue(t0.Add(100*time.Second), t0.Add(40*time.Second), lastLaunch, false, gone, cool); launch {
		t.Fatal("relaunched inside the cooldown")
	}
}
