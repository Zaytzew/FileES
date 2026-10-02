//go:build linux || openbsd

package servertool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/storagewatch"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestCapacityCutoverRealRepositoryPersistentHoldAndRecovery(t *testing.T) {
	path, _, repos := writeRepoPruneFixtureConfig(t)
	c, err := serverconfig.LoadFor(path, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(repos, uuid.NewString())
	runRepoPruneCommand(t, c.Repositories.SVNAdminBinary, "create", repo)
	conf := filepath.Join(repo, "conf", "svnserve.conf")
	const original = "[general]\nanon-access = none\nauth-access = write\n"
	if err := os.WriteFile(conf, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "capacity.json")
	s, err := storagewatch.Load(statePath, uuid.NewString(), "operator@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := storagewatch.Policy{WarningPercent: 15, CriticalPercent: 5, HysteresisPercent: 2, WarningBytes: 100, CriticalBytes: 25, HysteresisBytes: 10}
	observe := func(device uint64, available uint64) {
		t.Helper()
		if err := s.Observe([]storagewatch.Volume{{Device: device, Sample: storagewatch.Sample{Total: 1000, Available: available}}}, p, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	apply := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := applyCapacityCutover(ctx, c, s, statePath); err != nil {
			t.Fatal(err)
		}
	}
	observe(1, 1)
	apply()
	gate := storagewatch.Gate{Root: c.Activation.Root}
	if phase, err := gate.Phase(); err != nil || phase != "held" {
		t.Fatal(phase, err)
	}
	raw, _ := os.ReadFile(conf)
	if !strings.Contains(string(raw), "auth-access = read") {
		t.Fatal(string(raw))
	}
	var diag bytes.Buffer
	called := false
	if code := runCapacityWriter(c, &diag, func() int { called = true; return ExitOK }); called || code != ExitTempFail {
		t.Fatal(code, called, diag.String())
	}
	// Restart the monitor from disk. A new/missing device does not clear HOLD.
	s, err = storagewatch.Load(statePath, s.Realm, s.Recipient)
	if err != nil {
		t.Fatal(err)
	}
	observe(2, 900)
	apply()
	if held, err := gate.Held(); !held || err != nil {
		t.Fatal(held, err)
	}
	observe(1, 69) // below the established 5% + 2% recovery boundary
	apply()
	if held, _ := gate.Held(); !held {
		t.Fatal("resumed below recovery boundary")
	}
	observe(1, 900)
	apply()
	if held, err := gate.Held(); held || err != nil {
		t.Fatal(held, err)
	}
	raw, _ = os.ReadFile(conf)
	if string(raw) != original {
		t.Fatal(string(raw))
	}
	if code := runCapacityWriter(c, &diag, func() int { called = true; return ExitOK }); !called || code != ExitOK {
		t.Fatal(code, called)
	}
}

func TestCapacityCutoverOperatorEditKeepsGlobalStop(t *testing.T) {
	path, _, repos := writeRepoPruneFixtureConfig(t)
	c, err := serverconfig.LoadFor(path, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(repos, uuid.NewString())
	runRepoPruneCommand(t, c.Repositories.SVNAdminBinary, "create", repo)
	conf := filepath.Join(repo, "conf", "svnserve.conf")
	if err := os.WriteFile(conf, []byte("[general]\nauth-access = write\n"), 0640); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "capacity.json")
	s, _ := storagewatch.Load(statePath, uuid.NewString(), "operator@example.test")
	p := storagewatch.DefaultPolicy()
	if err := s.Observe([]storagewatch.Volume{{Device: 1, Sample: storagewatch.Sample{Total: 1 << 40, Available: 1}}}, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := applyCapacityCutover(context.Background(), c, s, statePath); err != nil {
		t.Fatal(err)
	}
	const edit = "[general]\nauth-access = none\n"
	if err := os.WriteFile(conf, []byte(edit), 0640); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe([]storagewatch.Volume{{Device: 1, Sample: storagewatch.Sample{Total: 1 << 40, Available: 1 << 39}}}, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := applyCapacityCutover(context.Background(), c, s, statePath); err == nil {
		t.Fatal("operator conflict ignored")
	}
	if held, err := (storagewatch.Gate{Root: c.Activation.Root}).Held(); !held || err != nil {
		t.Fatal(held, err)
	}
	raw, _ := os.ReadFile(conf)
	if string(raw) != edit {
		t.Fatal("operator edit overwritten")
	}
}

func TestCapacityCutoverWCLockTimeoutKeepsPendingStop(t *testing.T) {
	path, _, _ := writeRepoPruneFixtureConfig(t)
	c, err := serverconfig.LoadFor(path, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "capacity.json")
	s, _ := storagewatch.Load(statePath, uuid.NewString(), "operator@example.test")
	if err := s.Observe([]storagewatch.Volume{{Device: 1, Sample: storagewatch.Sample{Total: 1 << 40, Available: 1}}}, storagewatch.DefaultPolicy(), time.Now()); err != nil {
		t.Fatal(err)
	}
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- repoworker.WithFileLock(filepath.Join(c.Activation.Root, ".service-wc.lock"), func() error { close(locked); <-release; return nil })
	}()
	<-locked
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := applyCapacityCutover(ctx, c, s, statePath); err == nil {
		t.Fatal("acknowledged before WC lock admission")
	}
	if phase, err := (storagewatch.Gate{Root: c.Activation.Root}).Phase(); err != nil || phase != "draining" {
		t.Fatal(phase, err)
	}
}
