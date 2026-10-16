package passport

import (
	"context"
	"errors"
	"filees/pkg/client"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticReleaseUsesTokenReceipt(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "close-grace", true: "expiry"}[expiry], func(t *testing.T) {
			m, b, p := releaseFixture(t)
			pass := m.passports[p]
			if expiry {
				pass.HardExpiresAt = time.Now().Add(-time.Second)
			} else {
				pass.CloseAfter = time.Now().Add(-time.Second)
			}
			m.passports[p] = pass
			b.partitioned = true // automatic release must not perform an Inspect first
			var observed string
			m.cfg.OnReleased = func(path, token string) {
				if path != p {
					t.Fatal(path)
				}
				observed = token
			}
			b.release = func(_, token string) (*client.LockReleaseReceipt, string, error) {
				return &client.LockReleaseReceipt{Token: token, State: "released"}, "", nil
			}
			if err := m.Heartbeat(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(m.Snapshot()) != 0 || observed != pass.FencingToken || b.calls != 1 {
				t.Fatal("automatic release missing")
			}
		})
	}
}

type releaseBackend struct {
	*fakeBackend
	calls   int
	release func(string, string) (*client.LockReleaseReceipt, string, error)
}

func (b *releaseBackend) ReleaseToken(_ context.Context, path, token string) (*client.LockReleaseReceipt, string, error) {
	b.calls++
	return b.release(path, token)
}
func releaseFixture(t *testing.T) (*Manager, *releaseBackend, string) {
	t.Helper()
	b := &releaseBackend{fakeBackend: newFakeBackend()}
	m, err := Open(filepath.Join(t.TempDir(), "passports.json"), "instance", b, Config{})
	if err != nil {
		t.Fatal(err)
	}
	p := wcPath("/wc/doc")
	if _, _, err := m.Acquire(t.Context(), []string{p}, ""); err != nil {
		t.Fatal(err)
	}
	return m, b, p
}
func TestReleaseReceiptSurvivesLostReplyAndRestart(t *testing.T) {
	m, b, p := releaseFixture(t)
	token := m.Snapshot()[0].FencingToken
	b.release = func(path, expected string) (*client.LockReleaseReceipt, string, error) {
		if expected != token {
			t.Fatal("changed token")
		}
		persisted, err := Open(m.storePath, "instance", b, Config{})
		if err != nil || persisted.Snapshot()[0].State != StateReleasing {
			t.Fatal("mutation before durable intent", err)
		}
		delete(b.locks, path)
		return nil, "", errPartitioned
	}
	if _, err := m.Release(t.Context(), []string{p}); err == nil {
		t.Fatal("lost reply accepted")
	}
	if err := CheckSettledStore(m.storePath); err == nil {
		t.Fatal("unsettled release hidden")
	}
	if _, _, err := m.Acquire(t.Context(), []string{p}, ""); err == nil {
		t.Fatal("releasing intent replaced")
	}
	// A different token may appear before retry. Recovery must not release it.
	b.locks[p] = &Lock{Token: "new"}
	b.release = func(path, expected string) (*client.LockReleaseReceipt, string, error) {
		if expected != token {
			t.Fatal("new token substituted")
		}
		return &client.LockReleaseReceipt{Token: expected, State: "not-held"}, "", nil
	}
	restarted, err := Open(m.storePath, "instance", b, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Heartbeat(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(restarted.Snapshot()) != 0 || b.locks[p].Token != "new" || b.unlocks != 0 {
		t.Fatal("unsafe recovery")
	}
}
func TestReleaseReceiptRejectsWrongProofAndCancellation(t *testing.T) {
	for _, mode := range []string{"token", "state", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			m, b, p := releaseFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.release = func(_, token string) (*client.LockReleaseReceipt, string, error) {
				proof := &client.LockReleaseReceipt{Token: token, State: "released"}
				switch mode {
				case "token":
					proof.Token = "new"
				case "state":
					proof.State = "pending"
				case "cancel":
					cancel()
				}
				return proof, "", nil
			}
			if _, err := m.Release(ctx, []string{p}); err == nil {
				t.Fatal("invalid ACK accepted")
			}
			if got := m.Snapshot(); len(got) != 1 || got[0].State != StateReleasing {
				t.Fatal(got)
			}
		})
	}
}
func TestReleaseReceiptSaveFailureNeverMutates(t *testing.T) {
	m, b, p := releaseFixture(t)
	b.release = func(string, string) (*client.LockReleaseReceipt, string, error) {
		t.Fatal("mutation before save")
		return nil, "", nil
	}
	// Replace only the test store path with a directory, forcing atomic rename failure.
	if err := os.Remove(m.storePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.storePath, 0700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := m.Release(t.Context(), []string{p}); err == nil {
			t.Fatal("save failure ignored")
		}
	}
	if b.calls != 0 {
		t.Fatal(b.calls)
	}
}
func TestReleaseReceiptNoInspectionAndLegacyFallback(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "legacy"}[legacy], func(t *testing.T) {
			m, b, _ := releaseFixture(t)
			b.release = func(_, token string) (*client.LockReleaseReceipt, string, error) {
				if legacy {
					return nil, "", nil
				}
				return &client.LockReleaseReceipt{Token: token, State: "released"}, "", nil
			}
			b.partitioned = !legacy // proves receipt path does not Inspect
			if err := m.ReleaseAll(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(m.Snapshot()) != 0 {
				t.Fatal("release not retired")
			}
			if legacy && b.unlocks != 1 {
				t.Fatal("legacy mutation missing")
			}
		})
	}
}
func TestReleaseReceiptFinalSaveFailureRetainsIntent(t *testing.T) {
	m, b, p := releaseFixture(t)
	b.release = func(_, token string) (*client.LockReleaseReceipt, string, error) {
		if err := os.Remove(m.storePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(m.storePath, 0700); err != nil {
			t.Fatal(err)
		}
		return &client.LockReleaseReceipt{Token: token, State: "released"}, "", nil
	}
	_, err := m.Release(t.Context(), []string{p})
	if err == nil || errors.Is(err, context.Canceled) || m.Snapshot()[0].State != StateReleasing {
		t.Fatal(err)
	}
}

func TestLegacyReleaseDoesNotClaimCleanupFromMissingOrReplacedLock(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		m, b, p := releaseFixture(t)
		b.release = func(string, string) (*client.LockReleaseReceipt, string, error) { return nil, "", nil }
		delete(b.locks, p)
		if replaced {
			b.locks[p] = &Lock{Token: "new"}
		}
		if _, err := m.Release(t.Context(), []string{p}); !errors.Is(err, ErrPassportLost) {
			t.Fatal(err)
		}
		if got := m.Snapshot(); len(got) != 1 || got[0].State != StateLost || b.unlocks != 0 {
			t.Fatal(got)
		}
	}
}
