//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

// Ordinary synthetic fixtures test persistence without requiring the filter.
// Real preparation of a retained CFAPI file is tested by the native probe.
func newTestAnchorReleaseJournal(t *testing.T) (*anchorReleaseJournal, []byte) {
	t.Helper()
	wc, store := t.TempDir(), t.TempDir()
	body := bytes.Repeat([]byte("owned release evidence"), 2000)
	source := filepath.Join(wc, "drawing.dwg")
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	dir := filepath.Join(store, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	wide, _ := windows.UTF16PtrFromString(wc)
	h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	var rootInfo windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &rootInfo)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	f, sourceInfo, err := openAnchorReleaseFile(source, false)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	identity := anchorReleaseIdentity{ServerID: "fixture", RepoID: "fixture-repo", RepoURL: "file:///fixture", WC: wc, Rel: "drawing.dwg", Revision: 1}
	digest := sha256.Sum256(body)
	record := anchorReleaseRecord{Version: 1, ID: id, Identity: identity, Root: anchorReleaseID(rootInfo), Source: anchorReleaseID(sourceInfo), USN: "1", Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:]), Phase: anchorReleasePhases[0]}
	if err := writeAnchorReleaseRecord(dir, 0, record); err != nil {
		t.Fatal(err)
	}
	return &anchorReleaseJournal{store: store, identity: identity, id: id}, body
}

func TestAnchorReleaseJournalReloadsRequestsWithoutInferringEffects(t *testing.T) {
	j, body := newTestAnchorReleaseJournal(t)
	for i, phase := range anchorReleasePhases {
		if i > 0 {
			if err := j.request(t.Context(), phase); err != nil {
				t.Fatal(err)
			}
		}
		restarted := &anchorReleaseJournal{store: j.store, identity: j.identity, id: j.id}
		record, err := restarted.inspect(t.Context())
		if err != nil || record.Phase != phase {
			t.Fatalf("restart %s: %+v %v", phase, record, err)
		}
		if err := restarted.request(t.Context(), phase); err == nil {
			t.Fatal("duplicate accepted")
		}
		if err := restarted.request(t.Context(), "success"); err == nil {
			t.Fatal("request became success")
		}
		for _, file := range []string{filepath.Join(j.identity.WC, j.identity.Rel), filepath.Join(j.store, j.id, "data.bin")} {
			if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) {
				t.Fatal("journal changed/retired bytes")
			}
		}
	}
}

func TestAnchorReleaseJournalRefusesMissingOrChangedEvidence(t *testing.T) {
	for _, name := range []string{"same-size-bytes", "missing-bytes", "partial-record", "unknown-field", "trailing", "gap", "identity", "repo", "wc-replaced", "cancelled", "skip"} {
		t.Run(name, func(t *testing.T) {
			j, body := newTestAnchorReleaseJournal(t)
			dir := filepath.Join(j.store, j.id)
			record, err := j.inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			recordPath := filepath.Join(dir, "00.json")
			ctx := t.Context()
			switch name {
			case "same-size-bytes":
				body[0] ^= 1
				err = os.WriteFile(filepath.Join(dir, "data.bin"), body, 0600)
			case "missing-bytes":
				err = os.Rename(filepath.Join(dir, "data.bin"), filepath.Join(dir, "saved-data.bin"))
			case "partial-record":
				err = os.WriteFile(recordPath, []byte("{"), 0600)
			case "unknown-field", "trailing":
				raw, _ := json.Marshal(record)
				if name == "unknown-field" {
					raw = append(raw[:len(raw)-1], []byte(",\"Extra\":true}")...)
				} else {
					raw = append(raw, []byte(" {}")...)
				}
				err = os.WriteFile(recordPath, raw, 0600)
			case "gap":
				record.Phase = "exclude_requested"
				err = writeAnchorReleaseRecord(dir, 2, record)
			case "identity":
				record.Source.Index++
				record.Phase = "prepare_requested"
				err = writeAnchorReleaseRecord(dir, 1, record)
			case "repo":
				j.identity.RepoID = "different-repository"
			case "wc-replaced":
				err = os.Rename(j.identity.WC, j.identity.WC+"-saved")
				if err == nil {
					t.Cleanup(func() { _ = os.Rename(j.identity.WC+"-saved", j.identity.WC) })
					err = os.Mkdir(j.identity.WC, 0700)
					t.Cleanup(func() { _ = os.Remove(j.identity.WC) })
				}
			case "cancelled":
				var cancel func()
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "skip":
				if err := j.request(ctx, "exclude_requested"); err == nil {
					t.Fatal("skipped prepare intent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := j.inspect(ctx); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			if err := j.request(ctx, "prepare_requested"); err == nil {
				t.Fatal("invalid evidence authorized request")
			}
		})
	}
}

func TestAnchorReleaseJournalKeepsUncertainWorkAndPendingWrites(t *testing.T) {
	j, retained := newTestAnchorReleaseJournal(t)
	dir := filepath.Join(j.store, j.id)
	if err := j.request(t.Context(), "prepare_requested"); err != nil {
		t.Fatal(err)
	}
	newWork := []byte("new local work after uncertain external operation")
	if err := os.WriteFile(filepath.Join(j.identity.WC, j.identity.Rel), newWork, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pending-interrupted"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	record, err := j.inspect(t.Context())
	if err != nil || record.Phase != "prepare_requested" {
		t.Fatalf("lost intent: %+v %v", record, err)
	}
	if got, err := os.ReadFile(filepath.Join(j.identity.WC, j.identity.Rel)); err != nil || !bytes.Equal(got, newWork) {
		t.Fatal("replayed over new work")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "data.bin")); err != nil || !bytes.Equal(got, retained) {
		t.Fatal("retired backup")
	}
	if _, err := os.Stat(filepath.Join(dir, ".pending-interrupted")); err != nil {
		t.Fatal("partial record removed")
	}
}

func TestAnchorReleaseGuardsRejectWritersAndJunctions(t *testing.T) {
	j, _ := newTestAnchorReleaseJournal(t)
	wide, _ := windows.UTF16PtrFromString(filepath.Join(j.store, j.id, "data.bin"))
	h, err := windows.CreateFile(wide, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.inspect(t.Context()); err == nil {
		t.Fatal("active backup writer admitted")
	}
	windows.CloseHandle(h)
	link := filepath.Join(t.TempDir(), "junction")
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, j.store).CombinedOutput(); err != nil {
		t.Fatalf("owned junction: %s %v", out, err)
	}
	j.store = link
	if _, err := j.inspect(t.Context()); err == nil {
		t.Fatal("junction admitted")
	}
}

func TestAnchorReleaseDirectoryGuardDeniesRename(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "owned")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	guards, err := guardAnchorReleaseDirectories(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer closeAnchorReleaseGuards(guards)
	if err := os.Rename(dir, dir+"-moved"); err == nil {
		_ = os.Rename(dir+"-moved", dir)
		t.Fatal("directory guard admitted rename")
	}
}

func TestAnchorReleaseRecordDoesNotReplaceOrAcceptCancellation(t *testing.T) {
	j, _ := newTestAnchorReleaseJournal(t)
	dir := filepath.Join(j.store, j.id)
	record, err := j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAnchorReleaseRecord(dir, 0, record); err == nil {
		t.Fatal("phase replaced")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := j.request(ctx, "prepare_requested"); err == nil {
		t.Fatal("cancelled request accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "01.json")); !os.IsNotExist(err) {
		t.Fatal("cancelled request recorded")
	}
}
