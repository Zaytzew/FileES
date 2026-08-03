package clientprofile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/privatefile"
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
	// The profile names the identity-key file and pins known_hosts, so it is
	// worth protecting in its own right. Assert the guarantee rather than the
	// unix mode, which restricts nobody on Windows.
	if err := privatefile.Verify(path); err != nil {
		t.Fatalf("stored client profile is not private: %v", err)
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

func TestListDiscoversSortedCompleteProfiles(t *testing.T) {
	root := t.TempDir()
	base := Profile{Schema: Schema, DisplayName: "Server", Address: "example.net", ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(root, "id"), KnownHosts: filepath.Join(root, "known"), SSHPort: 22, ServiceURL: "svn+ssh://_filees-client@example.net/", ServiceWC: filepath.Join(root, "wc"), RelativeViewPath: "clients/00000000-0000-0000-0000-000000000001/view.json", CachePath: filepath.Join(root, "cache"), PollInterval: time.Minute}
	for _, id := range []string{"zeta", "alpha"} {
		profile := base
		profile.ServerID = id
		if err := Store(filepath.Join(root, id, "client-profile.json"), profile); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "unfinished"), 0o700); err != nil {
		t.Fatal(err)
	}
	profiles, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ServerID != "alpha" || profiles[1].ServerID != "zeta" {
		t.Fatalf("profiles=%+v", profiles)
	}
}

func TestRemoveDeletesOnlySelectedProfileDirectory(t *testing.T) {
	root := t.TempDir()
	for _, serverID := range []string{"office", "home"} {
		if err := os.MkdirAll(filepath.Join(root, serverID, "identity"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, serverID, "identity", "id_ed25519"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Remove(root, "office"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "office")); !os.IsNotExist(err) {
		t.Fatalf("removed profile stat error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "home", "identity", "id_ed25519")); err != nil {
		t.Fatalf("other profile was affected: %v", err)
	}
	if err := Remove(root, "../escape"); err == nil {
		t.Fatal("accepted escaping profile ID")
	}
}
