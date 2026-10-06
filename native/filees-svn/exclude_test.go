//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
