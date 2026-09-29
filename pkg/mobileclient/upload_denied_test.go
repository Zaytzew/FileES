package mobileclient

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

func TestObjectAccessDeniedPersistsUntilDecision(t *testing.T) {
	for _, decision := range []string{"discard", "retry"} {
		t.Run(decision, func(t *testing.T) {
			calls := 0
			transport := functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
				calls++
				resp, err := v1.NewError(req.RequestID, req.Operation, v1.ErrorBody{Code: "access.denied", Message: "operation failed"})
				return resp, nil, err
			})
			c := Client{Store: Store{Root: t.TempDir()}, Transport: transport}
			item, err := c.Store.EnqueueUpload("repo-1", "mobile-uploads", "a.jpg", "image/jpeg", []byte("keep me"))
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				// Reopen persisted state, as a later background tick/process would.
				c = Client{Store: Store{Root: c.Store.Root}, Transport: transport}
				got, err := c.SendUpload(context.Background(), item.RepoID, item.ID)
				if err != nil || got.State != UploadParked || !strings.Contains(got.LastError, "access.denied") {
					t.Fatalf("send = %+v, %v", got, err)
				}
				items, err := c.DrainPending(context.Background(), item.RepoID)
				if err != nil || len(items) != 1 || items[0].State != UploadParked {
					t.Fatalf("drain = %+v, %v", items, err)
				}
			}
			if calls != 1 {
				t.Fatalf("parked item caused %d network calls, want 1", calls)
			}
			body, err := c.Store.loadUploadPayload(item.RepoID, item.ID)
			if err != nil || string(body) != "keep me" {
				t.Fatalf("payload = %q, %v", body, err)
			}
			if decision == "retry" {
				next, err := c.Store.RetryUploadAs(item.RepoID, item.ID, "")
				if err != nil || next.ID == item.ID || next.State != UploadPendingCreate {
					t.Fatalf("explicit retry = %+v, %v", next, err)
				}
				body, err := c.Store.loadUploadPayload(next.RepoID, next.ID)
				if err != nil || string(body) != "keep me" {
					t.Fatalf("retry payload = %q, %v", body, err)
				}
			} else if err := c.Store.DiscardUpload(item.RepoID, item.ID); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{c.Store.uploadMetaPath(item.RepoID, item.ID), c.Store.uploadPayloadPath(item.RepoID, item.ID)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("old candidate remains at %s: %v", path, err)
				}
			}
		})
	}
}

func TestObjectNonAuthoritativeFailureRemainsRetryable(t *testing.T) {
	for _, mode := range []string{"worker.failed", "wrong-id", "wrong-operation", "missing-error", "network"} {
		t.Run(mode, func(t *testing.T) {
			uploads := 0
			transport := functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
				if req.Operation == v1.OpOperationStatus {
					resp, err := v1.NewSuccess(req.RequestID, req.Operation, v1.OperationStatusResult{State: v1.OpStateUnknown})
					return resp, nil, err
				}
				uploads++
				resp, err := v1.NewError(req.RequestID, req.Operation, v1.ErrorBody{Code: "access.denied", Message: "operation failed"})
				switch mode {
				case "worker.failed":
					resp.Error.Code = mode
				case "wrong-id":
					resp.RequestID = "another-request"
				case "wrong-operation":
					resp.Operation = v1.OpUploadTree
				case "missing-error":
					resp.Error = nil
				case "network":
					return v1.Response{}, nil, errors.New("connection lost")
				}
				return resp, nil, err
			})
			c := Client{Store: Store{Root: t.TempDir()}, Transport: transport}
			item, err := c.Store.EnqueueUpload("repo-1", "mobile-uploads", "a.jpg", "image/jpeg", []byte("a"))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				got, err := c.SendUpload(context.Background(), item.RepoID, item.ID)
				if err != nil || got.State != UploadPendingCreate || got.LastError == "" {
					t.Fatalf("send = %+v, %v; want pending with reason", got, err)
				}
			}
			if uploads != 2 {
				t.Fatalf("attempts = %d, want 2", uploads)
			}
		})
	}
}
