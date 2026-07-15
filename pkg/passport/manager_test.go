package passport

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type fakeBackend struct {
	locks                    map[string]*Lock
	seq, forceCalls, unlocks int
}

func newFakeBackend() *fakeBackend { return &fakeBackend{locks: map[string]*Lock{}} }
func (b *fakeBackend) Inspect(_ context.Context, path string) (*Lock, error) {
	if l := b.locks[path]; l != nil {
		copy := *l
		return &copy, nil
	}
	return nil, nil
}
func (b *fakeBackend) Lock(_ context.Context, path, comment string, force bool) (*Lock, string, error) {
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
	if b.locks[path] == nil {
		return "", errors.New("not locked")
	}
	delete(b.locks, path)
	b.unlocks++
	return "unlocked", nil
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
	got, _, err := m.Acquire(context.Background(), []string{"/wc/a.bin"})
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
	p, _, _ := m.Acquire(context.Background(), []string{"/wc/a.bin"})
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
	_, _, _ = m.Acquire(context.Background(), []string{path})
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
	first, _, _ := m.Acquire(context.Background(), []string{path})
	now = now.Add(16 * time.Minute)
	second, _, err := m.Acquire(context.Background(), []string{path})
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
	first, _, err := m.Acquire(context.Background(), []string{owned})
	if err != nil {
		t.Fatal(err)
	}
	b.locks[foreign] = &Lock{Token: "foreign", Comment: "ordinary foreign lock"}
	if _, _, err := m.Acquire(context.Background(), []string{owned, foreign}); !errors.Is(err, ErrHeldByOther) {
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
	if _, _, err := m.Acquire(context.Background(), []string{path}); !errors.Is(err, ErrHeldByOther) {
		t.Fatalf("live lock error=%v", err)
	}
	meta, _ := ParseComment(b.locks[path].Comment)
	meta.ExpiresAt = now.Add(-time.Second)
	b.locks[path].Comment = FormatComment(meta)
	if _, _, err := m.Acquire(context.Background(), []string{path}); err != nil {
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
	_, _, _ = m.Acquire(context.Background(), []string{path})
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
	_, _, _ = m.Acquire(context.Background(), []string{"/wc/a", "/wc/b"})
	b.locks["/wc/b"] = &Lock{Token: "stolen", Comment: "foreign"}
	if err := m.ReleaseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].Path != "/wc/b" || snap[0].State != StateLost || b.unlocks != 1 {
		t.Fatalf("snapshot=%#v unlocks=%d", snap, b.unlocks)
	}
}
