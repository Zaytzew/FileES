package passport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
)

// newSVNRepo creates a local svnadmin repository, checks it out, commits three
// versioned files and returns the working-copy path. It skips the calling test
// if svnadmin or svn are not available on this machine.
func newSVNRepo(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"svnadmin", "svn"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH — skipping SVN integration test", bin)
		}
	}

	base := t.TempDir()
	repoPath := filepath.Join(base, "repo")
	wcPath := filepath.Join(base, "wc")

	run := func(name string, args ...string) {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	run("svnadmin", "create", repoPath)
	repoURL := "file://" + repoPath
	run("svn", "checkout", "--non-interactive", "--no-auth-cache", repoURL, wcPath)

	for _, name := range []string{"doc_a.txt", "doc_b.txt", "doc_c.txt"} {
		if err := os.WriteFile(filepath.Join(wcPath, name), []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("svn", "add", "--non-interactive", "--no-auth-cache", filepath.Join(wcPath, name))
	}
	run("svn", "commit", "--non-interactive", "--no-auth-cache", "-m", "init", wcPath)
	return wcPath
}

func newSVNManager(t *testing.T, wcPath string, now *time.Time, cfg Config) *Manager {
	t.Helper()
	cfg.Now = func() time.Time { return *now }
	b := SVNBackend{
		Client: client.New(client.Options{Timeout: 30 * time.Second}),
		WC:     wcPath,
	}
	m, err := Open(filepath.Join(t.TempDir(), "passports.json"), "instance-svn", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSVNBackendAcquireReleaseRoundTrip verifies that Manager can acquire SVN
// locks on multiple versioned files, that Inspect returns the correct opaque
// token, and that Release removes the locks from the repository.
func TestSVNBackendAcquireReleaseRoundTrip(t *testing.T) {
	wc := newSVNRepo(t)
	now := time.Now()
	m := newSVNManager(t, wc, &now, Config{})
	ctx := context.Background()

	pathA := filepath.Join(wc, "doc_a.txt")
	pathB := filepath.Join(wc, "doc_b.txt")

	passports, _, err := m.Acquire(ctx, []string{pathA, pathB})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if len(passports) != 2 {
		t.Fatalf("Acquire returned %d passports, want 2", len(passports))
	}
	for _, p := range passports {
		if p.FencingToken == "" || p.PassportID == "" {
			t.Fatalf("incomplete passport: %#v", p)
		}
	}

	// Verify SVN actually holds the locks.
	cli := client.New(client.Options{Timeout: 10 * time.Second})
	for _, abs := range []string{pathA, pathB} {
		info, err := cli.LockInfo(ctx, wc, abs, "", "")
		if err != nil || info == nil {
			t.Fatalf("SVN lock not present for %s: info=%v err=%v", abs, info, err)
		}
	}

	if _, err := m.Release(ctx, []string{pathA, pathB}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Verify SVN locks are gone.
	for _, abs := range []string{pathA, pathB} {
		info, err := cli.LockInfo(ctx, wc, abs, "", "")
		if err != nil || info != nil {
			t.Fatalf("SVN lock still present after Release for %s: info=%v err=%v", abs, info, err)
		}
	}
	if snap := m.Snapshot(); len(snap) != 0 {
		t.Fatalf("passports not empty after release: %v", snap)
	}
}

// TestSVNBackendStateLostWhenLockStolenExternally verifies that when another
// process force-locks a file that Manager holds, the next Heartbeat marks the
// passport StateLost and the token is no longer trusted for publication.
func TestSVNBackendStateLostWhenLockStolenExternally(t *testing.T) {
	wc := newSVNRepo(t)
	now := time.Now()
	m := newSVNManager(t, wc, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute})
	ctx := context.Background()
	pathA := filepath.Join(wc, "doc_a.txt")

	if _, _, err := m.Acquire(ctx, []string{pathA}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ownedToken := m.Snapshot()[0].FencingToken

	// External process steals the lock (force). Use the SVN CLI directly.
	cli := client.New(client.Options{Timeout: 10 * time.Second})
	if _, err := cli.LockWithComment(ctx, wc, []string{pathA}, "foreign lock", true, "", ""); err != nil {
		t.Fatalf("external force-lock: %v", err)
	}

	// Heartbeat must detect the mismatch (stolen token) and mark StateLost.
	if err := m.Heartbeat(ctx); !errors.Is(err, ErrPassportLost) {
		t.Fatalf("Heartbeat = %v, want ErrPassportLost", err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].State != StateLost {
		t.Fatalf("passport state = %#v, want StateLost", snap)
	}
	if snap[0].FencingToken != ownedToken {
		t.Fatalf("StateLost passport must retain original token for audit; got %q", snap[0].FencingToken)
	}
	// Release must fail — we no longer own the lock.
	if _, err := m.Release(ctx, []string{pathA}); !errors.Is(err, ErrNoPassport) {
		t.Fatalf("Release after StateLost = %v, want ErrNoPassport", err)
	}
}

// TestSVNBackendHeartbeatRenewsTokenBeforeExpiry verifies that when the
// passport approaches its TTL boundary, Heartbeat issues a force-lock on the
// SVN server and rotates the local fencing token.
func TestSVNBackendHeartbeatRenewsTokenBeforeExpiry(t *testing.T) {
	wc := newSVNRepo(t)
	now := time.Now()
	m := newSVNManager(t, wc, &now, Config{TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute})
	ctx := context.Background()
	pathA := filepath.Join(wc, "doc_a.txt")

	if _, _, err := m.Acquire(ctx, []string{pathA}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	token0 := m.Snapshot()[0].FencingToken

	// Advance time to just before expiry — within one HeartbeatInterval of TTL.
	now = now.Add(11 * time.Minute)
	if err := m.Heartbeat(ctx); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	snap := m.Snapshot()
	if snap[0].FencingToken == token0 {
		t.Fatal("Heartbeat did not rotate token before expiry")
	}
	if snap[0].State != StateActive {
		t.Fatalf("state after renewal = %q, want active", snap[0].State)
	}

	// Verify the new token is the one SVN actually holds.
	cli := client.New(client.Options{Timeout: 10 * time.Second})
	info, err := cli.LockInfo(ctx, wc, pathA, "", "")
	if err != nil || info == nil {
		t.Fatalf("SVN lock missing after renewal: %v", err)
	}
	if info.Token != snap[0].FencingToken {
		t.Fatalf("SVN token %q != Manager token %q", info.Token, snap[0].FencingToken)
	}
}

// TestSVNRestartAfterSIGKILLMidRelease is the real-SVN counterpart of the
// unit chaos test: it simulates a process death after the SVN Unlock call
// succeeded but before Manager persisted the change. On restart, Heartbeat
// must detect that the lock is gone and mark the passport StateLost; a
// subsequent Acquire must then succeed with a fresh token.
func TestSVNRestartAfterSIGKILLMidRelease(t *testing.T) {
	wc := newSVNRepo(t)
	now := time.Now()
	store := filepath.Join(t.TempDir(), "passports.json")
	b := SVNBackend{
		Client: client.New(client.Options{Timeout: 30 * time.Second}),
		WC:     wc,
	}
	cfg := Config{Now: func() time.Time { return now }, TTL: 15 * time.Minute, HeartbeatInterval: 5 * time.Minute}
	m, err := Open(store, "instance-svn", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pathA := filepath.Join(wc, "doc_a.txt")
	pathB := filepath.Join(wc, "doc_b.txt")

	if _, _, err := m.Acquire(ctx, []string{pathA, pathB}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Simulate SIGKILL: directly unlock A on the SVN server without updating the store.
	cli := client.New(client.Options{Timeout: 10 * time.Second})
	if _, err := cli.Unlock(ctx, wc, []string{pathA}, "", ""); err != nil {
		t.Fatalf("direct SVN unlock (SIGKILL simulation): %v", err)
	}
	// passports.json still records both A and B.

	// Restart: reopen from the same store.
	m2, err := Open(store, "instance-svn", b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Snapshot()) != 2 {
		t.Fatalf("expected 2 passports after restart, got %v", m2.Snapshot())
	}

	// Heartbeat detects A is gone on the server → StateLost.
	if err := m2.Heartbeat(ctx); !errors.Is(err, ErrPassportLost) {
		t.Fatalf("Heartbeat = %v, want ErrPassportLost", err)
	}
	for _, p := range m2.Snapshot() {
		if p.Path == pathA && p.State != StateLost {
			t.Fatalf("%s state = %q, want StateLost", pathA, p.State)
		}
		if p.Path == pathB && p.State != StateActive {
			t.Fatalf("%s state = %q, want StateActive", pathB, p.State)
		}
	}

	// Re-acquire A must succeed — server has no lock on it.
	if _, _, err := m2.Acquire(ctx, []string{pathA}); err != nil {
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

	// Clean up — release everything.
	if _, err := m2.Release(ctx, []string{pathA, pathB}); err != nil {
		t.Fatalf("final release: %v", err)
	}
}
