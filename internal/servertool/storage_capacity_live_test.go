//go:build !windows

package servertool

import (
	"bytes"
	"encoding/json"
	"filees/pkg/clientview"
	v1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStorageCapacityRealAuthorizedBroker(t *testing.T) {
	path, _, _ := writeRepoPruneFixtureConfig(t)
	c, err := serverconfig.LoadFor(path, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	writeTestClientView(t, c.Activation.ServiceWorkingCopy, []clientview.Repository{})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, opt := range []bool{false, true} {
		raw, _ := json.Marshal(v1.Request{Schema: v1.StateSchema, IncludeStorageCapacity: opt})
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestStateEmitterTerminalWorkerHelper$")
		cmd.Env = append(os.Environ(), "FILEES_TERMINAL_HELPER=1", "FILEES_TERMINAL_CONFIG="+path, "FILEES_TERMINAL_CLIENT="+testClientID)
		cmd.Stdin = bytes.NewReader(raw)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err, stderr.String())
		}
		result, err := v1.ParseResult(out)
		if err != nil {
			t.Fatal(err, string(out))
		}
		if opt {
			if result.StorageCapacity == nil || result.StorageCapacity.State != "observed" || result.StorageCapacity.TotalBytes == 0 {
				t.Fatal(result)
			}
		} else if result.StorageCapacity != nil {
			t.Fatal("legacy request gained counters")
		}
		if strings.Contains(string(out), c.Repositories.Root) {
			t.Fatal("physical path leaked")
		}
	}
	// Unknown client receives no payload, including no storage information.
	raw, _ := json.Marshal(v1.Request{Schema: v1.StateSchema, IncludeStorageCapacity: true})
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestStateEmitterTerminalWorkerHelper$")
	cmd.Env = append(os.Environ(), "FILEES_TERMINAL_HELPER=1", "FILEES_TERMINAL_CONFIG="+path, "FILEES_TERMINAL_CLIENT=unknown-client")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err == nil || len(out) != 0 {
		t.Fatal("unauthorized capacity payload", err, string(out))
	}
}
