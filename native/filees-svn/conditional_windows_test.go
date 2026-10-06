//go:build native_svn_probe && windows

package nativesvnprobe

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConditionalExclusionRecovery(t *testing.T) {
	probe := tool(t, "FILEES_SVN_RELEASE_PROBE", "")
	for _, mode := range []string{"defer", "crash"} {
		for _, change := range []string{"none", "bytes", "replacement", "missing", "parent"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				f := newFixture(t, "chosen.txt")
				f.svnRun(t, "move", "chosen.txt", "folder/chosen.txt")
				f.svnRun(t, "commit", "--username", "creator", "-m", "nested release fixture")
				f.svnRun(t, "update")
				if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
					t.Fatal(err)
				}
				file, backup := filepath.Join(f.wc, "folder", "chosen.txt"), filepath.Join(f.root, "retained.txt")
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backup, body, 0600); err != nil {
					t.Fatal(err)
				}
				before := f.svnRun(t, "log", "--xml", f.repoURL)
				out, err := execute(t, f.wc, probe, file, backup, mode)
				if err == nil || (mode == "defer" && !strings.Contains(string(out), "deferred verified removal")) {
					t.Fatalf("did not interrupt after transition: %v %s", err, out)
				}
				if mode == "crash" {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 77 {
						t.Fatalf("wrong crash boundary: %v %s", err, out)
					}
				}
				if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) {
					t.Fatalf("interruption lost source: %v", err)
				}
				f.jsonCall(t, false, "inspect-excluded", "--wc", f.wc, "--", "folder/chosen.txt")
				// An unmodified client cannot reinterpret the new work item as a
				// pathname delete. Its cleanup must fail without removing bytes.
				oldOut, oldErr := execute(t, f.wc, f.svn, "cleanup", f.wc)
				if oldErr == nil || !strings.Contains(string(oldOut), "filees-remove-verified-v1") {
					t.Fatalf("old runtime accepted the private work item: %v %s", oldErr, oldOut)
				}
				if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) {
					t.Fatalf("old cleanup removed source: %v", err)
				}
				want := body
				switch change {
				case "bytes":
					want = append([]byte(nil), body...)
					want[0] ^= 32 // same identity and size; only the full hash detects this
					if err := os.WriteFile(file, want, 0600); err != nil {
						t.Fatal(err)
					}
				case "replacement":
					rename(t, file, filepath.Join(f.root, "original.txt"))
					if err := os.WriteFile(file, body, 0600); err != nil {
						t.Fatal(err)
					}
				case "missing":
					rename(t, file, filepath.Join(f.root, "original.txt"))
				case "parent":
					rename(t, filepath.Dir(file), filepath.Join(f.root, "original-folder"))
					if err := os.Mkdir(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, body, 0600); err != nil {
						t.Fatal(err)
					}
				}
				ok := change == "none" || change == "missing"
				f.jsonCall(t, ok, "cleanup", "--wc", f.wc)
				if ok {
					if _, err := os.Stat(file); !os.IsNotExist(err) {
						t.Fatalf("verified removal did not finish: %v", err)
					}
					got := f.jsonCall(t, true, "inspect-excluded", "--wc", f.wc, "--", "folder/chosen.txt")
					if got["state"] != "excluded" || got["wc_idle"] != true {
						t.Fatalf("bad idle receipt: %#v", got)
					}
				} else {
					for i := 0; i < 2; i++ { // persistent refusal, never a one-time pathname fallback
						f.jsonCall(t, false, "cleanup", "--wc", f.wc)
						if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
							t.Fatalf("recovery changed current file: %v", err)
						}
					}
				}
				if got, err := os.ReadFile(backup); err != nil || !bytes.Equal(got, body) {
					t.Fatalf("lost retained copy: %v", err)
				}
				if !bytes.Equal(before, f.svnRun(t, "log", "--xml", f.repoURL)) {
					t.Fatal("local recovery changed repository")
				}
			})
		}
	}
}

func TestConditionalExclusionReadOnly(t *testing.T) {
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
	attrs, err := windows.GetFileAttributes(wide)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(wide, attrs|windows.FILE_ATTRIBUTE_READONLY); err != nil {
		t.Fatal(err)
	}
	f.jsonCall(t, true, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("read-only original not removed: %v", err)
	}
	if got, err := os.ReadFile(backup); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("retained copy changed: %v", err)
	}
}

