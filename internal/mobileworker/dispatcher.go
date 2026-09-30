package mobileworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	v1 "filees/pkg/mobile/v1"
)

// Dispatcher serves exactly one framed mobile operation per invocation: it reads
// a request frame from in, routes it to the read-only browser or the append
// worker, and writes one response frame to out. ClientID is bound from the
// authenticated session (the forced command), never from the payload. It runs no
// listener and holds no state between calls.
type Dispatcher struct {
	Browser   Browser
	Appender  Appender
	Joiner    JoinRequester
	ClientID  string
	readSpace func(string, int64) error // nil uses the actual spool filesystem
}

// JoinRequester accepts an authenticated demand for a desktop join ticket.
// The worker must not return the invitation blob to the phone.
type JoinRequester interface {
	RequestDesktopJoin(ctx context.Context, clientID, email string) error
}

// Serve processes one operation. A malformed frame or request returns a Go error
// without a response frame (the session simply closes); a well-formed request
// always produces a well-formed response frame, success or domain error.
func (d Dispatcher) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	br := bufio.NewReader(in)
	header, err := v1.ReadHeader(br, v1.RequestMagic, v1.MaxHeaderBytes)
	if err != nil {
		return err
	}
	req, err := v1.ParseRequest(header)
	if err != nil {
		return err
	}

	switch req.Operation {
	case v1.OpRefreshManifest:
		var p v1.RefreshManifestPayload
		_ = json.Unmarshal(req.Payload, &p)
		res, err := d.Browser.RefreshManifest(ctx, d.ClientID, p)
		if err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, res, nil)

	case v1.OpListRepositories:
		res, err := d.Browser.ListRepositories(ctx, d.ClientID)
		if err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, res, nil)

	case v1.OpListDirectory:
		var p v1.ListDirectoryPayload
		_ = json.Unmarshal(req.Payload, &p)
		res, err := d.Browser.ListDirectory(ctx, d.ClientID, p)
		if err != nil {
			return d.writeError(out, req, err)
		}
		entries := res.Entries
		res.Entries = nil
		raw, err := json.Marshal(entries)
		if err != nil {
			return d.writeError(out, req, err)
		}
		if raw == nil {
			raw = []byte("[]")
		}
		return d.writeOK(out, req, res, raw)

	case v1.OpReadObject:
		var p v1.ReadObjectPayload
		_ = json.Unmarshal(req.Payload, &p)
		// The header contains size/hash, so finish reading into a private
		// disk spool before sending it. Never hold repository content in RAM.
		spool, err := os.CreateTemp(d.Appender.SpoolDir, "filees-mobile-read-*")
		if err != nil {
			return d.writeError(out, req, err)
		}
		defer os.Remove(spool.Name())
		defer spool.Close()
		res, err := d.Browser.ReadObject(ctx, d.ClientID, p, readSpool{File: spool, checkSpace: d.readSpace})
		if err != nil {
			return d.writeError(out, req, err)
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return d.writeError(out, req, err)
		}
		if err := d.writeOK(out, req, res, nil); err != nil {
			return err
		}
		_, err = io.CopyN(out, spool, res.Size)
		return err

	case v1.OpUploadObject:
		var p v1.UploadObjectPayload
		_ = json.Unmarshal(req.Payload, &p)
		res, err := d.Appender.Upload(ctx, d.ClientID, req.RequestID, p, br)
		if err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, res, nil)

	case v1.OpUploadTree:
		var p v1.UploadTreePayload
		_ = json.Unmarshal(req.Payload, &p)
		res, err := d.Appender.UploadTree(ctx, d.ClientID, req.RequestID, p, br)
		if err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, res, nil)

	case v1.OpOperationStatus:
		var p v1.OperationStatusPayload
		_ = json.Unmarshal(req.Payload, &p)
		return d.writeOK(out, req, d.status(ctx, p.TargetRequestID), nil)

	case v1.OpListDrawers:
		res, err := d.Browser.ListDrawers(ctx, d.ClientID)
		if err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, res, nil)

	case v1.OpRequestDesktopJoin:
		if d.Joiner == nil {
			body := v1.ErrorBody{Code: "op.unsupported", Message: "operation not supported"}
			return d.writeErrorBody(out, req, body)
		}
		var p v1.RequestDesktopJoinPayload
		_ = json.Unmarshal(req.Payload, &p)
		if err := d.Joiner.RequestDesktopJoin(ctx, d.ClientID, p.Email); err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, v1.RequestDesktopJoinResult{}, nil)

	default:
		body := v1.ErrorBody{Code: "op.unsupported", Message: "operation not supported"}
		return d.writeErrorBody(out, req, body)
	}
}

