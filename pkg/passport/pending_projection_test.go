package passport

import (
	"context"
	"testing"
	"time"
)

func TestPendingObserverRestoresAndClearsOnlyAfterConfirmation(t *testing.T) {
	f := newPendingFixture(t)
	var latest []PendingStatus
	observer := func(items []PendingStatus) { latest = items }
	m, err := Open(f.store, f.instance, f.backend, Config{Now: func() time.Time { return f.now }, OnPending: observer})
	if err != nil {
		t.Fatal(err)
	}
	f.cli.loseLock = true
	if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
		t.Fatal("expected pending")
	}
	if len(latest) != 1 || latest[0].Phase != "locking" || latest[0].Path != "Łódź.txt" {
		t.Fatalf("projection=%+v", latest)
	}
	if err := CheckSettledStore(f.store); err == nil {
		t.Fatal("free policy bypassed unresolved intent")
	}
	id := latest[0].ID
	latest[0].Path = "mutated projection"
	m, err = Open(f.store, f.instance, f.backend, Config{Now: func() time.Time { return f.now }, OnPending: observer})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].ID != id || latest[0].Path != "Łódź.txt" {
		t.Fatal("restart lost projection or leaked mutation")
	}
	if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err != nil {
		t.Fatal(err)
	}
	if len(latest) != 0 {
		t.Fatal("confirmed operation still projected")
	}
	if err := CheckSettledStore(f.store); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatReportsErrorsOutsideManagerMutex(t *testing.T) {
	b := newFakeBackend()
	m, err := Open(t.TempDir()+"/passports.json", "instance", b, Config{TTL: time.Second, HeartbeatInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Acquire(t.Context(), []string{"/wc/doc"}, ""); err != nil {
		t.Fatal(err)
	}
	b.partitioned = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := make(chan struct{}, 1)
	m.cfg.OnError = func(error) {
		_ = m.Snapshot()
		select {
		case called <- struct{}{}:
		default:
		}
		cancel()
	}
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("heartbeat error lost or mutex retained")
	}
	<-done
}
