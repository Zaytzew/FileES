package passport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errPartitioned = errors.New("network partition")

type fakeBackend struct {
	locks                    map[string]*Lock
	unlockErrors             map[string]error
	seq, forceCalls, unlocks int
	partitioned              bool
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{locks: map[string]*Lock{}, unlockErrors: map[string]error{}}
}
func (b *fakeBackend) Inspect(_ context.Context, path string) (*Lock, error) {
	if b.partitioned {
		return nil, errPartitioned
	}
	if l := b.locks[path]; l != nil {
		copy := *l
		return &copy, nil
	}
	return nil, nil
}
func (b *fakeBackend) Lock(_ context.Context, path, comment string, force bool) (*Lock, string, error) {
	if b.partitioned {
		return nil, "", errPartitioned
	}
	if b.locks[path] != nil && !force {
		return nil, "", ErrHeldByOther
	}
	if force {
		b.forceCalls++
	}
	b.seq++
	l := &Lock{Token: fmt.Sprintf("token-%d", b.seq), Comment: comment}
	b.locks[path] = l
	copy := *l
	return &copy, "locked", nil
}
func (b *fakeBackend) Unlock(_ context.Context, path string) (string, error) {
	if err := b.unlockErrors[path]; err != nil {
		return "", err
	}
	if b.locks[path] == nil {
		return "", errors.New("not locked")
	}
	delete(b.locks, path)
	b.unlocks++
	return "unlocked", nil
}