func (d Dispatcher) status(ctx context.Context, targetRequestID string) v1.OperationStatusResult {
	rec, err := d.Appender.Ledger.Lookup(targetRequestID)
	if err != nil || rec == nil || rec.ClientID != d.ClientID {
		return v1.OperationStatusResult{State: v1.OpStateUnknown}
	}
	view, err := d.Appender.Authority.Resolve(ctx, d.ClientID, rec.RepoID)
	if err != nil {
		return v1.OperationStatusResult{State: v1.OpStateUnknown}
	}
	lock, err := d.Appender.Ledger.lockOperation(targetRequestID)
	if err == nil {
		defer lock.Close()
		rec, err = d.Appender.Ledger.Lookup(targetRequestID)
		if err != nil || rec == nil {
			return v1.OperationStatusResult{State: v1.OpStateUnknown}
		}
		_ = d.Appender.recover(ctx, view.RepoPath, rec)
	}
	return v1.OperationStatusResult{State: rec.State, Revision: rec.Revision}
}

func (d Dispatcher) writeOK(out io.Writer, req v1.Request, result any, payload []byte) error {
	resp, err := v1.NewSuccess(req.RequestID, req.Operation, result)
	if err != nil {
		return d.writeErrorBody(out, req, v1.ErrorBody{Code: "worker.failed", Message: "operation failed"})
	}
	err = writeResponse(out, resp, payload)
	if errors.Is(err, errFrameHeaderTooLarge) {
		return d.writeErrorBody(out, req, v1.ErrorBody{
			Code:    "manifest.too_large",
			Message: "repository listing does not fit in one mobile frame",
		})
	}
	return err
}

// writeError maps an internal error to a domain code, never leaking raw tool
// text to the phone. The generic "worker.failed" fallback is the one case
// where nothing in the mapped code/message names the real cause - so that
// case and storage exhaustion are also logged for an administrator (see
// Ledger.LogError) before the mask is applied. Every other branch already
// puts the real cause in msg, so a second copy in the log would be noise.
func (d Dispatcher) writeError(out io.Writer, req v1.Request, err error) error {
	code, msg := "worker.failed", "operation failed"
	if IsStorageFull(err) {
		code, msg = "storage.full", "server storage is full; upload retained for retry"
		if req.Operation == v1.OpReadObject {
			msg = "server storage is full; download could not be prepared"
		}
	}
	if errors.Is(err, ErrReadLimit) {
		code, msg = "download.limit", "download exceeds the server size limit"
	}
	if errors.Is(err, ErrReadStorage) {
		code, msg = "storage.unavailable", "server storage is unavailable; download could not be prepared"
	}
	if errors.Is(err, errUploadLimit) {
		code, msg = "tree.limit", "upload exceeds file count or size limit"
	}
	if errors.Is(err, errOperationUncertain) {
		code, msg = "operation.uncertain", "operation commit is not yet confirmed"
	}
	if errors.Is(err, ErrAccessDenied) {
		code = "access.denied"
	}
	if errors.Is(err, ErrDirectoryTooLarge) {
		code, msg = "manifest.too_large", "directory listing exceeds limit"
	}
	if errors.Is(err, ErrNotDirectory) {
		code, msg = "path.not_directory", "path is not a directory"
	}
	if errors.Is(err, errNotTreePack) {
		code, msg = "tree.not_pack", "archive is not a FileES tree pack"
	}
	if errors.Is(err, errTreeIncomplete) {
		code, msg = "tree.incomplete", "zip file count does not match the header"
	}
	if errors.Is(err, errTreePayloadCorrupt) {
		code, msg = "tree.payload_corrupt", "zip sha256 or size does not match the header"
	}
	if errors.Is(err, ErrDrawersUnavailable) {
		code, msg = "op.unsupported", "operation not supported"
	}
	if code == "worker.failed" || code == "storage.full" || code == "storage.unavailable" {
		d.Appender.Ledger.LogError(req.RequestID, d.ClientID, string(req.Operation), err.Error())
	}
	return d.writeErrorBody(out, req, v1.ErrorBody{Code: code, Message: msg})
}

func (d Dispatcher) writeErrorBody(out io.Writer, req v1.Request, body v1.ErrorBody) error {
	resp, err := v1.NewError(req.RequestID, req.Operation, body)
	if err != nil {
		return err
	}
	return writeResponse(out, resp, nil)
}

var errFrameHeaderTooLarge = errors.New("mobile frame header too large")

func writeResponse(out io.Writer, resp v1.Response, payload []byte) error {
	header, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if len(header) > v1.MaxHeaderBytes {
		return errFrameHeaderTooLarge
	}
	return v1.WriteFrame(out, v1.ResponseMagic, header, payload)
}
