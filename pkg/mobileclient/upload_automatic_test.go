package mobileclient

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

// A watched folder's upload says so in the request, for both verbs, and a
// chosen upload does not (implementation notes (not distributed)). The flag is
// stored at enqueue, so a later process sending the queue repeats it.
func TestAutomaticFlagTravelsFromTheQueueToTheRequest(t *testing.T) {
	seen := map[string]bool{}
	transport := functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
		var payload struct {
			Filename  string `json:"filename"`
			Automatic bool   `json:"automatic"`
		}
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		seen[string(req.Operation)+":"+payload.Filename] = payload.Automatic
		resp, err := v1.NewError(req.RequestID, req.Operation, v1.ErrorBody{Code: "worker.failed", Message: "operation failed"})
		return resp, nil, err
	})
	root := t.TempDir()
	store := Store{Root: root}
	ctx := context.Background()
	watched, err := store.EnqueueAutomaticReader(ctx, "repo-1", "mobile-uploads", "watched.jpg", "image/jpeg", v1.OpUploadObject, 1, []string{"src-1"}, bytes.NewReader([]byte("a")))
	if err != nil || !watched.Automatic {
		t.Fatalf("enqueue automatic = %+v, %v", watched, err)
	}
	chosen, err := store.EnqueueReader(ctx, "repo-1", "mobile-uploads", "chosen.jpg", "image/jpeg", v1.OpUploadObject, 1, nil, bytes.NewReader([]byte("b")))
	if err != nil || chosen.Automatic {
		t.Fatalf("enqueue chosen = %+v, %v", chosen, err)
	}
	tree, err := store.EnqueueAutomaticReader(ctx, "repo-1", "mobile-uploads", "tree.zip", "application/zip", v1.OpUploadTree, 1, []string{"src-2"}, bytes.NewReader([]byte("zip")))
	if err != nil || !tree.Automatic {
		t.Fatalf("enqueue automatic tree = %+v, %v", tree, err)
	}
	// A fresh process, as the background tick is.
	c := Client{Store: Store{Root: root}, Transport: transport}
	for _, item := range []PendingUpload{watched, chosen, tree} {
		if _, err := c.SendUpload(ctx, item.RepoID, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !seen["UPLOAD_OBJECT:watched.jpg"] || seen["UPLOAD_OBJECT:chosen.jpg"] || !seen["UPLOAD_TREE:"] {
		t.Fatalf("automatic flags on the wire: %+v", seen)
	}
}

// The server's refusal for a repository shared with another realm parks the
// item with its reason; it is not retried on every tick.
func TestAutomaticSharedRefusalParksTheUpload(t *testing.T) {
	for _, operation := range []v1.Operation{v1.OpUploadObject, v1.OpUploadTree} {
		t.Run(string(operation), func(t *testing.T) {
			calls := 0
			transport := functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
				calls++
				resp, err := v1.NewError(req.RequestID, req.Operation, v1.ErrorBody{Code: v1.ErrCodeAutomaticShared, Message: "automatic upload refused"})
				return resp, nil, err
			})
			c := Client{Store: Store{Root: t.TempDir()}, Transport: transport}
			item, err := c.Store.EnqueueAutomaticReader(context.Background(), "repo-1", "mobile-uploads", "x.bin", "application/octet-stream", operation, 1, []string{"src"}, bytes.NewReader([]byte("x")))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, err := c.SendUpload(context.Background(), item.RepoID, item.ID)
				if err != nil || got.State != UploadParked || !strings.Contains(got.LastError, v1.ErrCodeAutomaticShared) {
					t.Fatalf("send = %+v, %v", got, err)
				}
			}
			if calls != 1 {
				t.Fatalf("a refused automatic upload caused %d network calls, want 1", calls)
			}
		})
	}
}
