package runtime

import (
	"context"
	"errors"
	"sync"
)

// ErrQuiescing is a retryable refusal to begin new work, not a failure of an
// operation that was already admitted. Callers must preserve queued intent.
var ErrQuiescing = errors.New("operation admission is closed")

// Admission tracks complete top-level operations, including their durable
// result writes. It is NOT a memory monitor, a worker cancellation mechanism,
// or evidence that unregistered execution paths are safe to restart.
//
// A zero value is ready to use. Acquire once at the operation boundary; nested
// helpers share that operation's lifetime rather than acquiring again. An
// asynchronous child must remain covered until it finishes, not just launches.
type Admission struct {
	mu     sync.Mutex
	active int
	drain  *admissionDrain
}

type admissionDrain struct {
	done chan struct{}
}

// Enter atomically races with Quiesce: work is either counted before the
// barrier, or refused. The returned release is idempotent and concurrency-safe.
func (a *Admission) Enter() (release func(), err error) {
	a.mu.Lock()
	if a.drain != nil {
		a.mu.Unlock()
		return nil, ErrQuiescing
	}
	a.active++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.active--
			if a.active == 0 && a.drain != nil {
				close(a.drain.done)
			}
		})
	}, nil
}

// Quiesce closes admission before waiting. Success holds it closed until the
// caller invokes resume (or exits after an independently verified safe restart).
// Cancellation reopens admission, without cancelling any admitted operation.
// Concurrent attempts cannot reopen or release another attempt's barrier.
func (a *Admission) Quiesce(ctx context.Context) (resume func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.drain != nil {
		a.mu.Unlock()
		return nil, ErrQuiescing
	}
	drain := &admissionDrain{done: make(chan struct{})}
	a.drain = drain
	if a.active == 0 {
		close(drain.done)
	}
	a.mu.Unlock()
	var once sync.Once
	resume = func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.drain == drain {
				a.drain = nil
			}
		})
	}
	select {
	case <-ctx.Done():
		resume()
		return nil, ctx.Err()
	case <-drain.done:
		// Do not authorize a restart from an already-cancelled request if both
		// channels became ready together.
		if err := ctx.Err(); err != nil {
			resume()
			return nil, err
		}
		return resume, nil
	}
}

// AdmissionState is diagnostic only: reading zero active work does not seal
// admission. Only a successful Quiesce holds that guarantee for tracked work.
type AdmissionState struct {
	Active int
	Closed bool
}

func (a *Admission) Snapshot() AdmissionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AdmissionState{Active: a.active, Closed: a.drain != nil}
}
