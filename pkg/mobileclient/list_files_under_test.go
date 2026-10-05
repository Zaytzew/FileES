package mobileclient

import (
	"context"
	"fmt"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

func TestListFilesUnderDoesNotTruncateAt200(t *testing.T) {
	entries := make([]v1.ManifestEntry, 201)
	for i := range entries {
		entries[i] = v1.ManifestEntry{Path: fmt.Sprintf("folder/%03d.txt", i), Kind: v1.KindFile, Size: 1}
	}
	manifest := v1.Manifest{Schema: v1.ManifestSchema, RepoID: "repo-1", ViewGeneration: 1,
		RepoRevision: 1, Complete: true, Entries: entries}
	client := Client{Store: Store{Root: t.TempDir()}, Transport: functionTransport(
		func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
			if req.Operation != v1.OpListDirectory {
				t.Fatalf("unexpected operation %s", req.Operation)
			}
			resp, err := v1.NewSuccess(req.RequestID, req.Operation, manifest)
			return resp, nil, err
		},
	)}
	got, err := client.ListFilesUnder(context.Background(), "repo-1", "folder", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(entries) {
		t.Fatalf("listed %d of %d files", len(got), len(entries))
	}
	if got[200].Path != "folder/200.txt" {
		t.Fatalf("last path %q", got[200].Path)
	}
}