// The production recovery entry is narrower than cleanup: exact retained
// binding and one private work item, no processing/clearing of other WC work.
func TestConditionalBoundResume(t *testing.T) {
	probe := tool(t, "FILEES_SVN_RELEASE_PROBE", "")
	for _, mode := range []string{"defer", "crash"} {
		for _, change := range []string{"none", "missing", "bytes", "replacement", "parent", "copy", "source-id", "revision", "uuid", "url", "other-file", "no-pending", "foreign-lock"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				f := newFixture(t, "chosen.txt")
				f.svnRun(t, "move", "chosen.txt", "folder/chosen.txt")
				f.svnRun(t, "commit", "--username", "creator", "-m", "bound recovery fixture")
				f.svnRun(t, "update")
				if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
					t.Fatal(err)
				}
				rel := "folder/chosen.txt"
				file, backup := filepath.Join(f.wc, filepath.FromSlash(rel)), filepath.Join(f.root, "retained.txt")
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backup, body, 0600); err != nil {
					t.Fatal(err)
				}
				wide, _ := windows.UTF16PtrFromString(file)
				h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				var id windows.ByHandleFileInformation
				err = windows.GetFileInformationByHandle(h, &id)
				windows.CloseHandle(h)
				if err != nil {
					t.Fatal(err)
				}
				source := fmt.Sprintf("%d:%d", id.VolumeSerialNumber, uint64(id.FileIndexHigh)<<32|uint64(id.FileIndexLow))
				info := f.jsonCall(t, true, "inspect-excluded", "--wc", f.wc, "--", rel)
				revision, uuid, uri := fmt.Sprint(info["revision"]), fmt.Sprint(info["repos_uuid"]), fmt.Sprint(info["url"])
				if change != "no-pending" {
					out, err := execute(t, f.wc, probe, file, backup, mode)
					if err == nil || (mode == "defer" && !strings.Contains(string(out), "deferred verified removal")) {
						t.Fatalf("not interrupted: %v %s", err, out)
					}
				}
				want := append([]byte(nil), body...)
				switch change {
				case "missing":
					rename(t, file, filepath.Join(f.root, "saved.txt"))
				case "bytes":
					want[0] ^= 32
					if err := os.WriteFile(file, want, 0600); err != nil {
						t.Fatal(err)
					}
				case "replacement":
					rename(t, file, filepath.Join(f.root, "saved.txt"))
					if err := os.WriteFile(file, body, 0600); err != nil {
						t.Fatal(err)
					}
				case "parent":
					rename(t, filepath.Dir(file), filepath.Join(f.root, "saved-folder"))
					if err := os.Mkdir(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, body, 0600); err != nil {
						t.Fatal(err)
					}
				case "copy":
					if err := os.WriteFile(backup, []byte("wrong retained bytes"), 0600); err != nil {
						t.Fatal(err)
					}
				case "source-id":
					source = "0:1"
				case "revision":
					revision = "999"
				case "uuid":
					uuid = "11111111-1111-1111-1111-111111111111"
				case "url":
					uri += "-other"
				case "other-file":
					rel = "other.txt"
				case "foreign-lock":
					if out, err := execute(t, f.wc, f.svn, "cleanup", f.wc); err == nil || !strings.Contains(string(out), "filees-remove-verified-v1") {
						t.Fatalf("foreign lock fixture: %v %s", err, out)
					}
				}
				ok := change == "none" || change == "missing"
				got := f.jsonCall(t, ok, "resume-bound", "--wc", f.wc, "--retained", backup, "--source-id", source, "--revision", revision, "--uuid", uuid, "--url", uri, "--", rel)
				if ok {
					if got["wc_idle"] != true || got["state"] != "excluded" {
						t.Fatalf("not idle: %#v", got)
					}
					if _, err := os.Stat(file); !os.IsNotExist(err) {
						t.Fatalf("source not removed: %v", err)
					}
				} else if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
					t.Fatalf("refusal changed source: %v", err)
				}
				if change != "copy" {
					if got, err := os.ReadFile(backup); err != nil || !bytes.Equal(got, body) {
						t.Fatalf("copy lost: %v", err)
					}
				}
			})
		}
	}
}

func TestConditionalExclusionUnavailableRefusesBeforeMutation(t *testing.T) {
	f := newFixture(t, "chosen.txt")
	f.probe = tool(t, "FILEES_SVN_UNMODIFIED_PROBE", "")
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
	db, err := os.ReadFile(filepath.Join(f.wc, ".svn", "wc.db"))
	if err != nil {
		t.Fatal(err)
	}
	got := f.jsonCall(t, false, "exclude-retained", "--wc", f.wc, "--retained", backup, "--", "chosen.txt")
	if !strings.Contains(fmt.Sprint(got), "conditional SVN runtime") {
		t.Fatalf("wrong refusal: %#v", got)
	}
	if now, err := os.ReadFile(filepath.Join(f.wc, ".svn", "wc.db")); err != nil || !bytes.Equal(db, now) {
		t.Fatalf("unsupported runtime changed metadata: %v", err)
	}
	if now, err := os.ReadFile(file); err != nil || !bytes.Equal(body, now) {
		t.Fatalf("unsupported runtime changed source: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(f.wc, ".filees"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported runtime created writer state: %v %#v", err, entries)
	}
}
