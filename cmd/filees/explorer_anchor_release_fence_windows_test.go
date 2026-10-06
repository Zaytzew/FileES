//go:build windows && !nocfapi

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/commit"
)

func testAnchorReleaseFence(t *testing.T) (*anchorReleaseJournal, *anchorReleaseFence) {
	t.Helper()
	j, _ := newTestAnchorReleaseJournal(t)
	if err := os.MkdirAll(filepath.Dir(anchorReleaseFencePath(j.identity.WC)), 0700); err != nil {
		t.Fatal(err)
	}
	fence, err := j.persistFence(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return j, fence
}

func TestAnchorReleaseFencePersistsAndReloadsBoundEvidence(t *testing.T) {
	j, fence := testAnchorReleaseFence(t)
	if !commit.HasPendingFileRelease(j.identity.WC) {
		t.Fatal("missing writer fence")
	}
	if _, err := j.persistFence(t.Context()); err == nil {
		t.Fatal("pending transaction replaced")
	}
	for _, phase := range anchorReleasePhases[1:] {
		if err := j.request(t.Context(), phase); err != nil {
			t.Fatal(err)
		}
	}
	loaded, token, err := loadAnchorReleaseFence(t.Context(), j.store, j.identity)
	if err != nil || loaded.id != j.id || token.record != fence.record || token.file != fence.file {
		t.Fatal(loaded, token, err)
	}
	if err := j.clearFence(t.Context(), token); err == nil || !commit.HasPendingFileRelease(j.identity.WC) {
		t.Fatal("request cleared recovery fence", err)
	}
}

func TestAnchorReleaseFenceRejectsForeignAndCorruptEvidence(t *testing.T) {
	for _, name := range []string{"server", "repo", "url", "record", "trailing", "unknown", "copy", "root"} {
		t.Run(name, func(t *testing.T) {
			j, fence := testAnchorReleaseFence(t)
			expected := j.identity
			path := anchorReleaseFencePath(j.identity.WC)
			switch name {
			case "server":
				expected.ServerID = "foreign"
			case "repo":
				expected.RepoID = "foreign"
			case "url":
				expected.RepoURL = "file:///foreign"
			case "record":
				fence.record.Record.Source.Index++
				raw, _ := json.Marshal(fence.record)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "trailing", "unknown":
				raw, _ := json.Marshal(fence.record)
				if name == "trailing" {
					raw = append(raw, []byte(" {}")...)
				} else {
					raw = append([]byte("{\"unknown\":1,"), raw[1:]...)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "copy":
				if err := os.WriteFile(filepath.Join(j.store, j.id, "00.json"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := os.Rename(j.identity.WC, j.identity.WC+"-kept"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(fence.record)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := loadAnchorReleaseFence(t.Context(), j.store, expected); err == nil {
				t.Fatal("foreign/corrupt recovery admitted")
			}
			if !commit.HasPendingFileRelease(j.identity.WC) {
				t.Fatal("invalid marker cleared")
			}
		})
	}
}

// Synthetic receipt tests exercise exact marker retirement only. They do not
// prove the SVN/CFAPI effects; those remain the coordinator's native tests.
func testCompletedFenceEvidence(t *testing.T, j *anchorReleaseJournal) {
	t.Helper()
	for _, phase := range anchorReleasePhases[1:] {
		if err := j.request(t.Context(), phase); err != nil {
			t.Fatal(err)
		}
	}
	r, err := j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(j.store, j.id, "data.bin")
	f, info, err := openAnchorReleaseFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	receipt := anchorReleaseRetirement{Version: 1, Record: r, Copy: anchorReleaseID(info)}
	for _, index := range []int{4, 5} {
		if err := writeAnchorReleaseJSON(filepath.Dir(path), index, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestAnchorReleaseFenceClearsOnlyOriginalMarkerAfterReceipts(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "original", true: "replacement"}[replacement], func(t *testing.T) {
			j, fence := testAnchorReleaseFence(t)
			testCompletedFenceEvidence(t, j)
			if replacement {
				path := anchorReleaseFencePath(j.identity.WC)
				if err := os.Rename(path, path+"-kept"); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(fence.record)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := j.clearFence(t.Context(), fence)
			if replacement {
				if err == nil || !commit.HasPendingFileRelease(j.identity.WC) {
					t.Fatal("replacement marker removed", err)
				}
			} else if err != nil || commit.HasPendingFileRelease(j.identity.WC) {
				t.Fatal("completed marker remains", err)
			}
		})
	}
}
