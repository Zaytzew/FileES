//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectExcludedUsesMetadataNotMissingPath(t *testing.T) {
	for _, state := range []string{"normal", "missing", "deleted", "modified", "replacement", "unknown", "directory", "private", "escape", "root", "nested-wc"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t, "chosen.txt")
			if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			rel := "chosen.txt"
			switch state {
			case "missing":
				rename(t, filepath.Join(f.wc, rel), filepath.Join(f.root, "saved.txt"))
			case "deleted":
				f.svnRun(t, "delete", rel)
			case "modified":
				write(t, filepath.Join(f.wc, rel), "unpublished edit")
			case "replacement":
				f.svnRun(t, "delete", rel)
				write(t, filepath.Join(f.wc, rel), "new local object")
				f.svnRun(t, "add", rel)
			case "unknown":
				rel = "not-versioned.txt"
				write(t, filepath.Join(f.wc, rel), "new local object")
			case "directory":
				rel = "folder"
			case "private":
				rel = ".svn/wc.db"
			case "escape":
				rel = "../outside.txt"
			case "root":
				rel = "."
			case "nested-wc":
				f.svnRun(t, "checkout", f.repoURL, "nested")
				rel = "nested/chosen.txt"
			}
			before := f.status(t)
			body, bodyErr := os.ReadFile(filepath.Join(f.wc, rel))
			db, err := os.ReadFile(filepath.Join(f.wc, ".svn", "wc.db"))
			if err != nil {
				t.Fatal(err)
			}
			good := state == "normal" || state == "missing" || state == "deleted" || state == "modified"
			got := f.jsonCall(t, good, "inspect-excluded", "--wc", f.wc, "--", rel)
			if good && (got["state"] != "not_excluded" || got["revision"] != float64(1) || got["path"] != rel) {
				t.Fatalf("ordinary/missing/deleted file appeared excluded: %#v", got)
			}
			if now := f.status(t); !bytes.Equal(now, before) {
				t.Fatalf("inspection changed status: %s\n%s", before, now)
			}
			if now, err := os.ReadFile(filepath.Join(f.wc, ".svn", "wc.db")); err != nil || sha256.Sum256(now) != sha256.Sum256(db) {
				t.Fatalf("inspection changed WC database: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.wc, ".svn", "filees-native-writer-v1")); !os.IsNotExist(err) {
				t.Fatalf("read-only inspection created a writer record: %v", err)
			}
			if bodyErr == nil {
				if now, err := os.ReadFile(filepath.Join(f.wc, rel)); err != nil || !bytes.Equal(body, now) {
					t.Fatalf("inspection changed local bytes: %v", err)
				}
			}
		})
	}
}

func TestInspectExcludedOfflineAfterLostReceiptAndReadoption(t *testing.T) {
	f := newFixture(t, "chosen.txt")
	if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	file, backup := filepath.Join(f.wc, "chosen.txt"), filepath.Join(f.root, "retained.txt")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, body, 0600); err != nil {
		t.Fatal(err)
	}
	uuid := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "repos-uuid", ".")))
	log := f.svnRun(t, "log", "--xml", f.repoURL)
	// Discard the mutation receipt. A NEW helper process must inspect the
	// actual metadata instead of replaying exclusion or guessing from absence.
	f.jsonCall(t, true, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
	repo, offline := filepath.Join(f.root, "repo"), filepath.Join(f.root, "repo-offline")
	rename(t, repo, offline)
	restored := false
	defer func() {
		if !restored {
			_ = os.Rename(offline, repo)
		}
	}()
	for _, present := range []bool{false, true} {
		if present {
			write(t, file, "new object after uncertain exclusion")
		}
		db, err := os.ReadFile(filepath.Join(f.wc, ".svn", "wc.db"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			got := f.jsonCall(t, true, "inspect-excluded", "--wc", f.wc, "--", "chosen.txt")
			if got["state"] != "excluded" || got["revision"] != float64(1) || got["path"] != "chosen.txt" ||
				got["wc"] != filepath.ToSlash(f.wc) || got["url"] != f.repoURL+"/chosen.txt" ||
				got["repos_root_url"] != f.repoURL || got["repos_uuid"] != uuid {
				t.Fatalf("missing exact excluded identity (present=%v): %#v", present, got)
			}
		}
		if now, err := os.ReadFile(filepath.Join(f.wc, ".svn", "wc.db")); err != nil || !bytes.Equal(db, now) {
			t.Fatalf("offline inspection changed database: %v", err)
		}
		if present {
			if now, err := os.ReadFile(file); err != nil || string(now) != "new object after uncertain exclusion" {
				t.Fatalf("inspection replaced a new local object: %v", err)
			}
		} else if _, err := os.Lstat(file); !os.IsNotExist(err) {
			t.Fatalf("inspection fetched a missing file: %v", err)
		}
	}
	rename(t, offline, repo)
	restored = true
	// Explicitly replace only OUR synthetic obstruction with retained bytes
	// for the adoption test, not as a behavior of the inspection operation.
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	f.jsonCall(t, true, "update", "--wc", f.wc, "--set-depth", "infinity", "--parents", "--adopt", "--revision", "1", "--", "chosen.txt")
	got := f.jsonCall(t, true, "inspect-excluded", "--wc", f.wc, "--", "chosen.txt")
	if got["state"] != "not_excluded" {
		t.Fatalf("readopted file still appeared excluded: %#v", got)
	}
	if now := f.svnRun(t, "log", "--xml", f.repoURL); !bytes.Equal(log, now) {
		t.Fatal("inspection cycle changed repository history")
	}
	if now, err := os.ReadFile(backup); err != nil || !bytes.Equal(body, now) {
		t.Fatalf("inspection lost retained evidence: %v", err)
	}
}

func TestExcludeRetainedPathKeepsRepositoryAndAllowsReadoption(t *testing.T) {
	f := newFixture(t, "chosen.txt")
	if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.wc, "chosen.txt")
	backup := filepath.Join(f.root, "retained.txt")
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	before := f.svnRun(t, "log", "--xml", f.repoURL)
	// Even a clean file needs an independent retained copy before exclusion.
	f.jsonCall(t, false, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
	if err := os.WriteFile(backup, content, 0600); err != nil {
		t.Fatal(err)
	}
	got := f.jsonCall(t, true, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
	if got["state"] != "excluded" || got["path"] != "chosen.txt" || got["revision"] != float64(1) {
		t.Fatalf("unexpected receipt: %#v", got)
	}
	if raw := f.svnRun(t, "status", "--xml"); bytes.Contains(raw, []byte(`item="deleted"`)) || bytes.Contains(raw, []byte(`item="missing"`)) {
		t.Fatalf("excluded path became a publication: %s", raw)
	}
	if now := f.svnRun(t, "log", "--xml", f.repoURL); !bytes.Equal(now, before) {
		t.Fatal("exclusion changed repository history")
	}
	if got, err := os.ReadFile(backup); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("retained bytes changed: %v", err)
	}
	f.svnRun(t, "update") // regular polling must respect the exclusion
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("plain update rematerialized: %v", err)
	}
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	f.jsonCall(t, true, "update", "--wc", f.wc, "--set-depth", "infinity", "--parents", "--adopt", "--revision", "1", "--", "chosen.txt")
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("adoption changed bytes: %v", err)
	}
	if raw := f.svnRun(t, "status", "--xml", "chosen.txt"); bytes.Contains(raw, []byte(`item="modified"`)) {
		t.Fatalf("readoption not clean: %s", raw)
	}
}

