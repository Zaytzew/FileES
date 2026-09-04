package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	rootversion "filees"
	guiapp "filees/internal/gui/app"
)

func TestClientVersionIsProjectedAndRenderedInHeader(t *testing.T) {
	if got := clientVersion(); got != rootversion.Version() {
		t.Fatalf("client version = %q, embedded = %q", got, rootversion.Version())
	}
	snapshot := projectViewModel(guiapp.ViewModel{})
	if snapshot.ClientVersion != clientVersion() {
		t.Fatalf("projected client version = %q, want %q", snapshot.ClientVersion, clientVersion())
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wantVersionJSON := fmt.Sprintf(`"client_version":%q`, rootversion.Version())
	if !strings.Contains(string(payload), wantVersionJSON) {
		t.Fatalf("snapshot JSON does not expose client version: %s", payload)
	}
	updateSnapshot := projectViewModel(guiapp.ViewModel{Update: &guiapp.UpdateViewModel{State: "current", Channel: "alpha", CurrentVersion: "r688"}})
	if updateSnapshot.Update == nil || updateSnapshot.Update.Channel != "alpha" {
		t.Fatalf("projected update channel = %+v", updateSnapshot.Update)
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
	for _, required := range []string{`id="version-overlay"`, `id="version-client"`, `id="version-channel"`, `id="version-release"`, `id="version-update-actions"`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("version popup markup does not contain %s", required)
		}
	}
	for _, required := range []string{`assets/atm-projekt.png`, `Odpowiedzialny dostawca`, `FileES jest marką handlową`, `kontakt@atmprojekt.pl`, `BSD 2-Clause`, `Copyright © 2026 ATM Projekt`, `Pełna treść licencji`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("version popup does not identify its provider and licence via %q", required)
		}
	}
	for _, forbidden := range []string{`Zaytzew`, `Acme Król`} {
		if strings.Contains(markup, forbidden) {
			t.Fatalf("public product description exposes personal attribution %q", forbidden)
		}
	}
	for _, required := range []string{`.product-provider`, `.provider-logo`, `.product-legal`, `.license-copy`, `max-height:calc(100vh - 76px)`} {
		if !strings.Contains(styles, required) {
			t.Fatalf("version popup legal/provider styling does not contain %q", required)
		}
	}
	for _, required := range []string{`update?.channel`, `$("#version-channel").textContent`, `channel || "nieustalony"`} {
		if !strings.Contains(script, required) {
			t.Fatalf("version popup does not render update channel via %s", required)
		}
	}
	if !strings.Contains(models, `this["channel"]`) {
		t.Fatal("generated UpdateProjection model does not carry channel")
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
