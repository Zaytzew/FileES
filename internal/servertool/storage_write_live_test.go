//go:build !windows

package servertool

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/storagewatch"
	"filees/pkg/clientview"
	v1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestStorageWriteRealBroker(t *testing.T) {
	configPath, _, repos := writeRepoPruneFixtureConfig(t)
	c, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	runRepoPruneCommand(t, c.Repositories.SVNAdminBinary, "create", filepath.Join(repos, repoID))
	writeTestClientView(t, c.Activation.ServiceWorkingCopy, []clientview.Repository{reservationTestRepository(repoID, "active")})
	root := filepath.Dir(configPath)
	settings := capacityConfig{Schema: "filees.capacity-alerts/v1", Realm: uuid.NewString(), Email: "private@example.test", StateDir: root, Policy: storagewatch.DefaultPolicy()}
	terminalJSON(t, filepath.Join(root, "capacity-alerts.json"), settings)
	device, err := (storagewatch.Native{}).Device(repos)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storagewatch.Load(filepath.Join(root, "capacity.json"), settings.Realm, settings.Email)
	if err != nil {
		t.Fatal(err)
	}
	p := storagewatch.Policy{WarningPercent: 15, CriticalPercent: 5, HysteresisPercent: 2, WarningBytes: 100, CriticalBytes: 25, HysteresisBytes: 10}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(id string, opt bool) (v1.Result, error) {
		raw, _ := json.Marshal(v1.Request{Schema: v1.StateSchema, RepoID: id, IncludeStorageWrite: opt})
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestStateEmitterTerminalWorkerHelper$")
		cmd.Env = append(os.Environ(), "FILEES_TERMINAL_HELPER=1", "FILEES_TERMINAL_CONFIG="+configPath, "FILEES_TERMINAL_CLIENT="+testClientID)
		cmd.Stdin = bytes.NewReader(raw)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if len(out) != 0 {
				t.Fatal("failed request leaked state", string(out))
			}
			return v1.Result{}, err
		}
		result, err := v1.ParseResult(out)
		if err != nil {
			t.Fatalf("%s %s: %v", out, stderr.String(), err)
		}
		return result, nil
	}
	for _, tc := range []struct {
		free uint64
		age  time.Duration
		want string
	}{{1, 0, "blocked"}, {800, 0, "available"}, {800, 4 * time.Minute, "unknown"}} {
		if err := s.Observe([]storagewatch.Volume{{Device: device, Sample: storagewatch.Sample{Total: 1000, Available: tc.free}}}, p, time.Now().Add(-tc.age)); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(filepath.Join(root, "capacity.json")); err != nil {
			t.Fatal(err)
		}
		result, err := fetch(repoID, true)
		if err != nil || result.StorageWrite == nil || result.StorageWrite.State != tc.want {
			t.Fatalf("want %s got %+v %v", tc.want, result, err)
		}
		old, err := fetch(repoID, false)
		if err != nil || old.StorageWrite != nil {
			t.Fatal("old request changed", old, err)
		}
	}
	if _, err := fetch(uuid.NewString(), true); err == nil {
		t.Fatal("unauthorized repository received state")
	}
	if err := os.Remove(filepath.Join(root, "capacity.json")); err != nil {
		t.Fatal(err)
	}
	if result, err := fetch(repoID, true); err != nil || result.StorageWrite == nil || result.StorageWrite.State != "unknown" {
		t.Fatal(result, err)
	}
}