func TestExcludeRetainedPathRefusesUnsafeStates(t *testing.T) {
	for _, state := range []string{"modified", "metadata-hidden-edit", "property", "translation-property", "local-lock", "remote-lock", "unversioned", "deleted", "missing", "directory", "private", "outside", "root", "mismatched-copy", "inside-copy", "same-copy", "hardlinked-copy", "relative-copy"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t, "chosen.txt")
			if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			path := "chosen.txt"
			backup := filepath.Join(f.root, "retained.txt")
			content, err := os.ReadFile(filepath.Join(f.wc, path))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(backup, content, 0600); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "metadata-hidden-edit":
				file := filepath.Join(f.wc, path)
				info, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				content[0] = 'F'
				if err := os.WriteFile(file, content, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backup, content, 0600); err != nil {
					t.Fatal(err)
				}
			case "translation-property":
				f.svnRun(t, "propset", "svn:eol-style", "native", path)
				f.svnRun(t, "commit", "--username", "creator", "-m", "translation", path)
				f.svnRun(t, "update")
			case "modified":
				content = append(content, []byte("changed")...)
				if err := os.WriteFile(filepath.Join(f.wc, path), content, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backup, content, 0600); err != nil {
					t.Fatal(err)
				}
			case "property":
				f.svnRun(t, "propset", "filees:probe", "pending", path)
			case "local-lock":
				f.svnRun(t, "lock", "--username", "creator", "-m", "held", path)
			case "remote-lock":
				f.svnRun(t, "lock", "--username", "creator", "-m", "remote held", f.repoURL+"/chosen.txt")
			case "unversioned":
				path = "unknown.txt"
			case "deleted":
				f.svnRun(t, "delete", path)
			case "directory":
				path = "folder"
			case "private":
				path = ".svn/wc.db"
			case "outside":
				path = "../retained.txt"
			case "root":
				path = "."
			case "missing":
				rename(t, filepath.Join(f.wc, path), filepath.Join(f.root, "staged.txt"))
			case "mismatched-copy":
				if err := os.WriteFile(backup, []byte("not the file"), 0600); err != nil {
					t.Fatal(err)
				}
			case "inside-copy":
				backup = filepath.Join(f.wc, "occupied.txt")
			case "same-copy":
				backup = filepath.Join(f.wc, path)
			case "hardlinked-copy":
				backup = filepath.Join(f.root, "hardlink.txt")
				if err := os.Link(filepath.Join(f.wc, path), backup); err != nil {
					t.Fatal(err)
				}
			case "relative-copy":
				backup = "retained.txt"
			}
			preserved := make(map[string][]byte)
			for _, name := range []string{filepath.Join(f.wc, "chosen.txt"), backup} {
				if body, err := os.ReadFile(name); err == nil {
					preserved[name] = body
				}
			}
			before := f.status(t)
			refused := f.jsonCall(t, false, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", path)
			if (state == "local-lock" || state == "remote-lock") && !strings.Contains(fmt.Sprint(refused["errors"]), "locked file") {
				t.Fatalf("did not observe the actual lock: %#v", refused)
			}
			if now := f.status(t); !bytes.Equal(now, before) {
				t.Fatalf("refusal changed WC: %s\n%s", before, now)
			}
			for name, body := range preserved {
				got, err := os.ReadFile(name)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("refusal changed retained/working bytes at %s: %v", name, err)
				}
			}
		})
	}
}
