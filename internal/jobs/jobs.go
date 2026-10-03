// Package jobs is a small async-job registry for long-running operations
// (build_compile, editor_restart, pie_wait_until) that must not block the MCP
// call or the single-flight editor channel. A job runs in a goroutine, streams
// progress lines, and is pollable/cancellable by id (GO_REWRITE_PLAN.md §9 Group I).
package jobs

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Status is a job's lifecycle state.
type Status string

const (
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

// Func is the work a job performs. It should call progress() to stream lines and
// respect ctx cancellation. Its return value becomes the job result.
type Func func(ctx context.Context, progress func(string)) (any, error)

// Job is a single async job.
type Job struct {
	ID string

	mu       sync.Mutex
	status   Status
	progress []string
	result   any
	err      error
	owner    string // optional owner tag (see CancelOwned); guarded by mu
	cancel   context.CancelFunc
	done     chan struct{}
	changed  chan struct{} // closed and replaced on every progress line / finish
}

// Snapshot is an immutable view of a job's state.
type Snapshot struct {
	ID       string
	Status   Status
	Progress []string
	Result   any
	Err      string
}

func (j *Job) addProgress(line string) {
	j.mu.Lock()
	j.progress = append(j.progress, line)
	j.signalLocked()
	j.mu.Unlock()
}

// signalLocked wakes every WaitFor caller. Callers hold j.mu.
func (j *Job) signalLocked() {
	close(j.changed)
	j.changed = make(chan struct{})
}

func (j *Job) finish(status Status, result any, err error) {
	j.mu.Lock()
	j.status = status
	j.result = result
	j.err = err
	j.signalLocked()
	j.mu.Unlock()
	close(j.done)
}

// Snapshot returns the current state (safe to call concurrently).
func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	errStr := ""
	if j.err != nil {
		errStr = j.err.Error()
	}
	prog := make([]string, len(j.progress))
	copy(prog, j.progress)
	return Snapshot{ID: j.ID, Status: j.status, Progress: prog, Result: j.result, Err: errStr}
}

// Status returns the job's current status (cheap; no allocation).
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// Owner returns the job's owner tag.
func (j *Job) Owner() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.owner
}

// SetOwner changes the owner tag (e.g. "" once a session-scoped wait is over, so the
// work is no longer cancelled with that session).
func (j *Job) SetOwner(owner string) {
	j.mu.Lock()
	j.owner = owner
	j.mu.Unlock()
}

// Cancel requests cancellation; the job's ctx is cancelled and its final status
// becomes Cancelled unless it already finished.
func (j *Job) Cancel() {
	j.cancel()
}

// WaitFor blocks until the job finishes, d elapses, or ctx is done, calling
// onProgress (if non-nil) with a fresh snapshot each time a progress line arrives.
// It reports whether the job finished.
func (j *Job) WaitFor(ctx context.Context, d time.Duration, onProgress func(Snapshot)) (Snapshot, bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		j.mu.Lock()
		ch := j.changed
		j.mu.Unlock()
		select {
		case <-j.done:
			return j.Snapshot(), true
		case <-ch:
			if onProgress != nil {
				onProgress(j.Snapshot())
			}
		case <-timer.C:
			return j.Snapshot(), false
		case <-ctx.Done():
			return j.Snapshot(), false
		}
	}
}

// Wait blocks until the job finishes and returns its final snapshot (for tests
// and synchronous callers).
func (j *Job) Wait() Snapshot {
	<-j.done
	return j.Snapshot()
}

// Registry holds and runs jobs.
type Registry struct {
	seq  atomic.Uint64
	mu   sync.RWMutex
	jobs map[string]*Job
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]*Job)}
}

// Start launches fn as a new job under parent ctx and returns the job. The job's
// context is derived from ctx and cancellable via Job.Cancel.
func (r *Registry) Start(ctx context.Context, fn Func) *Job {
	return r.StartOwned(ctx, "", fn)
}

// StartOwned is Start with an owner tag (see CancelOwned).
func (r *Registry) StartOwned(ctx context.Context, owner string, fn Func) *Job {
	id := fmt.Sprintf("job-%d", r.seq.Add(1))
	jctx, cancel := context.WithCancel(ctx)
	j := &Job{ID: id, owner: owner, status: Running, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{})}

	r.mu.Lock()
	r.jobs[id] = j
	r.mu.Unlock()

	go func() {
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				j.finish(Failed, nil, fmt.Errorf("job panicked: %v", rec))
			}
		}()
		result, err := fn(jctx, j.addProgress)
		switch {
		case err != nil && jctx.Err() != nil:
			j.finish(Cancelled, nil, jctx.Err())
		case err != nil:
			j.finish(Failed, nil, err)
		default:
			j.finish(Succeeded, result, nil)
		}
	}()
	return j
}

// Get returns a job by id.
func (r *Registry) Get(id string) (*Job, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[id]
	return j, ok
}

// HasRunning reports whether any job is still Running. The daemon idle-sweep uses
// this to REFUSE reclaiming a lease whose holder has async work in flight (e.g. a
// long build_compile that produces no tool calls for minutes).
func (r *Registry) HasRunning() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, j := range r.jobs {
		if j.Status() == Running {
			return true
		}
	}
	return false
}

// CancelOwned cancels every running job with the given owner tag and returns how
// many it cancelled.
func (r *Registry) CancelOwned(owner string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, j := range r.jobs {
		if j.Owner() == owner && j.Status() == Running {
			j.Cancel()
			n++
		}
	}
	return n
}

// List returns snapshots of every job, oldest first.
func (r *Registry) List() []Snapshot {
	r.mu.RLock()
	jobs := make([]*Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		jobs = append(jobs, j)
	}
	r.mu.RUnlock()
	sort.Slice(jobs, func(a, b int) bool { return jobSeq(jobs[a].ID) < jobSeq(jobs[b].ID) })
	out := make([]Snapshot, len(jobs))
	for i, j := range jobs {
		out[i] = j.Snapshot()
	}
	return out
}

func jobSeq(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "job-"))
	return n
}
