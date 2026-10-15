package client

import (
	"context"
	"sync"
)

// Process-wide, not per WC. Remote work leaves two slots for local inspection.
// Slots cover the whole child lifetime (including pipe drain), not just Start.
// Admission does not consume the child's execution timeout; the caller's
// deadline still bounds the wait. This never retries an operation.
type nativeProcessBudget struct {
	mu             sync.Mutex
	all, remote    chan struct{}
	waiting        [2][]*nativeWaiter
	interactiveRun int
}

type nativeWaiter struct {
	ctx     context.Context
	remote  bool
	ready   chan struct{}
	granted bool
}

const nativeInteractiveBurst = 3

func newNativeProcessBudget() *nativeProcessBudget {
	return &nativeProcessBudget{all: make(chan struct{}, 4), remote: make(chan struct{}, 2)}
}

var nativeProcesses = newNativeProcessBudget()

type nativeRemoteContextKey struct{}
type nativeInteractiveReadKey struct{}

// WithInteractiveRead marks a short user-requested metadata read. Do not use
// for background refresh, content transfers or mutations. It changes admission
// only, not authorization, process limits, retries or already running work.
func WithInteractiveRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeInteractiveReadKey{}, true)
}

func (b *nativeProcessBudget) acquire(ctx context.Context, remote bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	class := 0
	if interactive, _ := ctx.Value(nativeInteractiveReadKey{}).(bool); interactive {
		class = 1
	}
	w := &nativeWaiter{ctx: ctx, remote: remote, ready: make(chan struct{})}
	b.mu.Lock()
	b.waiting[class] = append(b.waiting[class], w)
	b.dispatch()
	b.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	}
	b.mu.Lock()
	if err := ctx.Err(); err != nil {
		if w.granted {
			b.put(w)
		} else {
			for i, item := range b.waiting[class] {
				if item == w {
					b.remove(class, i)
					break
				}
			}
		}
		b.dispatch()
		b.mu.Unlock()
		return nil, err
	}
	b.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { b.mu.Lock(); defer b.mu.Unlock(); b.put(w); b.dispatch() }) }, nil
}

// Below: mu must be held. Reserve both permits together, never a partial
// reservation. Skip ineligible remote waiters so local inspection can run.
func (b *nativeProcessBudget) eligible(class int) int {
	for i := 0; i < len(b.waiting[class]); {
		w := b.waiting[class][i]
		if w.ctx.Err() != nil {
			b.remove(class, i)
			continue
		}
		if !w.remote || len(b.remote) < cap(b.remote) {
			return i
		}
		i++
	}
	return -1
}

func (b *nativeProcessBudget) remove(class, index int) *nativeWaiter {
	q := b.waiting[class]
	w := q[index]
	copy(q[index:], q[index+1:])
	q[len(q)-1] = nil
	b.waiting[class] = q[:len(q)-1]
	return w
}

func (b *nativeProcessBudget) dispatch() {
	for len(b.all) < cap(b.all) {
		background, interactive := b.eligible(0), b.eligible(1)
		class, index := 0, background
		if interactive >= 0 && (background < 0 || b.interactiveRun < nativeInteractiveBurst) {
			class, index = 1, interactive
		}
		if index < 0 {
			return
		}
		w := b.remove(class, index)
		b.all <- struct{}{}
		if w.remote {
			b.remote <- struct{}{}
		}
		w.granted = true
		if class == 1 {
			if b.interactiveRun < nativeInteractiveBurst {
				b.interactiveRun++
			}
		} else {
			b.interactiveRun = 0
		}
		close(w.ready)
	}
}

func (b *nativeProcessBudget) put(w *nativeWaiter) {
	<-b.all
	if w.remote {
		<-b.remote
	}
	w.granted = false
}
