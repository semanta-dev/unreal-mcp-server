package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestJobSucceeds(t *testing.T) {
	r := NewRegistry()
	j := r.Start(context.Background(), func(ctx context.Context, progress func(string)) (any, error) {
		progress("step 1")
		progress("step 2")
		return "ok", nil
	})
	snap := j.Wait()
	if snap.Status != Succeeded {
		t.Fatalf("status = %s, want succeeded", snap.Status)
	}
	if snap.Result != "ok" {
		t.Fatalf("result = %v", snap.Result)
	}
	if len(snap.Progress) != 2 || snap.Progress[0] != "step 1" {
		t.Fatalf("progress = %v", snap.Progress)
	}
}

func TestJobFails(t *testing.T) {
	r := NewRegistry()
	j := r.Start(context.Background(), func(ctx context.Context, _ func(string)) (any, error) {
		return nil, errors.New("boom")
	})
	snap := j.Wait()
	if snap.Status != Failed || snap.Err != "boom" {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestJobPanicIsCaught(t *testing.T) {
	r := NewRegistry()
	j := r.Start(context.Background(), func(ctx context.Context, _ func(string)) (any, error) {
		panic("kaboom")
	})
	snap := j.Wait()
	if snap.Status != Failed {
		t.Fatalf("panic should fail the job, got %s", snap.Status)
	}
}

func TestJobCancel(t *testing.T) {
	r := NewRegistry()
	started := make(chan struct{})
	j := r.Start(context.Background(), func(ctx context.Context, _ func(string)) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	<-started
	j.Cancel()
	snap := j.Wait()
	if snap.Status != Cancelled {
		t.Fatalf("status = %s, want cancelled", snap.Status)
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	j := r.Start(context.Background(), func(ctx context.Context, _ func(string)) (any, error) { return 1, nil })
	j.Wait()
	got, ok := r.Get(j.ID)
	if !ok || got.ID != j.ID {
		t.Fatalf("Get(%s) failed", j.ID)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get of unknown id should fail")
	}
}

func TestJobProgressConcurrentSafe(t *testing.T) {
	r := NewRegistry()
	j := r.Start(context.Background(), func(ctx context.Context, progress func(string)) (any, error) {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); progress("x") }()
		}
		wg.Wait()
		return nil, nil
	})
	// concurrently snapshot while it runs
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = j.Snapshot()
	}
	snap := j.Wait()
	if snap.Status != Succeeded || len(snap.Progress) != 20 {
		t.Fatalf("snap = %+v", snap)
	}
}
