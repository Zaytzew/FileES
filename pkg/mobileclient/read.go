package mobileclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

// ReadTransport streams a provisional response body. Real mobile SSH implements it.
type ReadTransport interface {
	DoStreamTo(context.Context, v1.Request, io.Writer) (v1.Response, error)
}

// ReadTo verifies the response identity, path, byte count and SHA-256. The sink
// may contain partial/unverified bytes on failure; callers must stage privately.
func (c Client) ReadTo(ctx context.Context, repoID, path string, sink io.Writer) (v1.ReadObjectResult, error) {
	var result v1.ReadObjectResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if sink == nil {
		return result, errors.New("read sink is required")
	}
	req, err := v1.NewRequest(uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: repoID, Path: path})
	if err != nil {
		return result, err
	}
	h := sha256.New()
	counter := &readCounter{w: io.MultiWriter(sink, h)}
	var resp v1.Response
	if transport, ok := c.Transport.(ReadTransport); ok {
		resp, err = transport.DoStreamTo(ctx, req, counter)
	} else {
		// Small in-process transports only; production never enters this path.
		var payload []byte
		resp, payload, err = c.Transport.Do(ctx, req, nil)
		if err == nil && resp.Status == v1.StatusOK {
			if len(payload) > 4<<20 {
				return result, errors.New("transport does not support streamed download")
			}
			_, err = counter.Write(payload)
		}
	}
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if resp.RequestID != req.RequestID || resp.Operation != req.Operation {
		return result, errors.New("read response identity mismatch")
	}
	if resp.Status != v1.StatusOK {
		return result, respError(resp)
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return result, err
	}
	if result.Path != path || result.Size != counter.n || !strings.EqualFold(result.Sha256, hex.EncodeToString(h.Sum(nil))) {
		return result, errors.New("read object path, size or sha256 mismatch")
	}
	return result, nil
}

type readCounter struct {
	w io.Writer
	n int64
}

func (w *readCounter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// The legacy []byte API is for small callers; large objects must use ReadTo.
type smallReadWriter struct {
	w io.Writer
	n int64
}

func (w *smallReadWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > (4<<20)-w.n {
		return 0, errors.New("buffered read exceeds 4 MiB; use ReadTo")
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}
