package sshtransport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	v1 "filees/pkg/mobile/v1"
)

func readResponse(stdout io.Reader, req v1.Request, sink io.Writer) (header, payload []byte, err error) {
	if sink == nil {
		return v1.ReadFrame(stdout, v1.ResponseMagic, v1.MaxHeaderBytes)
	}
	br := bufio.NewReader(stdout)
	header, err = v1.ReadHeader(br, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		return nil, nil, err
	}
	resp, err := v1.ParseResponse(header)
	if err != nil {
		return nil, nil, err
	}
	if resp.RequestID != req.RequestID || resp.Operation != req.Operation {
		return nil, nil, errors.New("response identity mismatch")
	}
	if resp.Status == v1.StatusOK {
		var result v1.ReadObjectResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, nil, err
		}
		var asked v1.ReadObjectPayload
		if err := json.Unmarshal(req.Payload, &asked); err != nil {
			return nil, nil, err
		}
		if result.Path != asked.Path || result.Sha256 == "" {
			return nil, nil, errors.New("read result path/hash mismatch")
		}
		if _, err := io.CopyN(sink, br, result.Size); err != nil {
			return nil, nil, fmt.Errorf("read object payload: %w", err)
		}
	}
	// Frame body must have precisely the declared size; errors have no body.
	if _, err := br.ReadByte(); err != io.EOF {
		if err == nil {
			err = errors.New("excess read object payload")
		}
		return nil, nil, err
	}
	return header, nil, nil
}