func TestReleasePersistsEarlierSuccessBeforeLaterFailure(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	store := filepath.Join(t.TempDir(), "passports.json")
	cfg := Config{Now: func() time.Time { return now }}
	m, err := Open(store, "instance-a", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Acquire(context.Background(), []string{"/wc/a", "/wc/b"}, ""); err != nil {
		t.Fatal(err)
	}
	b.unlockErrors["/wc/b"] = errors.New("unlock b failed")
	if _, err := m.Release(context.Background(), []string{"/wc/a", "/wc/b"}); err == nil {
		t.Fatal("partial release unexpectedly succeeded")
	}
	reopened, err := Open(store, "instance-a", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap := reopened.Snapshot()
	if len(snap) != 1 || snap[0].Path != "/wc/b" {
		t.Fatalf("persisted passports after partial release: %#v", snap)
	}
}

func openTestManager(t *testing.T, b *fakeBackend, now *time.Time, cfg Config) *Manager {
	t.Helper()
	cfg.Now = func() time.Time { return *now }
	m, err := Open(filepath.Join(t.TempDir(), "passports.json"), "instance-a", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAcquirePersistsAuthoritativeFencingToken(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	store := filepath.Join(t.TempDir(), "passports.json")
	m, err := Open(store, "instance-a", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := m.Acquire(context.Background(), []string{"/wc/a.bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FencingToken == "" || got[0].ExpiresAt.Sub(now) != 15*time.Minute || got[0].HardExpiresAt.Sub(now) != 24*time.Hour {
		t.Fatalf("passport=%#v", got)
	}
	reopened, err := Open(store, "instance-a", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if snap := reopened.Snapshot(); len(snap) != 1 || snap[0].FencingToken != got[0].FencingToken {
		t.Fatalf("reopened=%#v", snap)
	}
}

func TestHeartbeatRotatesTokenOnlyAfterOwnershipCheck(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute})
	p, _, _ := m.Acquire(context.Background(), []string{"/wc/a.bin"}, "")
	old := p[0].FencingToken
	now = now.Add(11 * time.Minute)
	if err := m.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	if snap[0].FencingToken == old || b.forceCalls != 1 {
		t.Fatalf("heartbeat=%#v force=%d", snap, b.forceCalls)
	}
	b.locks["/wc/a.bin"] = &Lock{Token: "stolen", Comment: FormatComment(Metadata{PassportID: "other", InstanceUID: "instance-b", IssuedAt: now, ExpiresAt: now.Add(time.Hour), HardExpiresAt: now.Add(time.Hour)})}
	now = now.Add(11 * time.Minute)
	if err := m.Heartbeat(context.Background()); !errors.Is(err, ErrPassportLost) {
		t.Fatalf("error=%v", err)
	}
	if m.Snapshot()[0].State != StateLost || b.forceCalls != 1 {
		t.Fatalf("lost passport was renewed: %#v", m.Snapshot())
	}
}

func TestBeginPublishFreezesHeartbeatTokenRotation(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute})
	path := "/wc/a.bin"
	_, _, _ = m.Acquire(context.Background(), []string{path}, "")
	release, err := m.BeginPublish(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	done := make(chan error, 1)
	go func() { done <- m.Heartbeat(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("heartbeat passed publication barrier: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	// A cleanup path may be reached twice while unwinding an error. The guard is
	// idempotent and must not unlock a future operation or panic.
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b.forceCalls != 1 {
		t.Fatalf("force calls=%d", b.forceCalls)
	}
}

func TestAcquireDoesNotReuseExpiredLocalPassport(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute})
	path := "/wc/a.bin"
	first, _, _ := m.Acquire(context.Background(), []string{path}, "")
	now = now.Add(16 * time.Minute)
	second, _, err := m.Acquire(context.Background(), []string{path}, "")
	if err != nil {
		t.Fatal(err)
	}
	if second[0].PassportID == first[0].PassportID || b.forceCalls != 1 {
		t.Fatalf("expired passport reused: first=%#v second=%#v force=%d", first, second, b.forceCalls)
	}
}

func TestAcquireRollbackDoesNotReleasePreexistingPassport(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{})
	owned := "/wc/already-owned.bin"
	foreign := "/wc/foreign.bin"
	first, _, err := m.Acquire(context.Background(), []string{owned}, "")
	if err != nil {
		t.Fatal(err)
	}
	b.locks[foreign] = &Lock{Token: "foreign", Comment: "ordinary foreign lock"}
	if _, _, err := m.Acquire(context.Background(), []string{owned, foreign}, ""); !errors.Is(err, ErrHeldByOther) {
		t.Fatalf("error=%v", err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].Path != owned || snap[0].FencingToken != first[0].FencingToken {
		t.Fatalf("preexisting passport changed after rollback: %#v", snap)
	}
	if b.locks[owned] == nil || b.unlocks != 0 {
		t.Fatalf("preexisting server lock was released: lock=%#v unlocks=%d", b.locks[owned], b.unlocks)
	}
}

func TestOpenRejectsUnsafeTimingConfiguration(t *testing.T) {
	b := newFakeBackend()
	store := filepath.Join(t.TempDir(), "passports.json")
	if _, err := Open(store, "instance-a", b, Config{TTL: time.Minute, HeartbeatInterval: time.Minute}); err == nil {
		t.Fatal("heartbeat equal to TTL was accepted")
	}
	if _, err := Open(store, "instance-a", b, Config{TTL: time.Hour, HeartbeatInterval: time.Minute, MaxSession: 30 * time.Minute}); err == nil {
		t.Fatal("max session shorter than TTL was accepted")
	}
}

func TestExpiredForeignPassportMayBeTakenButLiveOneMayNot(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	path := "/wc/a.bin"
	b.locks[path] = &Lock{Token: "foreign", Comment: FormatComment(Metadata{PassportID: "foreign", InstanceUID: "instance-b", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)})}
	m := openTestManager(t, b, &now, Config{})
	if _, _, err := m.Acquire(context.Background(), []string{path}, ""); !errors.Is(err, ErrHeldByOther) {
		t.Fatalf("live lock error=%v", err)
	}
	meta, _ := ParseComment(b.locks[path].Comment)
	meta.ExpiresAt = now.Add(-time.Second)
	b.locks[path].Comment = FormatComment(meta)
	if _, _, err := m.Acquire(context.Background(), []string{path}, ""); err != nil {
		t.Fatal(err)
	}
	if b.forceCalls != 1 {
		t.Fatalf("force calls=%d", b.forceCalls)
	}
}

func TestCloseGraceResetsOnActivityAndReleasesAfterPublishedQuiet(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{CloseGrace: 5 * time.Minute, HeartbeatInterval: time.Minute})
	path := "/wc/a.bin"
	_, _, _ = m.Acquire(context.Background(), []string{path}, "")
	m.MarkPublished([]string{path})
	now = now.Add(4 * time.Minute)
	m.Touch(path)
	now = now.Add(2 * time.Minute)
	if err := m.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.locks[path] == nil {
		t.Fatal("activity did not cancel close grace")
	}
	m.MarkPublished([]string{path})
	now = now.Add(5 * time.Minute)
	if err := m.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.locks[path] != nil || len(m.Snapshot()) != 0 {
		t.Fatalf("passport not released: %#v", m.Snapshot())
	}
}

func TestReleaseAllUnlocksOnlyOwnedTokens(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{})
	_, _, _ = m.Acquire(context.Background(), []string{"/wc/a", "/wc/b"}, "")
	b.locks["/wc/b"] = &Lock{Token: "stolen", Comment: "foreign"}
	if err := m.ReleaseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].Path != "/wc/b" || snap[0].State != StateLost || b.unlocks != 1 {
		t.Fatalf("snapshot=%#v unlocks=%d", snap, b.unlocks)
	}
}

// ---- Klasa 1: SIGKILL w połowie Release ----

// TestRestartAfterSIGKILLMidReleaseRecoversThroughHeartbeat models the
// scenario where the process is killed after a successful SVN Unlock but
// before saveLocked() persists the change. On restart the store contains a
// passport that is no longer backed by a server lock. Heartbeat must detect
// the mismatch and mark the entry as StateLost; a subsequent Acquire on the
// same path must then succeed.
func TestRestartAfterSIGKILLMidReleaseRecoversThroughHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	store := filepath.Join(t.TempDir(), "passports.json")
	cfg := Config{Now: func() time.Time { return now }}
	m, err := Open(store, "instance-a", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := m.Acquire(ctx, []string{"/wc/a", "/wc/b"}, ""); err != nil {
		t.Fatal(err)
	}
	// Simulate SIGKILL: SVN unlocked /wc/a on the server but the process died
	// before Manager could call saveLocked(). The store still shows both paths.
	delete(b.locks, "/wc/a")

	m2, err := Open(store, "instance-a", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Snapshot()) != 2 {
		t.Fatalf("expected 2 passports after restart, got %v", m2.Snapshot())
	}

	// Heartbeat detects /wc/a has been externally unlocked → StateLost.
	if err := m2.Heartbeat(ctx); !errors.Is(err, ErrPassportLost) {
		t.Fatalf("Heartbeat = %v, want ErrPassportLost", err)
	}
	var aState string
	for _, p := range m2.Snapshot() {
		if p.Path == "/wc/a" {
			aState = p.State
		}
	}
	if aState != StateLost {
		t.Fatalf("/wc/a state = %q after heartbeat, want %q", aState, StateLost)
	}

	// Re-acquire must succeed: server no longer holds the lock for /wc/a.
	if _, _, err := m2.Acquire(ctx, []string{"/wc/a"}, ""); err != nil {
		t.Fatalf("re-acquire after StateLost: %v", err)
	}
	snap := m2.Snapshot()
	active := 0
	for _, p := range snap {
		if p.State == StateActive {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("expected 2 active passports after re-acquire, got %v", snap)
	}
}

// TestPartialReleaseFailureSavesOnlyRemainingPathsOnDisk verifies the CR-02
// fix: when Release([A, B, C]) succeeds for A then fails on B, the store must
// reflect A's removal so that a restart does not see A as still active.
func TestPartialReleaseFailureSavesOnlyRemainingPathsOnDisk(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	store := filepath.Join(t.TempDir(), "passports.json")
	m, err := Open(store, "instance-a", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := m.Acquire(ctx, []string{"/wc/a", "/wc/b", "/wc/c"}, ""); err != nil {
		t.Fatal(err)
	}
	// cleanPaths sorts alphabetically; /wc/b is second — Release(A) succeeds,
	// Release(B) fails, Release(C) is never attempted.
	b.unlockErrors["/wc/b"] = errors.New("transient unlock failure")
	if _, err := m.Release(ctx, []string{"/wc/a", "/wc/b", "/wc/c"}); err == nil {
		t.Fatal("partial release unexpectedly succeeded")
	}

	m2, err := Open(store, "instance-a", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]bool)
	for _, p := range m2.Snapshot() {
		paths[p.Path] = true
	}
	if paths["/wc/a"] {
		t.Error("successfully released /wc/a still present on disk")
	}
	if !paths["/wc/b"] {
		t.Error("failed-to-release /wc/b missing from disk")
	}
	if !paths["/wc/c"] {
		t.Error("unattempted /wc/c missing from disk")
	}
}

// ---- Klasa 2: token wygasa podczas BeginPublish blokuje Heartbeat ----

// TestTokenHardExpiryDuringBeginPublishBlockedHeartbeat verifies that when
// the clock advances past HardExpiresAt while Heartbeat is blocked on opMu
// (held by BeginPublish), the next Heartbeat after release transitions the
// passport to StateExpired and unlocks the server lock.
func TestTokenHardExpiryDuringBeginPublishBlockedHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute, MaxSession: 30 * time.Minute})
	ctx := context.Background()
	if _, _, err := m.Acquire(ctx, []string{"/wc/doc.txt"}, ""); err != nil {
		t.Fatal(err)
	}
	initialToken := m.Snapshot()[0].FencingToken

	release, err := m.BeginPublish(ctx, []string{"/wc/doc.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// While opMu is held, Heartbeat would block. Time advances past HardExpiresAt.
	now = now.Add(31 * time.Minute)
	release()

	// Heartbeat now runs with time well past HardExpiresAt. It must unlock the
	// server lock and transition the passport to StateExpired.
	if err := m.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat after hard expiry: %v", err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].State != StateExpired {
		t.Fatalf("passport state = %#v, want StateExpired", snap)
	}
	if b.unlocks != 1 {
		t.Fatalf("server unlocks = %d, want 1 (hard-expiry unlock)", b.unlocks)
	}

	// Re-acquire must succeed now that the server lock is free.
	if _, _, err := m.Acquire(ctx, []string{"/wc/doc.txt"}, ""); err != nil {
		t.Fatalf("re-acquire after hard expiry: %v", err)
	}
	snap = m.Snapshot()
	if len(snap) != 1 || snap[0].State != StateActive || snap[0].FencingToken == initialToken {
		t.Fatalf("re-acquired passport = %#v", snap)
	}
}

