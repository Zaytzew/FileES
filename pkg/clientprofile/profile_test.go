package clientprofile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLoadProfileRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "server", "client-profile.json")
	want := Profile{Schema: Schema, ServerID: "office", DisplayName: "filees.example.net", Address: "filees.example.net", ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(root, "id"), KnownHosts: filepath.Join(root, "known"), SSHPort: 22, ServiceURL: "svn+ssh://_filees-client@filees.example.net/", ServiceWC: filepath.Join(root, "wc"), RelativeViewPath: "clients/00000000-0000-0000-0000-000000000001/view.json", CachePath: filepath.Join(root, "cache", "view.json"), PollInterval: time.Minute}
	if err := Store(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode())
	}
}

func TestLoadRejectsUnknownOrTrailingProfileData(t *testing.T) {
	for _, raw := range []string{`{"unknown":true}`, `{} {}`} {
		path := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
