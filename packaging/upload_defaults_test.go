package packaging_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This tests the shipped defaults, not a running server or custom overrides.
func TestUploadDefaultsUsePersistentSharedIntakeAndStreamingAV(t *testing.T) {
	var server struct {
		Upload struct {
			IntakeRoot string   `json:"intake_root"`
			AVCommand  []string `json:"av_command"`
		} `json:"upload"`
	}
	var links struct {
		IntakeRoot string `json:"intake_root"`
	}
	for name, target := range map[string]any{
		"server/server.example.json":       &server,
		"server/public-links.example.json": &links,
	} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	const root = "/var/filees-downloads/intake"
	if server.Upload.IntakeRoot != root || links.IntakeRoot != root {
		t.Fatalf("intake defaults must agree on persistent path: server=%q links=%q", server.Upload.IntakeRoot, links.IntakeRoot)
	}
	found := false
	for _, arg := range server.Upload.AVCommand {
		if arg == "--stream" {
			found = true
		}
	}
	if !found {
		t.Fatal("clamd must receive a stream rather than traverse the shared intake directory")
	}
	for _, name := range []string{"server/install-server.sh", "server/openbsd/install-ssh.sh"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, `public_upload_intake_root=${PUBLIC_UPLOAD_INTAKE_ROOT:-$public_downloads_dir/intake}`) ||
			!strings.Contains(text, `"$public_links_cache_root" "$public_upload_intake_root"; do`) {
			t.Errorf("%s must include intake in the persistent-path guard", name)
		}
		if strings.Contains(text, "/var/tmp/filees-upload-intake") {
			t.Errorf("%s recreates unsafe legacy intake", name)
		}
		if strings.Contains(name, "openbsd/") && !strings.Contains(text, `install -d -o _filees-links -g "$public_access_group" -m 770 "$public_upload_intake_root"`) {
			t.Error("OpenBSD intake needs shared state/links ownership")
		}
	}
}
