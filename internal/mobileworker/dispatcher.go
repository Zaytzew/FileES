package mobileworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	v1 "filees/pkg/mobile/v1"
)

// Dispatcher serves exactly one framed mobile operation per invocation: it reads
// a request frame from in, routes it to the read-only browser or the append
// worker, and writes one response frame to out. ClientID is bound from the
// authenticated session (the forced command), never from the payload. It runs no
// listener and holds no state between calls.
type Dispatcher struct {
	Browser  Browser
	Appender Appender
	ClientID string
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

	case v1.OpReadObject:
		var p v1.ReadObjectPayload
		_ = json.Unmarshal(req.Payload, &p)
		var buf bytes.Buffer
		res, err := d.Browser.ReadObject(ctx, d.ClientID, p, &buf)
		if err != nil {
			return d.writeError(out, req, err)
		}
		return d.writeOK(out, req, res, buf.Bytes())

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
		return d.writeOK(out, req, d.status(p.TargetRequestID), nil)

	default:
		// LIST_DIRECTORY is optional and not yet implemented.
		body := v1.ErrorBody{Code: "op.unsupported", Message: "operation not supported"}
		return d.writeErrorBody(out, req, body)
	}
}

func (d Dispatcher) status(targetRequestID string) v1.OperationStatusResult {
	rec, err := d.Appender.Ledger.Lookup(targetRequestID)
	if err != nil || rec == nil {
		return v1.OperationStatusResult{State: v1.OpStateUnknown}
	}
	return v1.OperationStatusResult{State: rec.State, Revision: rec.Revision}
}

func (d Dispatcher) writeOK(out io.Writer, req v1.Request, result any, payload []byte) error {
	resp, err := v1.NewSuccess(req.RequestID, req.Operation, result)
	if err != nil {
		return err
	}
	return writeResponse(out, resp, payload)
}

// writeError maps an internal error to a domain code, never leaking raw tool text.
func (d Dispatcher) writeError(out io.Writer, req v1.Request, err error) error {
	code, msg := "worker.failed", "operation failed"
	if errors.Is(err, ErrAccessDenied) {
		code = "access.denied"
	}
	if errors.Is(err, errTreePayloadCorrupt) {
		code, msg = "tree.payload_corrupt", "zip sha256 or size does not match the header"
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

func writeResponse(out io.Writer, resp v1.Response, payload []byte) error {
	header, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return v1.WriteFrame(out, v1.ResponseMagic, header, payload)
}
