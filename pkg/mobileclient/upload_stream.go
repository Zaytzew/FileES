package mobileclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"filees/internal/fsdurable"
	v1 "filees/pkg/mobile/v1"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"
)

// Payload limits match the worker. ZIP framing has a separate bounded allowance.
const MaxUploadBytes = v1.MaxUploadBytes
const MaxTreeWireBytes = v1.MaxTreeWireBytes
const MaxTreeFiles = v1.MaxTreeFiles

var queueLocks sync.Map

func (s Store) lockQueue(ctx context.Context) (func(), error) {
	key, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, err
	}
	candidate := make(chan struct{}, 1)
	value, _ := queueLocks.LoadOrStore(key, candidate)
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// EnqueueReader persists payload and source identities before any network I/O.
// Tree and object intents share the same durable queue and lifecycle.
func (s Store) EnqueueReader(ctx context.Context, repoID, parent, name, contentType string, operation v1.Operation, count int, sources []string, input io.Reader) (PendingUpload, error) {
	unlock, err := s.lockQueue(ctx)
	if err != nil {
		return PendingUpload{}, err
	}
	defer unlock()
	return s.enqueueReaderLocked(ctx, repoID, parent, name, contentType, operation, count, sources, input)
}

func (s Store) enqueueReaderLocked(ctx context.Context, repoID, parent, name, contentType string, operation v1.Operation, count int, sources []string, input io.Reader) (PendingUpload, error) {
	if repoID == "" || filepath.Base(repoID) != repoID || repoID == "." || repoID == ".." {
		return PendingUpload{}, errors.New("invalid upload repo_id")
	}
	if operation != v1.OpUploadObject && operation != v1.OpUploadTree {
		return PendingUpload{}, errors.New("invalid upload operation")
	}
	limit := MaxUploadBytes
	if operation == v1.OpUploadTree {
		limit = MaxTreeWireBytes
		if count < 1 || count > MaxTreeFiles {
			return PendingUpload{}, errors.New("tree file count exceeds limit")
		}
	}
	if name == "" {
		return PendingUpload{}, errors.New("upload filename is required")
	}
	dir := s.uploadDir(repoID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return PendingUpload{}, err
	}
	f, err := os.CreateTemp(dir, ".spool-*")
	if err != nil {
		return PendingUpload{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, input}, limit+1))
	if err != nil {
		return PendingUpload{}, err
	}
	if size > limit {
		return PendingUpload{}, errors.New("upload exceeds size limit")
	}
	item := PendingUpload{ID: uuid.NewString(), RepoID: repoID, ParentPath: parent, Filename: name, ContentType: contentType, Operation: operation, FileCount: count, Sources: sources, Size: size, Sha256: hex.EncodeToString(h.Sum(nil)), State: UploadPendingCreate, EnqueuedAt: time.Now().UTC()}
	// Re-entry after an uncertain result reuses its persisted identity. A
	// completed operation is never silently treated as a new user's intent.
	queued, err := s.ListUploads(repoID)
	if err != nil {
		return PendingUpload{}, err
	}
	for _, old := range queued {
		if !old.State.terminal() && old.Operation == operation && old.ParentPath == parent && old.Filename == name && old.Sha256 == item.Sha256 && old.FileCount == count {
			return old, nil
		}
	}
	if err := f.Sync(); err != nil {
		return PendingUpload{}, err
	}
	if err := f.Close(); err != nil {
		return PendingUpload{}, err
	}
	if err := os.Rename(f.Name(), s.uploadPayloadPath(repoID, item.ID)); err != nil {
		return PendingUpload{}, err
	}
	if err := fsdurable.SyncDir(dir); err != nil {
		return PendingUpload{}, err
	}
	if err := atomicWriteJSON(s.uploadMetaPath(repoID, item.ID), &item); err != nil {
		return PendingUpload{}, err
	}
	return item, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

type StreamTransport interface {
	DoStream(context.Context, v1.Request, io.Reader) (v1.Response, []byte, error)
}

func (c Client) sendPayload(ctx context.Context, req v1.Request, item PendingUpload) (v1.Response, []byte, error) {
	f, err := os.Open(c.Store.uploadPayloadPath(item.RepoID, item.ID))
	if err != nil {
		return v1.Response{}, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return v1.Response{}, nil, err
	}
	if info.Size() != item.Size {
		return v1.Response{}, nil, errors.New("durable upload payload size changed")
	}
	if transport, ok := c.Transport.(StreamTransport); ok {
		return transport.DoStream(ctx, req, contextReader{ctx, f})
	}
	// Compatibility for small in-process transports. Never buffer a large
	// transfer merely because a transport forgot to implement streaming.
	if item.Size > 4<<20 {
		return v1.Response{}, nil, errors.New("transport does not support streaming")
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return v1.Response{}, nil, err
	}
	return c.Transport.Do(ctx, req, body)
}

