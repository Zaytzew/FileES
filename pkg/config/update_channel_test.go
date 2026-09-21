package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpdateChannelPreservesConfigurationAndStatePaths(t *testing.T) {
	dir := t.TempDir()
	defaults, err := NewUpdateConfig("https://releases.example.test", "alpha", "desktop", "windows-amd64", filepath.Join(dir, "state.json"), filepath.Join(dir, "stage"), "svn")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"server_id":"own-server","transport":{"identity_file":"keep-key","known_hosts":"keep-hosts"},"repositories":[]}`)
	beta, err := WithUpdateChannel(original, "beta", &defaults)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	json.Unmarshal(original, &before)
	json.Unmarshal(beta, &after)
	for key, value := range before {
		var a, b any
		json.Unmarshal(value, &a)
		json.Unmarshal(after[key], &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s changed", key)
		}
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, beta, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := LoadClientView(path)
	if err != nil {
		t.Fatal(err)
	}
	if !view.UpdateConfigured || view.Update.Channel != "beta" || view.Update.StatePath != defaults.StatePath || view.Update.StageRoot != defaults.StageRoot {
		t.Fatalf("view=%+v", view)
	}
	// A later binary's defaults must not move the existing anti-rollback floor,
	// repository or transport when the user subsequently selects another lane.
	defaults.RepoURL = "https://different.example.test"
	defaults.StatePath = filepath.Join(dir, "wrong-state.json")
	alpha, err := WithUpdateChannel(beta, "alpha", &defaults)
	if err != nil {
		t.Fatal(err)
	}
	var betaFields, alphaFields map[string]json.RawMessage
	json.Unmarshal(beta, &betaFields)
	json.Unmarshal(alpha, &alphaFields)
	var oldUpdate, newUpdate map[string]any
	json.Unmarshal(betaFields["update"], &oldUpdate)
	json.Unmarshal(alphaFields["update"], &newUpdate)
	delete(oldUpdate, "channel")
	delete(newUpdate, "channel")
	if !reflect.DeepEqual(oldUpdate, newUpdate) {
		t.Fatal("channel selection changed other update settings")
	}
	unchanged, err := WithUpdateChannel(alpha, "alpha", nil)
	if err != nil || string(unchanged) != string(alpha) {
		t.Fatal("repeat selection rewrote configuration")
	}
}

func TestUpdateChannelRefusesOptOutAndInvalidInput(t *testing.T) {
	for _, raw := range []string{`{"update":{"enabled":false},"repositories":[]}`, `{}`, `null`, `[]`, `{"unknown":"value"}`, `{} {}`, `{"update":{"enabled":true}}`} {
		if _, err := WithUpdateChannel([]byte(raw), "beta", nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, channel := range []string{"", "../beta", "Beta", "nightly"} {
		if _, err := WithUpdateChannel([]byte(`{}`), channel, nil); err == nil {
			t.Fatalf("accepted %q", channel)
		}
	}
}