// ---- Klasa 3: awaria sieci dłuższa niż TTL ----

// TestNetworkPartitionLongerThanTTLAllowsRenewalAfterReconnect verifies that
// when Heartbeat cannot reach the server for longer than TTL, the passport
// is still renewed (via force lock) on the first successful Heartbeat after
// reconnect — provided the server still holds the original token.
func TestNetworkPartitionLongerThanTTLAllowsRenewalAfterReconnect(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute, MaxSession: 2 * time.Hour})
	ctx := context.Background()
	if _, _, err := m.Acquire(ctx, []string{"/wc/doc.txt"}, ""); err != nil {
		t.Fatal(err)
	}
	token0 := m.Snapshot()[0].FencingToken

	// Network partition: Inspect and Lock both fail.
	b.partitioned = true
	now = now.Add(11 * time.Minute)
	if err := m.Heartbeat(ctx); !errors.Is(err, errPartitioned) {
		t.Fatalf("first partitioned heartbeat = %v, want errPartitioned", err)
	}
	now = now.Add(11 * time.Minute) // now T+22min — past ExpiresAt (T+15min)
	if err := m.Heartbeat(ctx); !errors.Is(err, errPartitioned) {
		t.Fatalf("second partitioned heartbeat = %v, want errPartitioned", err)
	}
	// Passport still StateActive locally but renewal has been failing.
	if snap := m.Snapshot(); snap[0].State != StateActive {
		t.Fatalf("partition must not drop local state: %v", snap)
	}

	// Reconnect: server still holds the original token (we never called Unlock).
	b.partitioned = false
	if err := m.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat after reconnect: %v", err)
	}
	snap := m.Snapshot()
	if snap[0].FencingToken == token0 {
		t.Fatal("token not rotated after reconnect renewal")
	}
	if snap[0].State != StateActive {
		t.Fatalf("state = %q after renewal, want active", snap[0].State)
	}
	if b.forceCalls != 1 {
		t.Fatalf("force renewals = %d, want 1", b.forceCalls)
	}
}

