package mobileworker

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiagnosticLogRotatesFourBoundedFilesWithoutLedgerChanges(t *testing.T) {
	root := t.TempDir()
	ledger := Ledger{Dir: root}
	if err := ledger.Put(Record{RequestID: "untouched", Revision: 27}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "untouched.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	full := bytes.Repeat([]byte("old\n"), diagnosticLogLimit/4)
	for cycle := 0; cycle < 6; cycle++ {
		if err := os.WriteFile(filepath.Join(root, "errors.log"), full, 0600); err != nil {
			t.Fatal(err)
		}
		ledger.LogError("fixture", "synthetic", "APPEND", "latest diagnostic")
	}
	logs, err := filepath.Glob(filepath.Join(root, "errors.log*"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range logs {
		if strings.HasSuffix(p, ".lock") {
			continue
		}
		count++
		i, e := os.Stat(p)
		if e != nil || i.Size() > diagnosticLogLimit {
			t.Fatalf("unbounded log %s %v", p, e)
		}
		if runtime.GOOS != "windows" && i.Mode().Perm() != 0600 {
			t.Fatalf("mode %o", i.Mode().Perm())
		}
	}
	if count != diagnosticLogFiles {
		t.Fatalf("files=%d", count)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("ledger modified", err)
	}
	active, err := os.ReadFile(filepath.Join(root, "errors.log"))
	if err != nil || !bytes.Contains(active, []byte("latest diagnostic")) {
		t.Fatal("latest diagnostic lost", err)
	}
}

func TestDiagnosticLogMigratesOversizedLegacyFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "errors.log")
	if e := os.WriteFile(path, bytes.Repeat([]byte("old line\n"), diagnosticLogLimit/9+100), 0600); e != nil {
		t.Fatal(e)
	}
	Ledger{Dir: root}.LogError("fixture", "synthetic", "APPEND", strings.Repeat("x", 4096))
	i, e := os.Stat(path + ".1")
	if e != nil || i.Size() > diagnosticLogLimit {
		t.Fatal("legacy file not bounded", e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || len(raw) > 700 || bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatal("field bound changed", e)
	}
	if temps, _ := filepath.Glob(filepath.Join(root, ".errors-log-*.tmp")); len(temps) != 0 {
		t.Fatal("temporary residue")
	}
}

func TestDiagnosticLogRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if e := os.WriteFile(target, []byte("preserved"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, filepath.Join(root, "errors.log")); e != nil {
		t.Skip("symlink unavailable")
	}
	Ledger{Dir: root}.LogError("fixture", "synthetic", "APPEND", "error")
	raw, e := os.ReadFile(target)
	if e != nil || string(raw) != "preserved" {
		t.Fatal("symlink target modified", e)
	}
}

func TestDiagnosticLogSerializesWorkerProcesses(t *testing.T) {
	if root := os.Getenv("FILEES_TEST_DIAGNOSTIC_LOG_ROOT"); root != "" {
		for i := 0; i < 200; i++ {
			Ledger{Dir: root}.LogError(fmt.Sprintf("%s-%d", os.Getenv("FILEES_TEST_LOG_CHILD"), i), "synthetic", "APPEND", "fixture")
		}
		return
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "openbsd" {
		t.Skip("server process locks are accepted on Linux/OpenBSD")
	}
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var children []*exec.Cmd
	for _, id := range []string{"a", "b"} {
		cmd := exec.Command(exe, "-test.run=^TestDiagnosticLogSerializesWorkerProcesses$")
		cmd.Env = append(os.Environ(), "FILEES_TEST_DIAGNOSTIC_LOG_ROOT="+root, "FILEES_TEST_LOG_CHILD="+id)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
	}
	for _, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "errors.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte("\n")) != 400 {
		t.Fatal("diagnostic lines lost or interleaved")
	}
}
