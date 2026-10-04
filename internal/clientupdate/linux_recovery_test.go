//go:build !windows

package clientupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/internal/releaseenvelope"
)

// The first installation changes only the daemon. A fresh service then runs
// that new version with the old GUI, exactly as after an interrupted update.
type interruptedPairRunner struct {
	prefix      string
	interrupted bool
}

func (r *interruptedPairRunner) Run(_ context.Context, _ string, root string, _ []string) error {
	for _, name := range []string{"filees", "filees-gui", "filees-svn"} {
		data, err := os.ReadFile(filepath.Join(root, "bin", name))
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(r.prefix, "bin", name), data, 0755); err != nil {
			return err
		}
		if r.interrupted {
			r.interrupted = false
			return errors.New("installation interrupted after daemon promotion")
		}
	}
	return nil
}
func TestLinuxInterruptedPairIsRepairableAfterDaemonRestart(t *testing.T) {
	bundle, resolved := linuxBundle(t)
	resolved.Envelope = &releaseenvelope.Envelope{ReleaseID: "r1", Sequence: 1, SecurityEpoch: 1}
	home := t.TempDir()
	prefix := filepath.Join(home, ".local")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"filees", "filees-gui", "filees-svn"} {
		if err := os.WriteFile(filepath.Join(prefix, "bin", name), []byte("old"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &interruptedPairRunner{prefix: prefix, interrupted: true}
	installer := LinuxInstaller{Stager: BundleStager{Fetcher: artifactFetcher{"releases/r1/desktop/linux-amd64/client.tar.gz": bundle}, Root: t.TempDir()}, Paths: LinuxPaths{Home: home, Prefix: prefix, DataHome: filepath.Join(home, "data"), ConfigHome: filepath.Join(home, "config")}, Runner: runner}
	store := StateStore{Path: filepath.Join(home, "update.json")}
	service := &Service{Resolver: resolverStub{resolved: resolved}, Installer: installer, State: store, CurrentVersion: "1.0"}
	if _, err := service.Apply(t.Context()); err == nil {
		t.Fatal("interruption reported success")
	}
	state, err := store.Load()
	if err != nil || state.HighestSequence != 0 {
		t.Fatalf("checkpoint advanced after interruption: %+v %v", state, err)
	}
	if pending, err := installer.PendingInstallation(resolved); err != nil || !pending {
		t.Fatalf("missing pending installation: %v %v", pending, err)
	}
	newer := &Service{Resolver: service.Resolver, Installer: installer, State: store, CurrentVersion: "2.0"}
	if result, err := newer.Apply(t.Context()); err != nil || result.InstalledVersion != "2.0" {
		t.Fatalf("pending installation downgraded newer build: %+v %v", result, err)
	}
	restarted := &Service{Resolver: service.Resolver, Installer: installer, State: store, CurrentVersion: resolved.Manifest.Version}
	status, err := restarted.Status(t.Context())
	if err != nil || status.State != "available" {
		t.Fatalf("mixed pair reported current: %+v %v", status, err)
	}
	plan, err := planWhenDownloaded(t, restarted)
	if err != nil || len(plan.Changes) == 0 {
		t.Fatalf("repair plan unavailable: %+v %v", plan, err)
	}
	if _, err := restarted.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"filees": "daemon-new", "filees-gui": "gui-new", "filees-svn": "native-svn-new"} {
		got, err := os.ReadFile(filepath.Join(prefix, "bin", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if pending, err := installer.PendingInstallation(resolved); err != nil || pending {
		t.Fatalf("successful installation still pending: %v %v", pending, err)
	}
	state, err = store.Load()
	if err != nil || state.HighestSequence != 1 {
		t.Fatalf("successful repair checkpoint: %+v %v", state, err)
	}
}
