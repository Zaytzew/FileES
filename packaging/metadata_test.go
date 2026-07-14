package packaging_test

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"strings"
	"testing"

	"filees/internal/gui/identity"
)

func TestLinuxDesktopMetadata(t *testing.T) {
	data, err := os.ReadFile("linux/filees-gui.desktop")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"[Desktop Entry]", "Type=Application", "Name=" + identity.Name,
		"Exec=/usr/local/bin/filees-gui", "Icon=" + identity.ID, "Terminal=false",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("desktop metadata missing %q", required)
		}
	}
}

func TestWindowsManifestIsWellFormedAndUnelevated(t *testing.T) {
	data, err := os.ReadFile("windows/filees-gui.exe.manifest")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatalf("invalid Windows manifest: %v", err)
	}
	text := string(data)
	for _, required := range []string{`level="asInvoker"`, "PerMonitorV2", "longPathAware"} {
		if !strings.Contains(text, required) {
			t.Errorf("Windows manifest missing %q", required)
		}
	}
}

func TestWindowsPackagingIdentityMatchesApplication(t *testing.T) {
	data, err := os.ReadFile("windows/identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Name  string `json:"name"`
		AUMID string `json:"app_user_model_id"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Name != identity.Name || metadata.AUMID != identity.AUMID {
		t.Fatalf("packaging identity = %#v, application = %q/%q", metadata, identity.Name, identity.AUMID)
	}
}

func TestWindowsBuildUsesGUISubsystem(t *testing.T) {
	data, err := os.ReadFile("build-gui.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `-H=windowsgui`) {
		t.Fatal("Windows GUI build would open a console window")
	}
}
