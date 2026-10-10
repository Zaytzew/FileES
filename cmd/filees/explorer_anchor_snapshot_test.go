//go:build !nocfapi

package main

import (
	"errors"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
)

func TestReleaseSnapshotRefreshesVersionsButNeverAddsPaths(t *testing.T) {
	p := contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: "dir", Action: "release"}
	first, err := anchorBatchPlan(t.Context(), batchManifest(t, "{\"path\":\"dir\",\"kind\":\"dir\"}\n{\"path\":\"dir/a\",\"kind\":\"file\",\"size\":10}\n{\"path\":\"dir/b\",\"kind\":\"file\",\"size\":10}\n"), "dir", "release", "binding", func(paths []string) ([]cfapiAnswer, error) {
		return []cfapiAnswer{batchState(paths[0], true), batchState(paths[1], false)}, nil
	})
	if err != nil || first.preview.Files != 2 || first.preview.Bytes != 10 {
		t.Fatal(first, err)
	}
	first.rootID = "1:2"
	var plans anchorReleasePlans
	plans.put(p, first, "operation", "url", "root")
	request := contract.RepoAnchorQueuePayload{RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: first.preview.Fingerprint}, Files: 2}
	snapshot, ok := plans.get(request, "operation", "url", "root")
	if !ok || strings.Join(snapshot.Paths, ",") != "dir/a,dir/b" {
		t.Fatal(snapshot)
	}
	freshManifest := batchManifest(t, "{\"path\":\"dir\",\"kind\":\"dir\"}\n{\"path\":\"dir/a\",\"kind\":\"file\",\"size\":20}\n{\"path\":\"dir/b\",\"kind\":\"file\",\"size\":10}\n{\"path\":\"dir/new\",\"kind\":\"file\",\"size\":30}\n")
	fresh, err := anchorBatchPlanSnapshot(t.Context(), freshManifest, "dir", "release", "binding", snapshot.Paths, func(paths []string) ([]cfapiAnswer, error) {
		if strings.Join(paths, ",") != "dir/a,dir/b" {
			t.Fatal("new file probed", paths)
		}
		a, b := batchState(paths[0], true), batchState(paths[1], true)
		size := int64(20)
		a.Size = &size
		a.USN = "99"
		return []cfapiAnswer{a, b}, nil
	})
	if err != nil || fresh.preview.Files != 2 || fresh.preview.Bytes != 30 || fresh.preview.Fingerprint == first.preview.Fingerprint {
		t.Fatal(fresh, err)
	}
	if _, ok := plans.get(request, "new-operation", "url", "root"); ok {
		t.Fatal("preview crossed attachment")
	}
	if _, err := anchorBatchPlanSnapshot(t.Context(), freshManifest, "dir", "release", "binding", []string{"dir/missing"}, nil); !errors.Is(err, ipcserver.ErrAnchorSelectionChanged) {
		t.Fatal("missing member admitted", err)
	}
}

func TestReleaseSnapshotWaitsForUnpublishedMetadata(t *testing.T) {
	manifest := batchManifest(t, "{\"path\":\"a\",\"kind\":\"file\",\"size\":10}\n")
	_, err := anchorBatchPlanSnapshot(t.Context(), manifest, ".", "release", "binding", []string{"a"}, func(paths []string) ([]cfapiAnswer, error) {
		s := batchState("a", true)
		s.InSync = false
		return []cfapiAnswer{s}, nil
	})
	if !errors.Is(err, errAnchorReleaseLocalWork) {
		t.Fatal(err)
	}
}

func TestReleaseSnapshotPlanUsesPortableNameModel(t *testing.T) {
	for _, pair := range []struct {
		a, b   string
		reject bool
	}{
		{"ı.txt", "I.txt", true}, {"i.txt", "İ.txt", false}, {"straße.txt", "STRASSE.txt", false},
	} {
		t.Run(pair.a+"-"+pair.b, func(t *testing.T) {
			manifest := batchManifest(t, "{\"path\":\"dir\",\"kind\":\"dir\"}\n{\"path\":\"dir/"+pair.a+"\",\"kind\":\"file\",\"size\":10}\n{\"path\":\"dir/"+pair.b+"\",\"kind\":\"file\",\"size\":10}\n")
			_, err := anchorBatchPlan(t.Context(), manifest, "dir", "release", "binding", func(paths []string) ([]cfapiAnswer, error) {
				return []cfapiAnswer{batchState(paths[0], true), batchState(paths[1], true)}, nil
			})
			if (err != nil) != pair.reject {
				t.Fatal("wrong portable collision decision", pair, err)
			}
		})
	}
}
