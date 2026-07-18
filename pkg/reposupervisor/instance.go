package reposupervisor

import (
	"context"
	"errors"
	"sync"
)

type RunFunc func(context.Context) error
type CleanupFunc func(context.Context) error

// ManagedInstance gives one repository pipeline an owned cancellation and
// completion boundary. Stop is idempotent and never returns before Run exits;
// cleanup is then executed exactly once.
type ManagedInstance struct {
	cancel   context.CancelFunc
	done     chan struct{}
	runErr   error
	cleanup  CleanupFunc
	stopOnce sync.Once
	stopDone chan struct{}
	stopErr  error
}

func StartManaged(parent context.Context, run RunFunc, cleanup CleanupFunc) (*ManagedInstance, error) {
	if run == nil {
		return nil, errors.New("repository run function is required")
	}
	ctx, cancel := context.WithCancel(parent)
	instance := &ManagedInstance{cancel: cancel, done: make(chan struct{}), cleanup: cleanup, stopDone: make(chan struct{})}
	go func() { defer close(instance.done); instance.runErr = run(ctx) }()
	return instance, nil
}

func (m *ManagedInstance) Stop(ctx context.Context) error {
	m.stopOnce.Do(func() { go m.stop() })
	select {
	case <-m.stopDone:
		return m.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ManagedInstance) stop() {
	defer close(m.stopDone)
	m.cancel()
	<-m.done
	if m.cleanup != nil {
		m.stopErr = m.cleanup(context.Background())
	}
	if m.stopErr == nil && m.runErr != nil && !errors.Is(m.runErr, context.Canceled) {
		m.stopErr = m.runErr
	}
}
