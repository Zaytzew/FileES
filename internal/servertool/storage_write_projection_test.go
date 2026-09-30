package servertool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/storagewatch"
	v1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestStorageWriteProjectionScopeFreshnessAndPrivacy(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "server.json")
	settings := capacityConfig{Schema: "filees.capacity-alerts/v1", Realm: uuid.NewString(), Email: "private@example.test", StateDir: root, Policy: storagewatch.DefaultPolicy()}
	raw, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(root, "capacity-alerts.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := storagewatch.Load(filepath.Join(root, "capacity.json"), settings.Realm, settings.Email)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := storagewatch.Policy{WarningPercent: 15, CriticalPercent: 5, HysteresisPercent: 2, WarningBytes: 100, CriticalBytes: 25, HysteresisBytes: 10}
	if err := s.Observe([]storagewatch.Volume{{Device: 1, Sample: storagewatch.Sample{Total: 1000, Available: 800}}, {Device: 2, Sample: storagewatch.Sample{Total: 1000, Available: 1}}}, p, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(filepath.Join(root, "capacity.json")); err != nil {
		t.Fatal(err)
	}
	c := serverconfig.Config{}
	c.Repositories.Root = root
	c.Repositories.ResultsRoot = root
	c.Activation.Root = root
	c.Activation.ServiceRepository = root
	c.Activation.ServiceWorkingCopy = root
	device := func(path string) (uint64, error) {
		if filepath.Base(path) == "low" {
			return 2, nil
		}
		return 1, nil
	}
	for _, tc := range []struct {
		repo string
		at   time.Time
		want string
	}{{"ok", now, "available"}, {"low", now, "blocked"}, {"low", now.Add(3 * time.Minute), "unknown"}, {"ok", now.Add(-time.Second), "unknown"}} {
		got := projectStorageWrite(configPath, c, tc.repo, tc.at, device)
		if got.State != tc.want {
			t.Fatalf("%+v: %+v", tc, got)
		}
		raw, _ := json.Marshal(got)
		for _, private := range []string{root, settings.Email, "device", "available_bytes"} {
			if strings.Contains(string(raw), private) {
				t.Fatal("private data on wire", string(raw))
			}
		}
	}
	for _, probe := range []func(string) (uint64, error){func(string) (uint64, error) { return 3, nil }, func(string) (uint64, error) { return 0, errors.New("denied") }} {
		if got := projectStorageWrite(configPath, c, "ok", now, probe); got.State != "unknown" {
			t.Fatal(got)
		}
	}
	for _, state := range []string{"active", "deleted"} {
		result := v1.Result{RepositoryState: state}
		attachStorageWrite(configPath, c, v1.Request{}, &result)
		if result.StorageWrite != nil {
			t.Fatal("unsolicited storage signal")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "capacity.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := projectStorageWrite(configPath, c, "ok", now, device); got.State != "unknown" {
		t.Fatal(got)
	}
}
