package main

import (
	"encoding/json"
	"strings"
	"testing"

	guiapp "filees/internal/gui/app"
)

func TestClientVersionIsProjectedAndRenderedInHeader(t *testing.T) {
	if version != "0.1.0" {
		t.Fatalf("initial Wails client version = %q", version)
	}
	snapshot := projectViewModel(guiapp.ViewModel{})
	if snapshot.ClientVersion != version {
		t.Fatalf("projected client version = %q, want %q", snapshot.ClientVersion, version)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"client_version":"0.1.0"`) {
		t.Fatalf("snapshot JSON does not expose client version: %s", payload)
	}

	markup := embeddedFrontendFile(t, "frontend/index.html")
	script := embeddedFrontendFile(t, "frontend/app.js")
	models := embeddedFrontendFile(t, "frontend/bindings/filees/cmd/filees-gui-wails/models.js")
	styles := embeddedFrontendFile(t, "frontend/app.css")
	for name, content := range map[string]string{"markup": markup, "script": script, "models": models} {
		if !strings.Contains(content, "client-version") && !strings.Contains(content, "client_version") {
			t.Fatalf("%s does not carry the client version", name)
		}
	}
	if strings.Contains(markup, "Wersja testowa") {
		t.Fatal("legacy test-version label remains in the header")
	}
	for _, required := range []string{`id="version-overlay"`, `id="version-client"`, `id="version-release"`, `id="version-update-actions"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("version popup markup does not contain %s", required)
		}
	}
	for _, required := range []string{`function openVersionDialog()`, `renderVersionDialog(snapshot)`, `$("#client-version").addEventListener("click", openVersionDialog)`, `data-action="update_plan"`, `data-action="update_apply"`} {
		if !strings.Contains(script, required) && !strings.Contains(markup, required) {
			t.Fatalf("version popup does not contain %s", required)
		}
	}
	if !strings.Contains(styles, ".update-actions[hidden] { display:none; }") {
		t.Fatal("hidden version update actions are overridden by their flex layout")
	}
}