// TestNetworkPartitionExceedingHardExpiryRequiresFreshAcquire verifies that
// when the partition outlasts HardExpiresAt, the first Heartbeat after
// reconnect unlocks the server lock, marks the passport StateExpired, and a
// subsequent Acquire issues a new token without a force flag.
func TestNetworkPartitionExceedingHardExpiryRequiresFreshAcquire(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute, MaxSession: 30 * time.Minute})
	ctx := context.Background()
	if _, _, err := m.Acquire(ctx, []string{"/wc/doc.txt"}, ""); err != nil {
		t.Fatal(err)
	}

	// Partition for the entire MaxSession window.
	b.partitioned = true
	now = now.Add(31 * time.Minute)
	if err := m.Heartbeat(ctx); !errors.Is(err, errPartitioned) {
		t.Fatalf("partitioned heartbeat = %v, want errPartitioned", err)
	}

	// Reconnect with time past HardExpiresAt. Server still holds old lock.
	b.partitioned = false
	if err := m.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat after reconnect: %v", err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].State != StateExpired {
		t.Fatalf("passport state = %#v, want StateExpired", snap)
	}
	if b.unlocks != 1 {
		t.Fatalf("server unlocks = %d, want 1", b.unlocks)
	}

	// Fresh Acquire must succeed without force (server lock was already released
	// by Heartbeat's hard-expiry path).
	if _, _, err := m.Acquire(ctx, []string{"/wc/doc.txt"}, ""); err != nil {
		t.Fatalf("re-acquire after hard expiry: %v", err)
	}
	if b.forceCalls != 0 {
		t.Fatalf("re-acquire used force=%d, want 0 (server lock was already free)", b.forceCalls)
	}
	snap = m.Snapshot()
	if len(snap) != 1 || snap[0].State != StateActive {
		t.Fatalf("state after re-acquire = %#v, want active", snap)
	}
}

