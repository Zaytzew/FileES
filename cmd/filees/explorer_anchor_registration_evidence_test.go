//go:build !nocfapi

package main

import (
	"bytes"
	"encoding/json"
	"filees/pkg/localrepo"
	"filees/pkg/privatefile"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistrationEvidenceRetainsFirstAnomalyAndBoundedTail(t *testing.T) {
	d := &registrationEvidence{directory: t.TempDir(), helperHash: "test"}
	key := registrationEvidenceKey("test-root")
	healthy := []byte(`{"schema":"filees.registration-evidence/v1","healthy":true,"phase":"initial"}`)
	bad := []byte(`{"schema":"filees.registration-evidence/v1","anomaly":true,"phase":"first"}`)
	for _, raw := range [][]byte{healthy, bad} {
		if err := d.save(key, raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh collector simulates daemon restart; neither a later healthy
	// registration nor 40 newer failures can overwrite the first bad evidence.
	restarted := &registrationEvidence{directory: d.directory}
	for i := 0; i < 40; i++ {
		if err := restarted.save(key, []byte(`{"schema":"filees.registration-evidence/v1","anomaly":true,"phase":"later"}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := restarted.save(key, healthy, nil); err != nil {
		t.Fatal(err)
	}
	got, err := restarted.read(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got.FirstAnomaly, []byte(`"phase":"first"`)) || !bytes.Contains(got.BeforeAnomaly, []byte(`"phase":"initial"`)) ||
		!bytes.Contains(got.FirstAnomaly, []byte(`"helper_sha256":"test"`)) || len(got.Recent) != 8 {
		t.Fatalf("lost evidence: %+v", got)
	}
	info, err := os.Stat(filepath.Join(d.directory, key))
	if err != nil || info.Size() > registrationEvidenceLimit {
		t.Fatal(info, err)
	}
	if err := privatefile.Verify(d.directory); err != nil {
		t.Fatal(err)
	}
	if err := privatefile.Verify(filepath.Join(d.directory, key)); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationEvidenceOnlyKnownRootsAndNoRepeatedSnapshot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows wiring")
	}
	dir := t.TempDir()
	store, err := localrepo.Open(filepath.Join(dir, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "point")
	if _, err := store.BeginAnchorAttach("server", "repo", root, false); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{profiles: filepath.Join(dir, "state", "servers"), helper: helper, lifecycle: store}
	args := []string{"info", "--root", root}
	first := exec.Command(helper, args...)
	m.registrationTrace(first, args)
	if !strings.Contains(strings.Join(first.Env, "\n"), "FILEES_REGISTRATION_TRACE=sample") {
		t.Fatal(first.Env)
	}
	_, _ = first.Stderr.Write([]byte(`{"schema":"filees.registration-evidence/v1","healthy":false,"phase":"observation"}` + "\n"))
	backoff := exec.Command(helper, args...)
	m.registrationTrace(backoff, args)
	if backoff.Stderr != nil {
		t.Fatal("failed read creates diagnostic IO storm")
	}
	m.registration.retryAfter[registrationEvidenceKey(root)] = time.Now().Add(-time.Second)
	second := exec.Command(helper, args...)
	m.registrationTrace(second, args)
	if !strings.Contains(strings.Join(second.Env, "\n"), "FILEES_REGISTRATION_TRACE=anomaly") {
		t.Fatal(second.Env)
	}
	if _, err := second.Stderr.Write([]byte(`{"schema":"filees.registration-evidence/v1","anomaly":true}` + "\n")); err != nil {
		t.Fatal(err)
	}
	frozen := exec.Command(helper, args...)
	m.registrationTrace(frozen, args)
	if frozen.Stderr != nil {
		t.Fatal("keeps sampling frozen failure")
	}
	unknownArgs := []string{"info", "--root", filepath.Join(dir, "foreign")}
	unknown := exec.Command(helper, unknownArgs...)
	m.registrationTrace(unknown, unknownArgs)
	if unknown.Stderr != nil || unknown.Env != nil {
		t.Fatal("observed foreign root")
	}
	// A new manager sees the persisted first anomaly, with no new snapshot.
	restart := &anchorManager{profiles: m.profiles, helper: helper, lifecycle: store}
	command := exec.Command(helper, args...)
	restart.registrationTrace(command, args)
	if command.Stderr != nil {
		t.Fatal("restart forgot frozen failure")
	}
}

func TestRegistrationEvidenceWriterDrainsOversizeAndUnrelatedStderr(t *testing.T) {
	d := &registrationEvidence{directory: t.TempDir()}
	key := registrationEvidenceKey("root")
	w := registrationEvidenceWriter{accept: func(raw []byte) {
		if err := d.save(key, raw, nil); err != nil {
			t.Fatal(err)
		}
	}}
	for _, s := range []string{strings.Repeat("x", 100000) + "\n", "not json\n", `{"schema":"wrong"}` + "\n", `{"schema":"filees.registration-evidence/v1",`, `"healthy":true}` + "\n"} {
		if n, err := w.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatal(n, err)
		}
	}
	got, err := d.read(key)
	if err != nil || len(got.Recent) != 1 {
		t.Fatal(got, err)
	}
}

func TestRegistrationEvidencePreservesUnreadableFileAndCapsRoots(t *testing.T) {
	d := &registrationEvidence{directory: t.TempDir()}
	key := registrationEvidenceKey("root")
	path := filepath.Join(d.directory, key)
	if err := os.WriteFile(path, []byte("damaged evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := []byte(`{"schema":"filees.registration-evidence/v1","healthy":true}`)
	if err := d.save(key, valid, nil); err == nil {
		t.Fatal("overwrote unreadable evidence")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "damaged evidence" {
		t.Fatal(string(raw))
	}
	for i := 0; i < registrationEvidenceRoots; i++ {
		name := registrationEvidenceKey("reserved-" + strconv.Itoa(i))
		if err := os.WriteFile(filepath.Join(d.directory, name), []byte("reserved"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.save(registrationEvidenceKey("new"), valid, nil); err == nil {
		t.Fatal("unbounded roots")
	}
}

func TestRegistrationEvidenceInvalidJSONCannotCreateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	d := &registrationEvidence{directory: path}
	for _, raw := range [][]byte{[]byte("bad"), []byte(`{"schema":"other"}`), bytes.Repeat([]byte("x"), 32769)} {
		if err := d.save("bad", raw, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Serialized records always remain JSON, including control characters in
	// lifecycle identifiers; there is no string-built command or filename.
	raw, err := json.Marshal(registrationEvidenceLifecycle{Root: "test\nroot"})
	if err != nil || !json.Valid(raw) {
		t.Fatal(err)
	}
}

func TestRegistrationEvidencePreservesCrashStaging(t *testing.T) {
	d := &registrationEvidence{directory: t.TempDir()}
	key := registrationEvidenceKey("root")
	pending := filepath.Join(d.directory, "."+key+".pending")
	if err := os.WriteFile(pending, []byte("previous interrupted evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema":"filees.registration-evidence/v1","healthy":true}`)
	for i := 0; i < 3; i++ {
		if err := d.save(key, raw, nil); err == nil {
			t.Fatal("overwrote crash evidence")
		}
	}
	got, err := os.ReadFile(pending)
	if err != nil || string(got) != "previous interrupted evidence" {
		t.Fatal(string(got), err)
	}
	entries, err := os.ReadDir(d.directory)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
}
