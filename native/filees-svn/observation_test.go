//go:build native_svn_probe

package nativesvnprobe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInfoBeforeAdoptionAndRemoteHead(t *testing.T) {
	f := newFixture(t, "old.txt")
	if err := os.Remove(filepath.Join(f.wc, ".filees-native-probe")); err != nil {
		t.Fatal(err)
	}
	before := f.status(t)
	local := f.jsonCall(t, true, "info", "--inspect-wc", f.wc)
	f.jsonCall(t, true, "status", "--inspect-wc", f.wc)
	f.jsonCall(t, false, "status", "--inspect-wc", f.wc, "--show-updates")
	f.jsonCall(t, false, "status", "--inspect-wc", f.wc, "--wc", f.wc)
	remote := f.jsonCall(t, true, "info", "--url", f.repoURL)
	for _, result := range []map[string]any{local, remote} {
		row := result["entries"].([]any)[0].(map[string]any)
		if row["revision"] != float64(1) || row["repos_uuid"] == "" || row["url"] != f.repoURL {
			t.Fatalf("info: %#v", result)
		}
	}
	if _, err := os.Stat(filepath.Join(f.wc, ".filees")); !os.IsNotExist(err) {
		t.Fatal("inspection stamped WC", err)
	}
	if string(before) != string(f.status(t)) {
		t.Fatal("inspection mutated WC")
	}
	f.jsonCall(t, false, "add", "--inspect-wc", f.wc, "--", "old.txt")
	f.jsonCall(t, false, "info", "--url", f.repoURL, "--wc", f.wc)
	f.jsonCall(t, false, "info", "--inspect-wc", f.root)
	// Offline inspection remains local; URL HEAD must fail on the same outage.
	repo := filepath.Join(f.root, "repo")
	if err := os.Rename(repo, repo+"-offline"); err != nil {
		t.Fatal(err)
	}
	f.jsonCall(t, true, "info", "--inspect-wc", f.wc)
	f.jsonCall(t, false, "info", "--url", f.repoURL)
}

func TestRemoteStatusDoesNotResurrectStaleLocalLock(t *testing.T) {
	f := newFixture(t, "old.txt")
	f.svnRun(t, "lock", "--username", "holder", "-m", "remote proof", "old.txt")
	read := func() map[string]any {
		raw := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--show-updates", "--depth", "empty", "--", "old.txt")
		if raw["remote"] != true {
			t.Fatal(raw)
		}
		return raw["entries"].([]any)[0].(map[string]any)
	}
	row := read()
	if row["repos_lock"] == nil || row["local_lock"] == nil {
		t.Fatal(row)
	}
	// Fixture-only administrator action; not exposed by the FileES helper.
	f.svnRun(t, "unlock", "--force", "--username", "admin", f.repoURL+"/old.txt")
	row = read()
	if row["repos_lock"] != nil || row["local_lock"] == nil {
		t.Fatalf("stale local token fixture: %#v", row)
	}
}
