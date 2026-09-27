package mobileclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	v1 "filees/pkg/mobile/v1"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

type dropTreeAndStatus struct{ Transport }

func (t dropTreeAndStatus) Do(ctx context.Context, req v1.Request, body []byte) (v1.Response, []byte, error) {
	if req.Operation == v1.OpOperationStatus {
		return v1.Response{}, nil, errors.New("status lost too")
	}
	resp, b, err := t.Transport.Do(ctx, req, body)
	if req.Operation == v1.OpUploadTree && err == nil {
		return v1.Response{}, nil, errors.New("ACK lost")
	}
	return resp, b, err
}
func TestDurableTreeAfterRestartDoesNotOverwriteNewerCommit(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	c := newClient(t, repo, "rw")
	transport := c.Transport
	c.Transport = dropTreeAndStatus{transport}
	body := packTreeForTest(t, map[string][]byte{"note.txt": []byte("A")})
	if err := c.UploadTree(context.Background(), "repo-1", "mobile-uploads", 1, body); err == nil {
		t.Fatal("lost result reported as success")
	}
	queued, err := c.Store.ListUploads("repo-1")
	if err != nil || len(queued) != 1 {
		t.Fatalf("queue %v %v", queued, err)
	}
	firstID := queued[0].ID
	other := newClient(t, repo, "rw")
	if err := other.UploadTree(context.Background(), "repo-1", "mobile-uploads", 1, packTreeForTest(t, map[string][]byte{"note.txt": []byte("B")})); err != nil {
		t.Fatal(err)
	}
	restarted := Client{Transport: transport, Store: Store{Root: c.Store.Root}}
	if err := restarted.UploadTree(context.Background(), "repo-1", "mobile-uploads", 1, body); err != nil {
		t.Fatal(err)
	}
	queued, err = restarted.Store.ListUploads("repo-1")
	if err != nil || len(queued) != 1 || queued[0].ID != firstID || queued[0].State != UploadCommitted {
		t.Fatalf("queue %v %v", queued, err)
	}
	data, err := other.Read(context.Background(), "repo-1", "mobile-uploads/note.txt")
	if err != nil || string(data) != "B" {
		t.Fatalf("newer data overwritten %q %v", data, err)
	}
}

type zeroStream struct{}

func (zeroStream) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type streamingReceipt struct{ size int64 }

func (*streamingReceipt) Do(context.Context, v1.Request, []byte) (v1.Response, []byte, error) {
	panic("buffered transport used")
}
func (s *streamingReceipt) DoStream(ctx context.Context, req v1.Request, r io.Reader) (v1.Response, []byte, error) {
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return v1.Response{}, nil, err
	}
	s.size = n
	resp, err := v1.NewSuccess(req.RequestID, req.Operation, v1.UploadTreeResult{FileCount: 1, Size: n, Revision: 2})
	return resp, nil, err
}
func TestLargeQueuedPayloadUsesBoundedMemory(t *testing.T) {
	const size = int64(64 << 20)
	store := Store{Root: t.TempDir()}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	item, err := store.EnqueueReader(context.Background(), "repo-1", "mobile-uploads", "tree.zip", "application/zip", v1.OpUploadTree, 1, nil, io.LimitReader(zeroStream{}, size))
	if err != nil {
		t.Fatal(err)
	}
	transport := &streamingReceipt{}
	c := Client{Transport: transport, Store: store}
	result, err := c.SendUpload(context.Background(), "repo-1", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if result.State != UploadCommitted || transport.size != size {
		t.Fatalf("result %+v bytes %d", result, transport.size)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("allocated %d bytes for streaming %d", allocated, size)
	}
}

type functionTransport func(context.Context, v1.Request, []byte) (v1.Response, []byte, error)

func (f functionTransport) Do(ctx context.Context, r v1.Request, b []byte) (v1.Response, []byte, error) {
	return f(ctx, r, b)
}

func TestCancelledAttemptDoesNotStarveUntouchedQueue(t *testing.T) {
	store := Store{Root: t.TempDir()}
	first, err := store.EnqueueUpload("repo-1", "mobile-uploads", "first.txt", "", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueUpload("repo-1", "mobile-uploads", "second.txt", "", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := Client{Store: store, Transport: functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
		if req.RequestID != first.ID {
			t.Fatal("first attempt order")
		}
		cancel()
		return v1.Response{}, nil, context.Canceled
	})}
	if _, err := c.DrainPending(ctx, "repo-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	var order []string
	c.Transport = functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
		if req.Operation == v1.OpOperationStatus {
			r, e := v1.NewSuccess(req.RequestID, req.Operation, v1.OperationStatusResult{State: v1.OpStateUnknown})
			return r, nil, e
		}
		order = append(order, req.RequestID)
		var p v1.UploadObjectPayload
		_ = json.Unmarshal(req.Payload, &p)
		resp, err := v1.NewSuccess(req.RequestID, req.Operation, v1.UploadObjectResult{Outcome: v1.OutcomeCommitted, Revision: 2, FinalPath: p.ParentPath + "/" + p.Filename})
		return resp, nil, err
	})
	results, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != second.ID || order[1] != first.ID {
		t.Fatalf("retry order %v", order)
	}
	for _, result := range results {
		if result.State != UploadCommitted {
			t.Fatalf("result %+v", result)
		}
	}
}

