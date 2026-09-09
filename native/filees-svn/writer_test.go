//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A real post-commit executable, not a mock receipt or an edit to wc.db.
func init() {
	if len(os.Args) < 3 || !strings.HasPrefix(filepath.Base(os.Args[0]), "post-commit") {
		return
	}
	// SVN deliberately sanitizes hook environments on Unix. Derive the
	// isolated fixture path from the documented hook argv, not an env var.
	gate := filepath.Join(filepath.Dir(os.Args[1]), "entered")
	f, err := os.OpenFile(gate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(81)
	}
	_, err = f.WriteString("remote committed\n")
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		os.Exit(82)
	}
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(gate + ".release"); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(83)
}

func TestWriterLeaseCrashAndRecovery(t *testing.T) {
	f := newFixture(t, "old.txt")
	gate := filepath.Join(f.root, "entered")
	hook := filepath.Join(f.root, "repo", "hooks", "post-commit")
	if runtime.GOOS == "windows" {
		hook += ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(hook, data, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wc, "new.txt"), "published content\n")
	write(t, filepath.Join(f.wc, "other.txt"), "not selected\n")
	f.jsonCall(t, true, "add", "--disposable-wc", f.wc, "--", "new.txt")
	cmd := exec.Command(f.probe, "commit", "--disposable-wc", f.wc, "-m", "barrier", "--revprop", "filees:commit-id="+repairMarker, "--", "new.txt")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = os.WriteFile(gate+".release", []byte("release"), 0600)
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("barrier not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := f.status(t)
	// Every native WC mutator uses the same lease. Read-only status still works.
	for _, args := range [][]string{
		{"add", "--", "other.txt"}, {"delete", "--", "old.txt"},
		{"propset", "custom:x", "y", "--", "old.txt"}, {"propdel", "custom:x", "--", "old.txt"},
		{"cleanup"}, {"revert", "--", "new.txt"}, {"resolve", "--accept", "mine-full", "--", "new.txt"},
		{"update"}, {"lock", "--", "old.txt"}, {"unlock", "--", "old.txt"},
		{"commit", "-m", "must refuse", "--", "old.txt"},
	} {
		got := f.jsonCall(t, false, append([]string{args[0], "--disposable-wc", f.wc}, args[1:]...)...)
		if !strings.Contains(fmt.Sprint(got["errors"]), "another FileES native writer is active") {
			t.Fatal("wrong refusal", args, got)
		}
	}
	recoveryCall(t, f, false, repairMarker, f.repoURL, "new.txt")
	move := f.jsonCall(t, false, "record-move", "--disposable-wc", f.wc, "old.txt", "other.txt")
	if !strings.Contains(fmt.Sprint(move["errors"]), "another FileES native writer is active") {
		t.Fatal("wrong move refusal", move)
	}
	if !bytes.Equal(before, f.status(t)) {
		t.Fatal("active writer was disturbed")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(gate+".release", []byte("release"), 0600)
	_ = cmd.Wait()
	waited = true
	record := filepath.Join(f.wc, ".svn", "filees-native-writer-v1")
	b, err := os.ReadFile(record)
	if err != nil || !bytes.Contains(b, []byte(repairMarker)) {
		t.Fatal("owner lost", string(b), err)
	}
	owner := append([]byte(nil), b...)
	staleStatus := f.status(t)
	// Simulate a foreign/unrecorded or torn provenance record. An exact
	// server UUID alone does NOT authorize breaking the leftover SVN lock.
	for _, bad := range [][]byte{nil, []byte("torn record")} {
		if err := os.WriteFile(record, bad, 0600); err != nil {
			t.Fatal(err)
		}
		recoveryCall(t, f, false, repairMarker, f.repoURL, "new.txt")
		if !bytes.Equal(staleStatus, f.status(t)) {
			t.Fatal("unowned lock mutated")
		}
	}
	if err := os.WriteFile(record, owner, 0600); err != nil {
		t.Fatal(err)
	}
	// Same URL/history/commit marker in a replacement repository is not the
	// same authority. Refuse BEFORE cleanup, then restore the fixture UUID.
	originalUUID := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "repos-uuid", f.repoURL)))
	admin := tool(t, "FILEES_PROBE_SVNADMIN", "svnadmin")
	setUUID := func(id string) {
		t.Helper()
		if out, e := execute(t, f.root, admin, "setuuid", filepath.Join(f.root, "repo"), id); e != nil {
			t.Fatal(e, string(out))
		}
	}
	setUUID("02aee0b7-25d8-4c39-a7a4-e028f3162438")
	recoveryCall(t, f, false, repairMarker, f.repoURL, "new.txt")
	if !bytes.Equal(staleStatus, f.status(t)) {
		t.Fatal("foreign repository caused cleanup")
	}
	setUUID(originalUUID)
	// No explicit cleanup. A stale owner still fences ordinary mutators.
	f.jsonCall(t, false, "cleanup", "--disposable-wc", f.wc)
	f.jsonCall(t, false, "add", "--disposable-wc", f.wc, "--", "other.txt")
	recoveryCall(t, f, false, "different-receipt", f.repoURL, "new.txt")
	later := "later unsent content\n"
	write(t, filepath.Join(f.wc, "new.txt"), later)
	for range 2 {
		recoveryCall(t, f, true, repairMarker, f.repoURL, "new.txt")
	}
	b, err = os.ReadFile(record)
	if err != nil || len(b) != 0 {
		t.Fatal("owner not retired", string(b), err)
	}
	b, err = os.ReadFile(filepath.Join(f.wc, "new.txt"))
	if err != nil || string(b) != later {
		t.Fatal("later edit lost", string(b), err)
	}
	if got := string(f.svnRun(t, "status", "--xml", "new.txt")); !strings.Contains(got, `item="modified"`) {
		t.Fatal(got)
	}
	if got := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL))); got != "2" {
		t.Fatal("replayed", got)
	}
	f.jsonCall(t, true, "add", "--disposable-wc", f.wc, "--", "other.txt")
}