// SendUpload sends one concrete ID. It never derives success from another
// queue entry. The store lock also excludes background/foreground drains.
func (c Client) SendUpload(ctx context.Context, repoID, id string) (PendingUpload, error) {
	unlock, err := c.Store.lockQueue(ctx)
	if err != nil {
		return PendingUpload{}, err
	}
	defer unlock()
	item, err := c.Store.loadUploadMeta(repoID, id)
	if err != nil {
		return item, err
	}
	if item.State.terminal() {
		return item, nil
	}
	attempt, cancel := context.WithTimeout(ctx, uploadAttemptTimeout(item))
	defer cancel()
	return c.sendOne(attempt, ctx, item)
}

// Allow large captured videos to finish on mobile uplinks. The transfer
// allowance assumes 256 KiB/s, in addition to the normal processing budget.
// Cancellation, SSH liveness and the caller deadline still take precedence.
func uploadAttemptTimeout(item PendingUpload) time.Duration {
	base := sendOneTimeout
	if item.Operation == v1.OpUploadTree {
		base = 10 * time.Minute
	}
	const rate = int64(256 * 1024)
	const maximum = 3 * time.Hour
	if item.Size <= 0 {
		return base
	}
	seconds := item.Size/rate + 1
	if seconds >= int64((maximum-base)/time.Second) {
		return maximum
	}
	return base + time.Duration(seconds)*time.Second
}

func (c Client) pendingError(item PendingUpload, err error) (PendingUpload, error) {
	item.State, item.LastError = UploadPendingCreate, err.Error()
	return item, c.Store.recordUploadOutcome(item)
}

func (c Client) sendTree(ctx, caller context.Context, item PendingUpload) (PendingUpload, error) {
	req, err := v1.NewRequest(item.ID, v1.OpUploadTree, v1.UploadTreePayload{RepoID: item.RepoID, ParentPath: item.ParentPath, FileCount: item.FileCount, Size: item.Size, Sha256: item.Sha256})
	if err != nil {
		item.State, item.LastError = UploadParked, err.Error()
		return item, c.Store.recordUploadOutcome(item)
	}
	item.State, item.LastAttemptAt = UploadUploading, time.Now().UTC()
	if err := c.Store.recordUploadOutcome(item); err != nil {
		return item, err
	}
	resp, _, err := c.sendPayload(ctx, req, item)
	if err != nil {
		if resolved, ok := c.recoverReceipt(caller, item); ok {
			return resolved, c.Store.recordUploadOutcome(resolved)
		}
		return c.pendingError(item, fmt.Errorf("UPLOAD_TREE: %w", err))
	}
	if resp.Status != v1.StatusOK {
		// Generic worker failure can mean a committed transaction with a lost
		// ledger write. Preserve its ID and bytes. Deterministic validation
		// failures are parked, so poison packs cannot loop forever.
		if resp.Error != nil && (resp.Error.Code == "tree.incomplete" || resp.Error.Code == "tree.limit" || resp.Error.Code == "access.denied" || resp.Error.Code == "tree.not_pack") {
			item.State, item.LastError = UploadParked, respError(resp).Error()
			return item, c.Store.recordUploadOutcome(item)
		}
		return c.pendingError(item, respError(resp))
	}
	var receipt v1.UploadTreeResult
	if err := decodeTreeReceipt(resp, item, &receipt); err != nil {
		return c.pendingError(item, err)
	}
	item.State, item.Revision, item.FinalPath, item.LastError = UploadCommitted, receipt.Revision, item.ParentPath, ""
	return item, c.Store.recordUploadOutcome(item)
}

// Status uses its own small deadline after a timed-out attempt. Explicit
// worker/user cancellation starts no new I/O. It also avoids resending a large
// object merely to learn that its earlier commit already succeeded.
func (c Client) recoverReceipt(ctx context.Context, item PendingUpload) (PendingUpload, bool) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return item, false
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	// Ignore the attempt/deadline, but retain the caller's explicit stop signal
	// while status I/O is already running (not only before it starts).
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel()
		}
	})
	defer stop()
	result, err := c.operationReceipt(probe, item.ID)
	if err != nil || result.State != v1.OpStateCommitted || result.Revision < 1 {
		return item, false
	}
	item.State, item.Revision, item.LastError = UploadCommitted, result.Revision, ""
	item.FinalPath = item.ParentPath
	if item.Operation != v1.OpUploadTree {
		item.FinalPath = path.Join(item.ParentPath, item.Filename)
	}
	return item, true
}
