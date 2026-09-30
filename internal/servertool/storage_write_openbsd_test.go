//go:build openbsd

package servertool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/storagewatch"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestStorageWriteReadUnderLockedUnveil(t *testing.T) {
	if isolateSandboxingTest(t, "TestStorageWriteReadUnderLockedUnveil") {
		return
	}
	root := sandboxedTestRoot(t)
	configPath := filepath.Join(root, "config", "server.json")
	stateDir := filepath.Join(root, "observations")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	settings := capacityConfig{Schema: "filees.capacity-alerts/v1", Realm: uuid.NewString(), Email: "admin@example.test", StateDir: stateDir, Policy: storagewatch.DefaultPolicy()}
	terminalJSON(t, filepath.Join(filepath.Dir(configPath), "capacity-alerts.json"), settings)
	c := serverconfig.Config{}
	c.Repositories.Root = filepath.Join(root, "repos")
	c.Repositories.ResultsRoot = filepath.Join(root, "results")
	c.Activation.Root = filepath.Join(root, "activation")
	c.Activation.ServiceRepository = filepath.Join(root, "service-repo")
	c.Activation.ServiceWorkingCopy = filepath.Join(root, "service-wc")
	paths := storageWriteReadPaths(configPath)
	for _, path := range []string{c.Repositories.Root, c.Repositories.ResultsRoot, c.Activation.Root, c.Activation.ServiceRepository, c.Activation.ServiceWorkingCopy} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, obsandbox.Path{Label: "existing-core", Name: path, Perms: "r"})
	}
	if err := os.Mkdir(filepath.Join(c.Repositories.Root, repoID), 0700); err != nil {
		t.Fatal(err)
	}
	device, err := (storagewatch.Native{}).Device(c.Repositories.Root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storagewatch.Load(filepath.Join(stateDir, "capacity.json"), settings.Realm, settings.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Observe([]storagewatch.Volume{{Device: device, Sample: storagewatch.Sample{Total: 1 << 40, Available: 1 << 39}}}, settings.Policy, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(filepath.Join(stateDir, "capacity.json")); err != nil {
		t.Fatal(err)
	}
	paths = append(paths, storageWriteReadPaths(configPath)...)
	if err := obsandbox.Apply(obsandbox.Profile{Name: "storage-write-read", Promises: "stdio rpath", Paths: paths}); err != nil {
		t.Fatal(err)
	}
	got := projectStorageWrite(configPath, c, repoID, time.Now(), storagewatch.Native{}.Device)
	if got.State != "available" {
		t.Fatalf("locked unveil: %+v", got)
	}
}