func TestPermanentTreeFailureDoesNotStopOtherIntents(t *testing.T) {
	store := Store{Root: t.TempDir()}
	tree, err := store.EnqueueReader(context.Background(), "repo-1", "mobile-uploads", "tree.zip", "", v1.OpUploadTree, 1, nil, bytes.NewReader([]byte("invalid")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.EnqueueUpload("repo-1", "mobile-uploads", "valid.txt", "", []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	c := Client{Store: store, Transport: functionTransport(func(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
		if req.RequestID == tree.ID {
			r, e := v1.NewError(req.RequestID, req.Operation, v1.ErrorBody{Code: "tree.not_pack", Message: "invalid pack"})
			return r, nil, e
		}
		r, e := v1.NewSuccess(req.RequestID, req.Operation, v1.UploadObjectResult{Outcome: v1.OutcomeCommitted, Revision: 2, FinalPath: "mobile-uploads/valid.txt"})
		return r, nil, e
	})}
	results, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != UploadParked || results[1].State != UploadCommitted {
		t.Fatalf("results %+v", results)
	}
	retry, err := store.RetryUploadAs("repo-1", tree.ID, "")
	if err != nil || retry.ID != tree.ID {
		t.Fatalf("tree retry changed identity: %+v %v", retry, err)
	}
}

func TestTreeStatusHasIndependentDeadlineButHonorsCancellation(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			item, err := store.EnqueueReader(context.Background(), "repo-1", "mobile-uploads", "tree.zip", "", v1.OpUploadTree, 1, nil, bytes.NewReader([]byte("payload")))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			probes := 0
			c := Client{Store: store, Transport: functionTransport(func(callCtx context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
				if req.Operation == v1.OpUploadTree {
					if explicit {
						cancel()
					}
					<-callCtx.Done()
					return v1.Response{}, nil, callCtx.Err()
				}
				probes++
				if callCtx.Err() != nil {
					t.Fatal("probe inherited expired attempt")
				}
				r, e := v1.NewSuccess(req.RequestID, req.Operation, v1.OperationStatusResult{State: v1.OpStateCommitted, Revision: 2})
				return r, nil, e
			})}
			result, err := c.SendUpload(ctx, "repo-1", item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if explicit {
				if probes != 0 || result.State != UploadPendingCreate {
					t.Fatalf("cancel %+v probes %d", result, probes)
				}
			} else if probes != 1 || result.State != UploadCommitted {
				t.Fatalf("deadline %+v probes %d", result, probes)
			}
		})
	}
}

func TestObjectLostAckResolvesWithoutSecondUpload(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")
	transport := c.Transport
	calls := 0
	c.Transport = functionTransport(func(ctx context.Context, req v1.Request, body []byte) (v1.Response, []byte, error) {
		resp, payload, err := transport.Do(ctx, req, body)
		if req.Operation == v1.OpUploadObject {
			calls++
			if err == nil && resp.Status == v1.StatusOK {
				return v1.Response{}, nil, errors.New("object ACK lost")
			}
		}
		return resp, payload, err
	})
	item, err := c.Store.EnqueueUpload("repo-1", "mobile-uploads", "one.txt", "", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.SendUpload(context.Background(), "repo-1", item.ID)
	if err != nil || result.State != UploadCommitted || calls != 1 {
		t.Fatalf("result %+v err %v uploads %d", result, err, calls)
	}
}

func TestStatusAlreadyInFlightStopsWithCaller(t *testing.T) {
	caller, stop := context.WithCancel(context.Background())
	defer stop()
	started := make(chan struct{})
	finished := make(chan struct{})
	c := Client{Transport: functionTransport(func(ctx context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
		close(started)
		<-ctx.Done()
		return v1.Response{}, nil, ctx.Err()
	})}
	go func() { defer close(finished); c.recoverReceipt(caller, PendingUpload{ID: uuid.NewString()}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("status did not start")
	}
	stop()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("status ignored explicit stop")
	}
}

// Inspect the transport deadline without moving a gigabyte through a fixture.
// The artificial payload is never accepted as a valid server upload here.
type budgetTransport struct {
	t        *testing.T
	min, max time.Duration
	calls    int
}

func (b *budgetTransport) Do(ctx context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
	if req.Operation == v1.OpUploadObject || req.Operation == v1.OpUploadTree {
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining < b.min || remaining > b.max {
			b.t.Fatalf("upload budget %v outside [%v,%v]", remaining, b.min, b.max)
		}
		b.calls++
	}
	return v1.Response{}, nil, errors.New("offline test endpoint")
}
func (b *budgetTransport) DoStream(ctx context.Context, req v1.Request, _ io.Reader) (v1.Response, []byte, error) {
	return b.Do(ctx, req, nil)
}
func TestCapturedVideoBudgetAndCallerDeadline(t *testing.T) {
	const videoBytes = int64(1116709332) // measured phone file; ~30 min at 5 Mbit/s
	for _, op := range []v1.Operation{v1.OpUploadObject, v1.OpUploadTree} {
		for _, drain := range []bool{false, true} {
			for _, shortCaller := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/drain=%v/caller=%v", op, drain, shortCaller), func(t *testing.T) {
					store := Store{Root: t.TempDir()}
					item, err := store.EnqueueReader(context.Background(), "repo", "mobile-uploads", "video.mp4", "video/mp4", op, 1, nil, bytes.NewReader([]byte("fixture")))
					if err != nil {
						t.Fatal(err)
					}
					item.Size = videoBytes
					if err := os.Truncate(store.uploadPayloadPath(item.RepoID, item.ID), videoBytes); err != nil {
						t.Fatal(err)
					}
					if err := store.recordUploadOutcome(item); err != nil {
						t.Fatal(err)
					}
					transport := &budgetTransport{t: t, min: 30 * time.Minute, max: 3 * time.Hour}
					ctx := context.Background()
					if shortCaller {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, time.Second)
						defer cancel()
						transport.min = 0
						transport.max = time.Second
					}
					client := Client{Store: store, Transport: transport}
					if drain {
						_, err = client.DrainPending(ctx, "repo")
					} else {
						_, err = client.SendUpload(ctx, "repo", item.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					if transport.calls != 1 {
						t.Fatalf("upload attempts=%d", transport.calls)
					}
					queued, err := store.ListUploads("repo")
					if err != nil || len(queued) != 1 || queued[0].ID != item.ID || queued[0].State != UploadPendingCreate {
						t.Fatalf("lost pending intent: %v %v", queued, err)
					}
				})
			}
		}
	}
	if uploadAttemptTimeout(PendingUpload{Size: 1<<63 - 1, Operation: v1.OpUploadTree}) > 3*time.Hour {
		t.Fatal("unbounded duration")
	}
}

