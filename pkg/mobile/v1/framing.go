package v1

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Wire framing for one operation per SSH session:
//
//	<magic>\n
//	<header-length>\n
//	<header-json>
//	<payload-bytes>
//
// The header JSON is a Request or Response envelope. Payload bytes (upload
// content, or read-object stream) run to end of stream — the session boundary
// delimits them, so no explicit payload-length line is needed. Header length is
// bounded to keep a malformed stream from allocating without limit.
const (
	RequestMagic  = "FILEES-MOBILE/1"
	ResponseMagic = "FILEES-MOBILE-RESULT/1"

	// MaxHeaderBytes bounds the header JSON. Payload size limits are per-operation
	// and enforced by the worker from the header, not here.
	MaxHeaderBytes = 64 * 1024
	maxLineBytes   = 64
)

// WriteFrame writes a complete frame with a buffered payload.
func WriteFrame(w io.Writer, magic string, header, payload []byte) error {
	if len(header) > MaxHeaderBytes {
		return fmt.Errorf("frame header exceeds %d bytes", MaxHeaderBytes)
	}
	if _, err := io.WriteString(w, magic+"\n"+strconv.Itoa(len(header))+"\n"); err != nil {
		return err
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadHeader reads and validates the magic and header of a frame, leaving the
// payload buffered in br for the caller to stream. maxHeader bounds the header
// JSON; pass MaxHeaderBytes for the default.
func ReadHeader(br *bufio.Reader, wantMagic string, maxHeader int) ([]byte, error) {
	magic, err := readLimitedLine(br, maxLineBytes)
	if err != nil {
		return nil, fmt.Errorf("read frame magic: %w", err)
	}
	if magic != wantMagic {
		return nil, fmt.Errorf("unexpected frame magic %q", magic)
	}
	lenLine, err := readLimitedLine(br, maxLineBytes)
	if err != nil {
		return nil, fmt.Errorf("read frame header length: %w", err)
	}
	n, err := strconv.Atoi(lenLine)
	if err != nil || n < 0 {
		return nil, errors.New("invalid frame header length")
	}
	if n > maxHeader {
		return nil, fmt.Errorf("frame header exceeds %d bytes", maxHeader)
	}
	header := make([]byte, n)
	if _, err := io.ReadFull(br, header); err != nil {
		return nil, fmt.Errorf("read frame header: %w", err)
	}
	return header, nil
}

// ReadFrame reads a full frame, buffering the payload. Use ReadHeader with the
// returned reader when the payload is large and should be streamed.
func ReadFrame(r io.Reader, wantMagic string, maxHeader int) (header, payload []byte, err error) {
	br := bufio.NewReader(r)
	header, err = ReadHeader(br, wantMagic, maxHeader)
	if err != nil {
		return nil, nil, err
	}
	payload, err = io.ReadAll(br)
	if err != nil {
		return nil, nil, fmt.Errorf("read frame payload: %w", err)
	}
	return header, payload, nil
}

// readLimitedLine reads up to max bytes ending in '\n' and returns the line with
// the trailing "\r\n"/"\n" stripped. It refuses an unterminated over-long line so
// a stream without a newline cannot force unbounded reads.
func readLimitedLine(br *bufio.Reader, max int) (string, error) {
	buf := make([]byte, 0, max)
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			if len(buf) > 0 && buf[len(buf)-1] == '\r' {
				buf = buf[:len(buf)-1]
			}
			return string(buf), nil
		}
		if len(buf) >= max {
			return "", errors.New("frame line exceeds limit")
		}
		buf = append(buf, b)
	}
}
