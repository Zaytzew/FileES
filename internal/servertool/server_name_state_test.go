package servertool

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/pkg/clientview"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
)

func TestStateEmitterReadsCurrentNameWithoutChangingInvitationOrView(t *testing.T) {
	configPath, _, _ := writeRepoPruneFixtureConfig(t)
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	writeTestClientView(t, config.Activation.ServiceWorkingCopy, nil)
	viewPath := filepath.Join(config.Activation.ServiceWorkingCopy, "clients", testClientID, "view.json")
	view, err := clientview.Load(viewPath)
	if err != nil {
		t.Fatal(err)
	}
	view.ServerDisplayName = "spot"
	terminalJSON(t, viewPath, view)
	beforeView, err := os.ReadFile(viewPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	settings["invitation"].(map[string]any)["server_id"] = "spot"
	invitationBefore, _ := json.Marshal(settings["invitation"])
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"40rs:filees", "40rs — Nowa etykieta"} {
		settings["display_name"] = name
		terminalJSON(t, configPath, settings)
		beforeConfig, _ := os.ReadFile(configPath)
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestStateEmitterTerminalWorkerHelper$")
		cmd.Env = append(os.Environ(), "FILEES_TERMINAL_HELPER=1", "FILEES_TERMINAL_CONFIG="+configPath, "FILEES_TERMINAL_CLIENT="+testClientID)
		cmd.Stdin = bytes.NewBufferString(`{"schema":"filees.reservation/v2","repo_id":""}`)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("broker: %v %s", err, stderr.String())
		}
		result, err := reservationv1.ParseResult(output)
		if err != nil || result.ServerDisplayName != name || result.ServerID != "spot" || result.ViewGeneration != view.Generation {
			t.Fatalf("metadata=%s err=%v", output, err)
		}
		afterView, _ := os.ReadFile(viewPath)
		afterConfig, _ := os.ReadFile(configPath)
		if !bytes.Equal(beforeView, afterView) || !bytes.Equal(beforeConfig, afterConfig) {
			t.Fatal("metadata read rewrote authority")
		}
		loaded, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
		if err != nil || loaded.ServerID != "spot" || loaded.ServerDisplayName != name {
			t.Fatalf("identity changed: %v", err)
		}
		var afterSettings map[string]any
		if err := json.Unmarshal(afterConfig, &afterSettings); err != nil {
			t.Fatal(err)
		}
		invitationAfter, _ := json.Marshal(afterSettings["invitation"])
		if !bytes.Equal(invitationBefore, invitationAfter) {
			t.Fatal("display name changed invitation")
		}
	}
}
