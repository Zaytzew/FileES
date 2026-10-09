//go:build native_svn_probe && windows

package nativesvnprobe

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Deliberately inconsistent metadata belongs only to this disposable fixture.
// The conditional exclusion must not consume or append to another work queue.
func TestIdleBoundExclusionRefusesExistingQueue(t *testing.T) {
	f := newFixture(t, "chosen.txt")
	if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	sqlite := tool(t, "FILEES_TEST_SQLITE", "")
	file, backup := filepath.Join(f.wc, "chosen.txt"), filepath.Join(f.root, "retained.txt")
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
	info := f.jsonCall(t, true, "inspect-excluded", "--wc", f.wc, "--", "chosen.txt")
	db := filepath.Join(f.wc, ".svn", "wc.db")
	if out, err := execute(t, f.wc, sqlite, db, "INSERT INTO WORK_QUEUE(work) VALUES (CAST('(foreign-work chosen.txt)' AS BLOB));"); err != nil {
		t.Fatalf("fixture queue: %s %v", out, err)
	}
	got := f.jsonCall(t, false, "exclude-bound", "--wc", f.wc, "--retained", backup,
		"--source-id", fmt.Sprintf("%d:%d", id.VolumeSerialNumber, uint64(id.FileIndexHigh)<<32|uint64(id.FileIndexLow)),
		"--revision", fmt.Sprint(info["revision"]), "--uuid", fmt.Sprint(info["repos_uuid"]), "--url", fmt.Sprint(info["url"]), "--", "chosen.txt")
	// The helper may reject unfinished work before the atomic runtime check.
	if !strings.Contains(fmt.Sprint(got), "existing work queue") && !strings.Contains(fmt.Sprint(got), "Previous operation has not finished") {
		t.Fatalf("wrong refusal: %#v", got)
	}
	for _, path := range []string{file, backup} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
			t.Fatalf("refusal changed bytes: %v", err)
		}
	}
	if out, err := execute(t, f.wc, sqlite, "-readonly", db, "SELECT count(*) FROM WORK_QUEUE; SELECT presence FROM NODES WHERE local_relpath='chosen.txt' AND op_depth=0;"); err != nil || strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")) != "1\nnormal" {
		t.Fatalf("queue or BASE changed: %s %v", out, err)
	}
}
