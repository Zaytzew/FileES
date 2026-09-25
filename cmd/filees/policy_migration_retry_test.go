package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"filees/pkg/talk"
)

type heldMutex struct{ mu sync.Mutex }

func (m *heldMutex) Lock(ctx context.Context, _ string) (func(), error) {
	m.mu.Lock()
	return m.mu.Unlock, nil
}

// Owner's production, 2026-09-25: the migration deferred on a dirty working
// copy waited for the next repository start although the change that blocked
// it was published minutes later. It now retries after each publication,
// never beside a running commit, and stops once it completes.
func TestDeferredPolicyMigrationRetriesAfterPublicationUnderTheRepositoryMutex(t *testing.T) {
	mutex := &heldMutex{}
	wake := make(chan struct{}, 1)
	calls := make(chan struct{}, 4)
	results := []bool{true, false} // deferred once more, then done
	done := make(chan struct{})
	go func() {
		defer close(done)
		retryEditingPolicyMigration(t.Context(), wake, nil, mutex, "svn://repo", func(context.Context) bool {
			calls <- struct{}{}
			result := results[0]
			results = results[1:]
			return result
		}, talk.With("test"))
	}()

	// The commit that confirmed the publication still holds the mutex.
	unlock, _ := mutex.Lock(t.Context(), "")
	wake <- struct{}{}
	select {
	case <-calls:
		t.Fatal("migration ran beside the commit holding the repository mutex")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	<-calls

	wake <- struct{}{}
	<-calls
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not stop after the migration completed")
	}
}

func TestDeferredPolicyMigrationRetryStopsWithTheInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		retryEditingPolicyMigration(ctx, make(chan struct{}), nil, nil, "", func(context.Context) bool { return true }, talk.With("test"))
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry outlived its repository instance")
	}
}
