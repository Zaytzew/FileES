package whaleworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"filees/pkg/errcat"
	whale "filees/pkg/whale/v1"
)

type Dispatcher struct {
	Service  PutService
	ClientID string
}

func (d Dispatcher) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	header, err := whale.ReadHeader(reader, whale.RequestMagic)
	if err != nil {
		return err
	}
	request, err := whale.ParseRequest(header)
	if err != nil {
		return err
	}
	switch request.Operation {
	case whale.OpPutWindow:
		result, err := d.Service.ServeWindow(ctx, d.ClientID, request, reader, func(ready whale.PutResult) error {
			return writeResponse(out, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "continue", Result: &ready})
		})
		if err != nil {
			return d.writeError(out, request, err)
		}
		return writeResponse(out, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: &result})
	case whale.OpPutCommit:
		result, err := d.Service.Commit(ctx, d.ClientID, request.Identity)
		if err != nil {
			return d.writeError(out, request, err)
		}
		return writeResponse(out, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: &result})
	case whale.OpPutStatus:
		result, err := d.Service.Status(ctx, d.ClientID, request.Identity)
		if err != nil {
			return d.writeError(out, request, err)
		}
		return writeResponse(out, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: &result})
	default:
		return errors.New("unsupported Whale operation")
	}
}

func (d Dispatcher) writeError(out io.Writer, request whale.Request, err error) error {
	key := errcat.KeyWhaleFailed
	details := map[string]string(nil)
	var busy BusyError
	switch {
	case errors.As(err, &busy):
		key = errcat.KeyWhalePathBusy
		details = map[string]string{"queue_position": strconv.Itoa(busy.Position)}
	case errors.Is(err, ErrAccessDenied):
		key = errcat.KeyWhaleAccessDenied
	case errors.Is(err, ErrOffsetConflict):
		key = errcat.KeyWhaleOffsetConflict
	case errors.Is(err, ErrDigestMismatch):
		key = errcat.KeyWhaleDigestMismatch
	}
	spec, _ := errcat.ByKey(key)
	body := whale.ErrorBody{Code: string(spec.Code), Key: string(spec.Key), Message: spec.Diagnostic, Details: details}
	return writeResponse(out, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "error", Error: &body})
}

func writeResponse(out io.Writer, response whale.Response) error {
	header, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal Whale response: %w", err)
	}
	return whale.WriteFrame(out, whale.ResponseMagic, header)
}