// TestAcquireMigratesSilentlyBetweenSameRealmInstances is the A-04 autolock
// regression guard (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §4): a second
// instance of the SAME realm takes over an unexpired lock silently (force
// takeover, no ErrHeldByOther), because Acquire is realm-aware.
func TestAcquireMigratesSilentlyBetweenSameRealmInstances(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	path := "/wc/owned.bin"

	m1, err := Open(filepath.Join(t.TempDir(), "passports.json"), "instance-laptop", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := m1.Acquire(context.Background(), []string{path}, "realm-a")
	if err != nil {
		t.Fatal(err)
	}

	m2, err := Open(filepath.Join(t.TempDir(), "passports.json"), "instance-desktop", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := m2.Acquire(context.Background(), []string{path}, "realm-a")
	if err != nil {
		t.Fatalf("same-realm migration rejected: %v", err)
	}
	if second[0].PassportID == first[0].PassportID {
		t.Fatal("migration did not issue a new passport")
	}
	if b.forceCalls != 1 {
		t.Fatalf("force calls = %d, want 1 (silent takeover)", b.forceCalls)
	}
}

// TestAcquireNeverStealsFromForeignRealmEvenUnexpired confirms the migration
// path never applies across realms: a different realm's still-live lock is
// rejected exactly as before this feature, with zero force calls.
func TestAcquireNeverStealsFromForeignRealmEvenUnexpired(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	path := "/wc/owned.bin"

	m1 := openTestManager(t, b, &now, Config{})
	if _, _, err := m1.Acquire(context.Background(), []string{path}, "realm-a"); err != nil {
		t.Fatal(err)
	}

	m2, err := Open(filepath.Join(t.TempDir(), "passports.json"), "instance-b", b, Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m2.Acquire(context.Background(), []string{path}, "realm-b"); !errors.Is(err, ErrHeldByOther) {
		t.Fatalf("foreign realm error=%v, want ErrHeldByOther", err)
	}
	if b.forceCalls != 0 {
		t.Fatalf("force calls = %d, want 0 (never steal from a different realm)", b.forceCalls)
	}
}

// TestAutoUnlockOwnedChmodsFreeAndSameRealmPaths is the A-02/A-03 autolock
// regression guard: a read-only file with no lock, or a lock already
// belonging to the same realm, becomes locally writable without any real
// Lock() call; a foreign or unrecognized hold is left untouched.
func TestAutoUnlockOwnedChmodsFreeAndSameRealmPaths(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{})

	wc := t.TempDir()
	free := filepath.Join(wc, "free.txt")
	ownedByMe := filepath.Join(wc, "owned-by-me.txt")
	foreignHeld := filepath.Join(wc, "foreign.txt")
	unrecognized := filepath.Join(wc, "unrecognized.txt")
	for _, p := range []string{free, ownedByMe, foreignHeld, unrecognized} {
		if err := writeFile(p, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	b.locks[ownedByMe] = &Lock{Token: "t1", Comment: FormatComment(Metadata{PassportID: "p1", InstanceUID: "instance-other", RealmID: "realm-a", IssuedAt: now, ExpiresAt: now.Add(time.Hour), HardExpiresAt: now.Add(2 * time.Hour)})}
	b.locks[foreignHeld] = &Lock{Token: "t2", Comment: FormatComment(Metadata{PassportID: "p2", InstanceUID: "instance-other", RealmID: "realm-b", IssuedAt: now, ExpiresAt: now.Add(time.Hour), HardExpiresAt: now.Add(2 * time.Hour)})}
	b.locks[unrecognized] = &Lock{Token: "t3", Comment: "some other application's lock, not FileES"}

	if err := m.AutoUnlockOwned(context.Background(), wc, "realm-a"); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, free, true)
	assertWritable(t, ownedByMe, true)
	assertWritable(t, foreignHeld, false)
	assertWritable(t, unrecognized, false)
	if b.seq != 0 {
		t.Fatalf("AutoUnlockOwned must never call the real Lock: seq=%d", b.seq)
	}
}

// TestAutoUnlockOwnedNoopWithoutRealmID confirms the function is inert when
// no realm identity is known (matches config.Repo.RealmID's zero value for
// clients that haven't yet loaded a projection).
func TestAutoUnlockOwnedNoopWithoutRealmID(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	b := newFakeBackend()
	m := openTestManager(t, b, &now, Config{})
	wc := t.TempDir()
	path := filepath.Join(wc, "free.txt")
	if err := writeFile(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := m.AutoUnlockOwned(context.Background(), wc, ""); err != nil {
		t.Fatal(err)
	}
	assertWritable(t, path, false)
}

func writeFile(path string, mode os.FileMode) error {
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func assertWritable(t *testing.T, path string, want bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := info.Mode().Perm()&0o200 != 0
	if got != want {
		t.Fatalf("%s writable=%v, want %v (mode=%v)", path, got, want, info.Mode())
	}
}
