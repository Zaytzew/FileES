//go:build native_svn_probe && windows

package nativesvnprobe

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestExcludeRetainedRefusesOpenWriter(t *testing.T) {
	for _, which := range []string{"working", "retained"} {
		t.Run(which, func(t *testing.T) {
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
			opened := file
			if which == "retained" {
				opened = backup
			}
			wide, err := windows.UTF16PtrFromString(opened)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.GENERIC_WRITE,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
				nil, windows.OPEN_EXISTING, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(handle)
			before := f.status(t)
			f.jsonCall(t, false, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
			if !bytes.Equal(before, f.status(t)) {
				t.Fatal("open-writer refusal changed WC")
			}
			for _, name := range []string{file, backup} {
				got, err := os.ReadFile(name)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("lost bytes at %s: %v", name, err)
				}
			}
		})
	}
}

func TestExcludeRetainedRefusesOfflineAttributes(t *testing.T) {
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
	wide, err := windows.UTF16PtrFromString(file)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.GetFileAttributes(wide)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(wide, attributes|windows.FILE_ATTRIBUTE_OFFLINE); err != nil {
		t.Fatal(err)
	}
	defer windows.SetFileAttributes(wide, attributes)
	f.jsonCall(t, false, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
	if got, err := windows.GetFileAttributes(wide); err != nil || got != attributes|windows.FILE_ATTRIBUTE_OFFLINE {
		t.Fatalf("refusal changed availability attributes: %x %v", got, err)
	}
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("lost working bytes: %v", err)
	}
}

func TestExcludeRetainedRefusesCaseAliasedWCBackup(t *testing.T) {
	f := newFixture(t, "chosen.txt")
	if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.wc, "chosen.txt")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(f.wc, "inside.txt")
	if err := os.WriteFile(backup, body, 0600); err != nil {
		t.Fatal(err)
	}
	before := f.status(t)
	f.jsonCall(t, false, "exclude-retained", "--wc", f.wc, "--retained", strings.ToUpper(backup), "--", "chosen.txt")
	if !bytes.Equal(before, f.status(t)) {
		t.Fatal("case alias refusal changed WC")
	}
}

// A pathname guard cannot simply surround ordinary SVN exclusion: SVN can
// change WC metadata before failing to delete a protected file. Swallowing
// that error would leave a destructive work item for a later cleanup.
// This uses only a disposable ordinary file, never a replacement race.
func TestSVNExclusionWithDeleteDeniedLeavesPendingRemoval(t *testing.T) {
	f := newFixture(t, "chosen.txt")
	if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.wc, "chosen.txt")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	before := f.svnRun(t, "log", "--xml", f.repoURL)
	wide, err := windows.UTF16PtrFromString(file)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != windows.InvalidHandle {
			windows.CloseHandle(handle)
		}
	}()
	out, err := execute(t, f.wc, f.svn, "--non-interactive", "--no-auth-cache",
		"--config-dir", filepath.Join(f.root, "config"), "update", "--set-depth", "exclude", "--", "chosen.txt")
	if err == nil {
		t.Fatalf("SVN deleted a file protected against deletion: %s", out)
	}
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("protected source bytes changed: %v", err)
	}
	f.jsonCall(t, false, "inspect-excluded", "--wc", f.wc, "--", "chosen.txt")
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	// No substitute is ever placed here. Cleanup removes only our original
	// disposable source, demonstrating the pending effect through public API.
	f.svnRun(t, "cleanup")
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not finish the pending original-file removal: %v", err)
	}
	got := f.jsonCall(t, true, "inspect-excluded", "--wc", f.wc, "--", "chosen.txt")
	if got["state"] != "excluded" || got["wc_idle"] != true {
		t.Fatalf("completed cleanup did not give an idle exclusion receipt: %#v", got)
	}
	if after := f.svnRun(t, "log", "--xml", f.repoURL); !bytes.Equal(after, before) {
		t.Fatal("local exclusion changed repository history")
	}
}
