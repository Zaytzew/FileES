package runtime

import (
	"context"
	"sync"
	"sync/atomic"
)

type lifetimeKey struct{}

// Lifetime joins daemon-owned background work before process replacement.
// Parents must remain joined until they have launched all their children.
// Admission closes business operations; final local cleanup may still run.
type Lifetime struct {
	errMu      sync.Mutex
	err        error
	Admission  *Admission
	wg         sync.WaitGroup
	memoryStop atomic.Bool
}

func FinalizationError(ctx context.Context, err error) {
	l := lifetimeOf(ctx)
	if l == nil || err == nil || !l.memoryStop.Load() {
		return
	}
	l.errMu.Lock()
	if l.err == nil {
		l.err = err
	}
	l.errMu.Unlock()
}
func (l *Lifetime) Err() error { l.errMu.Lock(); defer l.errMu.Unlock(); return l.err }

func WithLifetime(ctx context.Context, l *Lifetime) context.Context {
	return context.WithValue(ctx, lifetimeKey{}, l)
}

func lifetimeOf(ctx context.Context) *Lifetime {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(lifetimeKey{}).(*Lifetime)
	return l
}

// Go preserves the ordinary standalone behavior without a daemon lifetime.
func Go(ctx context.Context, fn func()) {
	l := lifetimeOf(ctx)
	if l == nil {
		go fn()
		return
	}
	l.wg.Add(1)
	go func() { defer l.wg.Done(); fn() }()
}

func (l *Lifetime) Wait()            { l.wg.Wait() }
func (l *Lifetime) BeginMemoryStop() { l.memoryStop.Store(true) }
func MemoryStopping(ctx context.Context) bool {
	l := lifetimeOf(ctx)
	return l != nil && l.memoryStop.Load()
}

// EnterOperation is for top-level execution only, never nested helpers.
func EnterOperation(ctx context.Context) (func(), error) {
	l := lifetimeOf(ctx)
	if l == nil {
		return func() {}, ctx.Err()
	}
	return l.Admission.EnterContext(ctx)
}
