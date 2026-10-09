//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

func batchManifest(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plan.jsonl")
	if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func batchState(path string, local bool) cfapiAnswer {
	size := int64(10)
	return cfapiAnswer{OK: true, Placeholder: true, Partial: !local, Local: local, InSync: true, Size: &size, USN: "42", Identity: path}
}

func TestAnchorBatchPlanUsesOnlySelectedMetadataAndBindsChanges(t *testing.T) {
	manifest := batchManifest(t, "{\"path\":\"dir\",\"kind\":\"dir\"}\n{\"path\":\"dir/a\",\"kind\":\"file\",\"size\":10}\n{\"path\":\"elsewhere\",\"kind\":\"file\",\"size\":10}\n{\"path\":\"dir/b\",\"kind\":\"file\",\"size\":10}\n")
	usn := "42"
	probe := func(paths []string) ([]cfapiAnswer, error) {
		if strings.Join(paths, ",") != "dir/a,dir/b" {
			t.Fatal(paths)
		}
		states := []cfapiAnswer{batchState(paths[0], false), batchState(paths[1], true)}
		states[0].USN = usn
		return states, nil
	}
	first, err := anchorBatchPlan(t.Context(), manifest, "dir", "download", "point/revision/root", probe)
	if err != nil || !first.preview.Directory || first.preview.Files != 1 || first.preview.Bytes != 10 || first.files[0].rel != "dir/a" {
		t.Fatal(first, err)
	}
	other, err := anchorBatchPlan(t.Context(), manifest, "dir", "release", "point/revision/root", probe)
	if err != nil || other.preview.Files != 1 || other.files[0].rel != "dir/b" || other.preview.Fingerprint == first.preview.Fingerprint {
		t.Fatal(other, err)
	}
	usn = "43"
	changed, err := anchorBatchPlan(t.Context(), manifest, "dir", "download", "point/revision/root", probe)
	if err != nil || changed.preview.Fingerprint == first.preview.Fingerprint {
		t.Fatal("metadata change not bound", err)
	}
	if changed.preview.SelectionFingerprint != first.preview.SelectionFingerprint || other.preview.SelectionFingerprint != "" {
		t.Fatal("download consent changed with USN or destructive release got refresh consent")
	}
	changed, err = anchorBatchPlan(t.Context(), manifest, "dir", "download", "another attachment", probe)
	if err != nil || changed.preview.Fingerprint == first.preview.Fingerprint {
		t.Fatal("attachment change not bound", err)
	}
	if changed.preview.SelectionFingerprint == first.preview.SelectionFingerprint {
		t.Fatal("download consent escaped attachment binding")
	}
}

func TestAnchorBatchRefusesIncompleteForeignDirtyAndAmbiguousPlans(t *testing.T) {
	manifest := batchManifest(t, "{\"path\":\"a\",\"kind\":\"file\",\"size\":10}\n")
	for _, change := range []string{"missing", "failure", "size", "identity", "dirty"} {
		t.Run(change, func(t *testing.T) {
			_, err := anchorBatchPlan(t.Context(), manifest, ".", "release", "binding", func([]string) ([]cfapiAnswer, error) {
				s := batchState("a", true)
				switch change {
				case "missing":
					return nil, nil
				case "failure":
					s.OK = false
				case "size":
					s.Size = nil
				case "identity":
					s.Partial = true
				case "dirty":
					s.InSync = false
				}
				return []cfapiAnswer{s}, nil
			})
			if err == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
	for _, raw := range []string{
		"{\"path\":\"a\",\"kind\":\"file\",\"size\":10}\n{\"path\":\"A\",\"kind\":\"file\",\"size\":10}\n",
		"{\"path\":\".svn/wc.db\",\"kind\":\"file\",\"size\":10}\n",
	} {
		_, err := anchorBatchPlan(t.Context(), batchManifest(t, raw), ".", "download", "binding", func([]string) ([]cfapiAnswer, error) { t.Fatal("invalid plan reached helper"); return nil, nil })
		if err == nil {
			t.Fatal("unsafe manifest accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := anchorBatchPlan(ctx, manifest, ".", "download", "binding", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAnchorDownloadConsentBindsSelectedPathsKindsAndSizes(t *testing.T) {
	plan := func(raw, selected string) string {
		t.Helper()
		selection, err := anchorBatchPlan(t.Context(), batchManifest(t, raw), selected, "download", "same-point", func(paths []string) ([]cfapiAnswer, error) {
			states := make([]cfapiAnswer, len(paths))
			for i, path := range paths {
				// Already local files are still bound into the consent manifest.
				states[i] = batchState(path, true)
			}
			return states, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return selection.preview.SelectionFingerprint
	}
	base := "{\"path\":\"dir\",\"kind\":\"dir\"}\n{\"path\":\"dir/a\",\"kind\":\"file\",\"size\":10}\n"
	first := plan(base, "dir")
	for _, raw := range []string{
		strings.Replace(base, "dir/a", "dir/b", 1),
		strings.Replace(base, "\"size\":10", "\"size\":11", 1),
		strings.Replace(base, "\"kind\":\"file\",\"size\":10", "\"kind\":\"dir\"", 1),
		base + "{\"path\":\"dir/b\",\"kind\":\"file\",\"size\":10}\n",
	} {
		if plan(raw, "dir") == first {
			t.Fatal("changed subtree retained consent")
		}
	}
	if plan(base, ".") == first {
		t.Fatal("larger selection retained consent")
	}
	if plan(base+"{\"path\":\"elsewhere\",\"kind\":\"file\",\"size\":99}\n", "dir") != first {
		t.Fatal("unrelated path invalidated download consent")
	}
}

func TestAnchorBatchRefusesStaleRemoteSizeAndInvalidUSN(t *testing.T) {
	manifest := batchManifest(t, "{\"path\":\"a\",\"kind\":\"file\",\"size\":11}\n")
	for _, usn := range []string{"42", "0", "invalid"} {
		_, err := anchorBatchPlan(t.Context(), manifest, ".", "download", "binding", func([]string) ([]cfapiAnswer, error) {
			s := batchState("a", false)
			s.USN = usn
			return []cfapiAnswer{s}, nil
		})
		if err == nil {
			t.Fatalf("accepted stale size or invalid USN: %s", usn)
		}
	}
}

func TestAnchorBatchStopsAndReportsPartialWithoutSkipping(t *testing.T) {
	selection := anchorBatchSelection{files: []anchorBatchFile{{rel: "a"}, {rel: "b"}, {rel: "c"}}}
	var called []string
	failure := errors.New("changed file")
	n, err := runAnchorBatch(t.Context(), selection, func(_ context.Context, f anchorBatchFile) error {
		called = append(called, f.rel)
		if f.rel == "b" {
			return failure
		}
		return nil
	})
	if n != 1 || !errors.Is(err, failure) || strings.Join(called, ",") != "a,b" {
		t.Fatal(n, called, err)
	}
}

func TestAnchorExplicitApprovalDoesNotUnthrottleOtherPathsOrPrograms(t *testing.T) {
	guard := newAnchorGuard(talk.With("batch-test"))
	guard.limit = 1
	now := time.Now()
	if err := guard.allow("helper.exe", "first", now); err != nil {
		t.Fatal(err)
	}
	if err := guard.allow("helper.exe", "second", now); err == nil {
		t.Fatal("bulk guard not active")
	}
	withdraw := guard.authorize("helper.exe", "approved")
	if err := guard.allow("helper.exe", "approved", now); err != nil {
		t.Fatal(err)
	}
	if err := guard.allow("helper.exe", "other", now); err == nil {
		t.Fatal("other path bypassed guard")
	}
	guard.refused["scanner.exe"] = true
	if err := guard.allow("scanner.exe", "approved", now); err == nil {
		t.Fatal("other program bypassed guard")
	}
	withdraw()
	withdraw()
	if err := guard.allow("helper.exe", "approved", now); err == nil || len(guard.explicit) != 0 {
		t.Fatal("approval survived completion")
	}
}

type anchorPinnedFileProbe struct {
	client.Client
	headReads int
	revision  int64
	size      int64
}

func (p *anchorPinnedFileProbe) Revision(context.Context, string) (int64, error) {
	p.headReads++
	return 99, nil
}

func (p *anchorPinnedFileProbe) HistoryFetchFile(_ context.Context, _ string, revision int64, dest string) (int64, error) {
	p.revision = revision
	return p.size, os.WriteFile(dest, []byte("fixture"), 0600)
}

func TestAnchorBatchMaterializesConfirmedRevisionNotLaterHEAD(t *testing.T) {
	probe := &anchorPinnedFileProbe{size: 7}
	m := &anchorManager{newSVN: func(string) (client.Client, error) { return probe, nil }}
	record := localrepo.Record{ServerID: "server", RepoURL: "svn://fixture/repo", LocalPath: t.TempDir()}
	dest, revision, err := m.materializeAt(t.Context(), record, "file.txt", 12, 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(dest)) })
	if revision != 12 || probe.revision != 12 || probe.headReads != 0 {
		t.Fatal("confirmed revision replaced by HEAD", revision, probe)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "fixture" {
		t.Fatal(string(got), err)
	}
	if _, _, err := m.materializeAt(t.Context(), record, "file.txt", 12, 8); err == nil {
		t.Fatal("download differed from confirmed size")
	}
}
