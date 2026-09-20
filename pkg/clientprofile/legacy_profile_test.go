//go:build !windows

package clientprofile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func legacyFixture(t *testing.T, root, id, dir string) Profile {
	t.Helper()
	p := Profile{Schema: Schema, ServerID: id, Address: "example.net", ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(dir, "id"), KnownHosts: filepath.Join(dir, "known"), SSHPort: 22, ServiceURL: "svn+ssh://example.net/", ServiceWC: filepath.Join(dir, "wc"), RelativeViewPath: "view.json", CachePath: filepath.Join(dir, "cache"), PollInterval: time.Minute}
	if err := Store(filepath.Join(dir, "client-profile.json"), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLegacyUnixProfileKeepsIdentityAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	id := "atmprojekt:filees"
	dir := filepath.Join(root, id)
	want := legacyFixture(t, root, id, dir)
	profiles, err := List(root)
	if err != nil || len(profiles) != 1 || profiles[0] != want {
		t.Fatalf("legacy profile: %+v %v", profiles, err)
	}
	if got, err := ServerDir(root, id); err != nil || got != dir {
		t.Fatalf("lookup: %s %v", got, err)
	}
	name, _ := StateDirName(id)
	if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
		t.Fatal("legacy profile was moved or copied")
	}
	if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := List(root); err == nil {
		t.Fatal("conflicting encoded directory accepted")
	}
	if err := Remove(root, id); err == nil {
		t.Fatal("ambiguous profile was removed")
	}
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("legacy directory not removed")
	}
}

func TestEncodedAndLiteralEscapeIDsRemainDistinct(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"a:b", "a+3Ab"} {
		dir, err := ServerDir(root, id)
		if err != nil {
			t.Fatal(err)
		}
		legacyFixture(t, root, id, dir)
	}
	profiles, err := List(root)
	if err != nil || len(profiles) != 2 {
		t.Fatalf("distinct IDs collided: %+v %v", profiles, err)
	}
}

func TestLegacyProfileSymlinkAndIdentityMismatchRefused(t *testing.T) {
	root := t.TempDir()
	id := "a:b"
	dir := filepath.Join(root, id)
	legacyFixture(t, root, "different", dir)
	if _, err := ServerDir(root, id); err == nil {
		t.Fatal("wrong identity adopted")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ServerDir(root, id); err == nil {
		t.Fatal("symlink adopted")
	}
}
