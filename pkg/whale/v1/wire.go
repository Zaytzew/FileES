package v1

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/google/uuid"
)

const (
	RequestMagic         = "FILEES-WHALE/1"
	ResponseMagic        = "FILEES-WHALE-RESULT/1"
	MaxHeaderBytes       = 64 * 1024
	MaxWindowBytes int64 = 1024 * 1024 * 1024
	maxLineBytes         = 64
)

type Operation string

const (
	OpPutWindow Operation = "PUT_WINDOW"
	OpPutCommit Operation = "PUT_COMMIT"
	OpPutStatus Operation = "PUT_STATUS"
)

type Request struct {
	Schema      string    `json:"schema"`
	RequestID   string    `json:"request_id"`
	Operation   Operation `json:"operation"`
	Identity    Identity  `json:"identity"`
	Offset      int64     `json:"offset,omitempty"`
	PayloadSize int64     `json:"payload_size,omitempty"`
}

type Response struct {
	Schema    string     `json:"schema"`
	RequestID string     `json:"request_id"`
	Operation Operation  `json:"operation"`
	Status    string     `json:"status"`
	Result    *PutResult `json:"result,omitempty"`
	Error     *ErrorBody `json:"error,omitempty"`
}

type PutResult struct {
	GenerationID string `json:"generation_id"`
	Offset       int64  `json:"offset"`
	State        State  `json:"state"`
	Revision     int64  `json:"revision,omitempty"`
}

type ErrorBody struct {
	Code    string            `json:"code"`
	Key     string            `json:"key"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func (r Request) Validate() error {
	if r.Schema != Schema {
		return fmt.Errorf("unsupported Whale schema %q", r.Schema)
	}
	if id, err := uuid.Parse(r.RequestID); err != nil || id.String() != r.RequestID {
		return errors.New("request_id must be a canonical UUID")
	}
	if err := r.Identity.Validate(); err != nil {
		return err
	}
	switch r.Operation {
	case OpPutWindow:
		if err := ValidateOffset(r.Offset, r.Identity.ExpectedSize); err != nil {
			return err
		}
		if r.PayloadSize < 1 || r.PayloadSize > MaxWindowBytes || r.PayloadSize > r.Identity.ExpectedSize-r.Offset {
			return fmt.Errorf("payload_size must fit the generation and be in range 1..%d", MaxWindowBytes)
		}
	case OpPutCommit, OpPutStatus:
		if r.Offset != 0 || r.PayloadSize != 0 {
			return errors.New("status and commit requests cannot carry offset or payload")
		}
	default:
		return fmt.Errorf("unsupported Whale operation %q", r.Operation)
	}
	return nil
}

func ParseRequest(raw []byte) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("trailing JSON value")
	}
	return request, request.Validate()
}

func ParseResponse(raw []byte) (Response, error) {
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return Response{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Response{}, errors.New("trailing JSON value")
	}
	if response.Schema != Schema {
		return Response{}, fmt.Errorf("unsupported Whale schema %q", response.Schema)
	}
	if id, err := uuid.Parse(response.RequestID); err != nil || id.String() != response.RequestID {
		return Response{}, errors.New("request_id must be a canonical UUID")
	}
	switch response.Status {
	case "continue", "ok":
		if response.Result == nil || response.Error != nil {
			return Response{}, errors.New("successful Whale response must carry only result")
		}
	case "error":
		if response.Error == nil || response.Result != nil || response.Error.Code == "" || response.Error.Key == "" || response.Error.Message == "" {
			return Response{}, errors.New("failed Whale response must carry only error")
		}
	default:
		return Response{}, errors.New("invalid Whale response status")
	}
	return response, nil
}

func WriteFrame(out io.Writer, magic string, header []byte) error {
	if len(header) > MaxHeaderBytes {
		return errors.New("Whale frame header exceeds limit")
	}
	_, err := fmt.Fprintf(out, "%s\n%d\n", magic, len(header))
	if err == nil {
		_, err = out.Write(header)
	}
	return err
}

func ReadHeader(reader *bufio.Reader, magic string) ([]byte, error) {
	got, err := readLine(reader)
	if err != nil {
		return nil, err
	}
	if got != magic {
		return nil, fmt.Errorf("unexpected Whale frame magic %q", got)
	}
	length, err := readLine(reader)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(length)
	if err != nil || n < 1 || n > MaxHeaderBytes {
		return nil, errors.New("invalid Whale frame header length")
	}
	header := make([]byte, n)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	return header, nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line := make([]byte, 0, maxLineBytes)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return string(line), nil
		}
		if len(line) == maxLineBytes {
			return "", errors.New("Whale frame line exceeds limit")
		}
		line = append(line, b)
	}
}
