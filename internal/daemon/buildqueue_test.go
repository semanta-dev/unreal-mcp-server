package daemon

import "testing"

func TestBuildQueueSerializesPerEngine(t *testing.T) {
	q := NewBuildQueue()
	// First build on engine E1 runs immediately.
	if !q.Submit("b1", "E1") {
		t.Fatal("first build should start now")
	}
	if q.State("b1") != BuildRunning {
		t.Fatalf("b1 state = %s", q.State("b1"))
	}
	// Second build on E1 is QUEUED (known-healthy), not started.
	if q.Submit("b2", "E1") {
		t.Fatal("second build on same engine must queue, not start")
	}
	if q.State("b2") != BuildQueued || q.IsRunning("b2") {
		t.Fatalf("b2 should be queued/not-running: %s", q.State("b2"))
	}
	// A build on a DIFFERENT engine runs in parallel.
	if !q.Submit("b3", "E2") {
		t.Fatal("build on a different engine should start now (parallel)")
	}
}

func TestBuildQueueAdvancesOnFinish(t *testing.T) {
	q := NewBuildQueue()
	q.Submit("b1", "E1")
	q.Submit("b2", "E1")
	q.Submit("b3", "E1")
	// b1 finishes -> b2 promoted to running.
	next := q.Finish("b1", true)
	if next != "b2" || q.State("b2") != BuildRunning {
		t.Fatalf("finishing b1 should start b2, got next=%s state=%s", next, q.State("b2"))
	}
	if q.State("b1") != BuildDone {
		t.Fatalf("b1 should be done: %s", q.State("b1"))
	}
	// b2 fails -> b3 promoted (failure still advances the queue).
	next = q.Finish("b2", false)
	if next != "b3" || q.State("b3") != BuildRunning || q.State("b2") != BuildFailed {
		t.Fatalf("finishing b2 (fail) should start b3: next=%s", next)
	}
	// b3 finishes, queue empty -> no next.
	if next := q.Finish("b3", true); next != "" {
		t.Fatalf("empty queue should return no next, got %s", next)
	}
}

func TestBuildQueueCancel(t *testing.T) {
	q := NewBuildQueue()
	q.Submit("b1", "E1") // running
	q.Submit("b2", "E1") // queued
	q.Submit("b3", "E1") // queued
	// Cancel a QUEUED build -> dropped, running build unaffected, no advance.
	if next := q.Cancel("b2"); next != "" {
		t.Fatalf("cancelling a queued build should not advance, got %s", next)
	}
	if q.State("b2") != BuildCancelled {
		t.Fatalf("b2 should be cancelled: %s", q.State("b2"))
	}
	if !q.IsRunning("b1") {
		t.Fatal("cancelling a queued build must not disturb the running one")
	}
	// Cancel the RUNNING build -> frees engine, promotes the next surviving queued (b3).
	next := q.Cancel("b1")
	if next != "b3" || q.State("b3") != BuildRunning {
		t.Fatalf("cancelling running b1 should start b3 (b2 was cancelled), got %s", next)
	}
}