// A full server must not discard or replace an intent; retry obtains a real
// receipt for exactly the same durable bytes and ID after capacity is restored.
func TestServerStorageFullRetainsTreeUntilRetry(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")
	normal := c.Transport
	c.Transport = fullServerTransport{}
	body := packTreeForTest(t, map[string][]byte{"note.txt": []byte("retained")})
	if err := c.UploadTree(context.Background(), "repo-1", "mobile-uploads", 1, body); err == nil {
		t.Fatal("false success")
	}
	queued, err := c.Store.ListUploads("repo-1")
	if err != nil || len(queued) != 1 || queued[0].State != UploadPendingCreate {
		t.Fatalf("queue %+v %v", queued, err)
	}
	id := queued[0].ID
	restarted := Client{Store: Store{Root: c.Store.Root}, Transport: normal}
	got, err := restarted.SendUpload(context.Background(), "repo-1", id)
	if err != nil || got.State != UploadCommitted || got.ID != id {
		t.Fatalf("retry %+v %v", got, err)
	}
}

type fullServerTransport struct{}

func (fullServerTransport) Do(_ context.Context, req v1.Request, _ []byte) (v1.Response, []byte, error) {
	resp := v1.Response{RequestID: req.RequestID, Operation: req.Operation, Status: v1.StatusError, Error: &v1.ErrorBody{Code: "storage.full", Message: "server storage is full; upload retained for retry"}}
	return resp, nil, nil
}
